package llblib

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"

	"braces.dev/errtrace"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/entitlements"
	"github.com/muesli/cancelreader"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"
)

// unsetArgsSentinel is used to populate llb.Args for cases where we don't
// actually want the Args from the state.  Args are required via the ExecOp
// Validate when we try marshal the ExecOp when trying to extract details to
// apply to gateway.NewContainerRequest and gateway.StartRequest.
const unsetArgsSentinel = ""

// ContainerOption allows configuring an ad-hoc container.
type ContainerOption interface {
	SetContainerOptions(*ContainerOptions)
}

type containerOptionFunc func(*ContainerOptions)

func (f containerOptionFunc) SetContainerOptions(co *ContainerOptions) {
	f(co)
}

// ContainerOptions are options used to create ad-hoc containers in buildkit.
type ContainerOptions struct {
	// NewContainerRequest describes the state of the container to be created.
	gateway.NewContainerRequest
	// StartRequest describes the process to be run (pid 1) in the container.
	gateway.StartRequest
	// Resize is used to send tty resize events
	Resize <-chan gateway.WinSize
	// Signal is used to send signals to the pid 1 process
	Signal <-chan syscall.Signal
	// Setup are callbacks that will be executed after the pid 1 process has
	// started.
	Setup []func(context.Context) error
	// Teardown are callbacks that are executed after the pid 1 has exited
	Teardown []func() error
	runOpts  []llb.RunOption
	// dropErr will suppress the original build error when
	// handling onError containers
	dropErr      bool
	entitlements []entitlements.Entitlement
	lock         sync.Locker
}

// FdReader is an io.Reader that has a Fd file descriptor.
type FdReader interface {
	io.Reader
	Fd() uintptr
}

type noopWriteCloser struct {
	io.Writer
}

func (noopWriteCloser) Close() error {
	return nil
}

// WithTTY will run the container with the provided in/out/err connected to the
// tty in the container.  Resize events will automatically be propagated.
func WithTTY(in FdReader, outW, errW io.Writer) ContainerOption {
	return containerOptionFunc(func(co *ContainerOptions) {
		co.Tty = true
		inReader, err := cancelreader.NewReader(in)
		if err != nil {
			panic(fmt.Sprintf("create cancel reader: %s", err))
		}
		WithInput(inReader).SetContainerOptions(co)
		WithOutput(outW, errW).SetContainerOptions(co)

		co.Setup = append(co.Setup, func(ctx context.Context) error {
			oldState, err := term.MakeRaw(int(in.Fd()))
			if err != nil {
				return errtrace.Errorf("failed to set terminal input to raw mode: %w", err)
			}
			co.Teardown = append(co.Teardown, func() error {
				return errtrace.Wrap(term.Restore(int(in.Fd()), oldState))
			})
			co.Teardown = append(co.Teardown, func() error {
				inReader.Cancel()
				return nil
			})
			return nil
		})
		handleResize(co, int(in.Fd()))
	})
}

// WithInput will set stdin in the container to the provided reader.
func WithInput(in io.Reader) ContainerOption {
	return containerOptionFunc(func(co *ContainerOptions) {
		if closer, ok := in.(io.ReadCloser); ok {
			co.Stdin = closer
		} else {
			co.Stdin = io.NopCloser(in)
		}
	})
}

// WithOutput will set the stdout and stderr in the container to the provided
// writers.
func WithOutput(out, err io.Writer) ContainerOption {
	return containerOptionFunc(func(co *ContainerOptions) {
		if closer, ok := out.(io.WriteCloser); ok {
			co.Stdout = closer
		} else {
			co.Stdout = noopWriteCloser{out}
		}
		if closer, ok := err.(io.WriteCloser); ok {
			co.Stderr = closer
		} else {
			co.Stderr = noopWriteCloser{err}
		}
	})
}

// WithRun will apply the provide llb.RunOption to the container process.  This
// can be used to set the command to be run and mounts etc.
//
//	llblib.WithRun(
//		llb.AddMount("/", llb.Image("busybox", llb.LinuxArm64)),
//		llb.Args([]string{"/bin/sh"}),
//	)
func WithRun(opts ...llb.RunOption) ContainerOption {
	return containerOptionFunc(func(co *ContainerOptions) {
		co.runOpts = append(co.runOpts, opts...)
	})
}

// WithSetup can be used to start callbacks after the container process has
// started.
func WithSetup(s func(ctx context.Context) error) ContainerOption {
	return containerOptionFunc(func(co *ContainerOptions) {
		co.Setup = append(co.Setup, s)
	})
}

// WithTeardown can be used to cleanup resources after the container process has
// exited.
func WithTeardown(t func() error) ContainerOption {
	return containerOptionFunc(func(co *ContainerOptions) {
		co.Teardown = append(co.Teardown, t)
	})
}

// WithLock allows for synchronizing access to the container execution so only
// one is ever running at at time.  This is useful with OnError to prevent
// multiple error handlers from running at the same time when parallel solves
// are running.
func WithLock(l sync.Locker) ContainerOption {
	return containerOptionFunc(func(co *ContainerOptions) {
		co.lock = l
	})
}

func (s *solver) Container(root llb.State, opts ...ContainerOption) Request {
	var allowedEntitlements []entitlements.Entitlement
	tmp := ContainerOptions{}
	for _, opt := range opts {
		opt.SetContainerOptions(&tmp)
	}
	allowedEntitlements = tmp.entitlements
	build := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		if tmp.lock != nil {
			tmp.lock.Lock()
			defer tmp.lock.Unlock()
		}
		ei := llb.ExecInfo{
			State: root,
		}
		// first get the run opts so we can setup defaults, then we will
		// re-apply the container options again to the "real" ContainerOptions
		// later

		for _, opt := range tmp.runOpts {
			opt.SetRunOption(&ei)
		}

		op, err := opFromInfo(ctx, ei)
		if err != nil {
			return nil, errtrace.Errorf("failed to create exec from run options: %w", err)
		}

		containerOpts := containerFromOp(op)

		mountStates := map[string]llb.State{
			"/": root,
		}
		for _, m := range ei.Mounts {
			mountStates[m.Target] = llb.NewState(m.Source)
		}

		for i, m := range containerOpts.Mounts {
			var ref gateway.Reference
			if st, ok := mountStates[m.Dest]; ok && m.MountType == pb.MountType_BIND {
				def, err := st.Marshal(ctx)
				if err != nil {
					return nil, errtrace.Errorf("failed to mount state for %s: %w", m.Dest, err)
				}

				r, err := c.Solve(ctx, gateway.SolveRequest{
					Evaluate:   true,
					Definition: def.ToPB(),
				})
				if err != nil {
					return nil, errtrace.Errorf("failed to solve mount state for %s: %w", m.Dest, err)
				}
				ref = r.Ref
			}
			containerOpts.Mounts[i].Ref = ref
		}

		for _, opt := range opts {
			opt.SetContainerOptions(containerOpts)
		}

		if err := runContainer(ctx, c, containerOpts); err != nil {
			return nil, errtrace.Errorf("failed to run container: %w", err)
		}
		return gateway.NewResult(), nil
	}
	return Request{
		buildFunc:    build,
		entitlements: allowedEntitlements,
	}
}

func runContainer(ctx context.Context, c gateway.Client, co *ContainerOptions) error {
	ctr, err := c.NewContainer(ctx, co.NewContainerRequest)
	if err != nil {
		return errtrace.Errorf("failed to create breakpoint container: %w", err)
	}
	defer ctr.Release(context.Background())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	eg, ctx := errgroup.WithContext(ctx)

	proc, err := ctr.Start(ctx, co.StartRequest)
	if err != nil {
		return errtrace.Errorf("failed to start breakpoint process: %w", err)
	}

	LoadProgress(ctx).Pause()
	defer LoadProgress(ctx).Resume()

	for _, f := range co.Setup {
		if err := f(ctx); err != nil {
			return errtrace.Errorf("setup failed before container process created: %w", err)
		}
	}

	eg.Go(func() error {
		<-ctx.Done() // context cancelled when container exits
		var err error
		for _, f := range co.Teardown {
			if tearErr := f(); err != nil {
				err = errors.Join(err, tearErr)
			}
		}
		if err != nil {
			return errtrace.Errorf("teardown failed after container exit: %w", err)
		}
		return nil
	})

	if co.Resize != nil {
		eg.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case winsize := <-co.Resize:
					if err := proc.Resize(ctx, winsize); err != nil {
						return errtrace.Errorf("failed to send resize to process: %w", err)
					}
				}
			}
		})
	}

	if co.Signal != nil {
		eg.Go(func() error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case sig := <-co.Signal:
					if err := proc.Signal(ctx, sig); err != nil {
						return errtrace.Errorf("failed to send signal to process: %w", err)
					}
				}
			}
		})
	}

	if err := proc.Wait(); err != nil {
		return errtrace.Errorf("container process failed: %w", err)
	}
	cancel()
	if err := eg.Wait(); err != nil {
		return errtrace.Errorf("container process event error: %w", err)
	}
	return nil
}

func opFromInfo(ctx context.Context, ei llb.ExecInfo) (*pb.Op, error) {
	execOp := llb.NewExecOp(ei.State, ei.ProxyEnv, ei.ReadonlyRootFS, ei.Constraints)

	for _, m := range ei.Mounts {
		execOp.AddMount(m.Target, m.Source, m.Opts...)
	}

	_, dt, _, _, err := execOp.Marshal(ctx, &ei.Constraints)
	if err != nil {
		return nil, errtrace.Errorf("failed to marshal exec op: %w", err)
	}

	pbOp := pb.Op{}
	if err := pbOp.Unmarshal(dt); err != nil {
		return nil, errtrace.Errorf("failed to unmarshal execOp definition: %w", err)
	}
	pbExec := pbOp.GetExec()
	applyExecInfo(pbExec, ei)
	return &pbOp, nil
}

func applyExecInfo(exec *pb.ExecOp, ei llb.ExecInfo) {
	// We need to emulate the execOp created by Run, so secrets and
	// ssh are missing and we cannot add them to the execOp before
	// we marshal because they are private fields.
	for _, s := range ei.Secrets {
		exec.Mounts = append(exec.Mounts, &pb.Mount{
			Dest:      *s.Target,
			MountType: pb.MountType_SECRET,
			SecretOpt: &pb.SecretOpt{
				ID:       s.ID,
				Uid:      uint32(s.UID),
				Gid:      uint32(s.GID),
				Optional: s.Optional,
				Mode:     uint32(s.Mode),
			},
		})
	}

	for _, s := range ei.SSH {
		pm := &pb.Mount{
			Dest:      s.Target,
			MountType: pb.MountType_SSH,
			SSHOpt: &pb.SSHOpt{
				ID:       s.ID,
				Uid:      uint32(s.UID),
				Gid:      uint32(s.GID),
				Mode:     uint32(s.Mode),
				Optional: s.Optional,
			},
		}
		exec.Mounts = append(exec.Mounts, pm)
	}
}

func mergeExecOp(dest *pb.ExecOp, src *pb.ExecOp) {
	if len(src.Meta.Args) > 0 && src.Meta.Args[0] != unsetArgsSentinel {
		dest.Meta.Args = src.Meta.Args
	}
	if src.Meta.Cwd != "" && src.Meta.Cwd != "/" {
		dest.Meta.Cwd = src.Meta.Cwd
	}
	if src.Meta.User != "" {
		dest.Meta.User = src.Meta.User
	}
	if src.Meta.ProxyEnv != nil {
		dest.Meta.ProxyEnv = src.Meta.ProxyEnv
	}
	dest.Meta.ExtraHosts = append(dest.Meta.ExtraHosts, src.Meta.ExtraHosts...)
	if src.Meta.Hostname != "" {
		dest.Meta.Hostname = src.Meta.Hostname
	}
	dest.Meta.Ulimit = append(dest.Meta.Ulimit, src.Meta.Ulimit...)
	if src.Meta.CgroupParent != "" {
		dest.Meta.CgroupParent = src.Meta.CgroupParent
	}
	if src.Meta.RemoveMountStubsRecursive {
		dest.Meta.RemoveMountStubsRecursive = true
	}
	// dont append the root mount when merging
	dest.Mounts = append(dest.Mounts, src.Mounts[1:]...)
	if src.Network != pb.NetMode_UNSET {
		dest.Network = src.Network
	}
	// skipping Security, there is no way to tell the difference
	// between the default unset (sandbox) and explicitly wanting
	// sandbox to override what is in dest, so users will have to
	// set Security explicitly in dest
	dest.Secretenv = append(dest.Secretenv, src.Secretenv...)
}

func convertMounts(m []*pb.Mount) []gateway.Mount {
	mounts := []gateway.Mount{}
	for _, m := range m {
		mounts = append(mounts, gateway.Mount{
			Selector:  m.Selector,
			Dest:      m.Dest,
			Readonly:  m.Readonly,
			MountType: m.MountType,
			CacheOpt:  m.CacheOpt,
			SecretOpt: m.SecretOpt,
			SSHOpt:    m.SSHOpt,
		})
	}
	return mounts
}

func containerFromOp(op *pb.Op) *ContainerOptions {
	exec := op.GetExec()
	mounts := convertMounts(exec.Mounts)
	return &ContainerOptions{
		NewContainerRequest: gateway.NewContainerRequest{
			Mounts:      mounts,
			NetMode:     exec.Network,
			ExtraHosts:  exec.Meta.ExtraHosts,
			Platform:    op.Platform,
			Constraints: op.Constraints,
		},
		StartRequest: gateway.StartRequest{
			Args:         exec.Meta.Args,
			Env:          exec.Meta.Env,
			User:         exec.Meta.User,
			Cwd:          exec.Meta.Cwd,
			SecurityMode: exec.Security,
		},
	}
}
