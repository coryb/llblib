package llblib

import (
	"context"
	goerrors "errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh/terminal"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sys/unix"
)

type BreakpointOption interface {
	SetContainerOptions(*ContainerOptions)
}

type breakpointOptionFunc func(*ContainerOptions)

func (f breakpointOptionFunc) SetContainerOptions(co *ContainerOptions) {
	f(co)
}

type ContainerOptions struct {
	gateway.NewContainerRequest
	gateway.StartRequest
	Resize   <-chan gateway.WinSize
	Signal   <-chan syscall.Signal
	Setup    []func(context.Context) error
	Teardown []func() error
}

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

func WithTTY(in FdReader, out, err io.Writer) BreakpointOption {
	return breakpointOptionFunc(func(co *ContainerOptions) {
		co.Tty = true
		co.Stdin = io.NopCloser(in)
		co.Stdout = noopWriteCloser{out}
		co.Stderr = noopWriteCloser{err}
		co.Setup = append(co.Setup, func(ctx context.Context) error {
			oldState, err := terminal.MakeRaw(int(in.Fd()))
			if err != nil {
				return errors.Wrap(err, "failed to set terminal input to raw mode")
			}
			co.Teardown = append(co.Teardown, func() error {
				return terminal.Restore(int(in.Fd()), oldState)
			})
			resize := make(chan gateway.WinSize, 1)
			co.Resize = resize

			ch := make(chan os.Signal, 1)
			ch <- syscall.SIGWINCH // Initial resize.

			var eg errgroup.Group
			eg.Go(func() error {
				for {
					select {
					case <-ctx.Done():
						close(ch)
						return nil
					case <-ch:
						ws, err := unix.IoctlGetWinsize(int(in.Fd()), unix.TIOCGWINSZ)
						if err != nil {
							return errors.Wrap(err, "failed to get winsize")
						}
						resize <- gateway.WinSize{
							Cols: uint32(ws.Col),
							Rows: uint32(ws.Row),
						}
					}
				}
			})
			signal.Notify(ch, syscall.SIGWINCH)
			co.Teardown = append(co.Teardown, func() error {
				signal.Stop(ch)
				return nil
			}, func() error {
				if err := eg.Wait(); err != nil {
					return errors.Wrap(err, "SIGWINCH event loop failed")
				}
				return nil
			})
			return nil
		})
	})
}

func WithInput(in io.ReadCloser) BreakpointOption {
	return breakpointOptionFunc(func(co *ContainerOptions) {
		co.Stdin = in
	})
}

func WithOutput(out, err io.WriteCloser) BreakpointOption {
	return breakpointOptionFunc(func(co *ContainerOptions) {
		co.Stdout = out
		co.Stderr = err
	})
}

func (s *solver) Breakpoint(root llb.State, runOpts ...llb.RunOption) func(opts ...BreakpointOption) Request {
	return func(opts ...BreakpointOption) Request {
		build := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			ei := llb.ExecInfo{
				State: root,
			}
			for _, opt := range runOpts {
				opt.SetRunOption(&ei)
			}

			// lots of variables we need are only available via the proto, so lets
			// serialize an exec op to proto, then unmarshal back into a struct
			// we can interrogate
			execOp := llb.NewExecOp(ei.State, ei.ProxyEnv, ei.ReadonlyRootFS, ei.Constraints)

			mountStates := map[string]llb.State{
				"/": root,
			}

			for _, m := range ei.Mounts {
				mountStates[m.Target] = llb.NewState(m.Source)
				execOp.AddMount(m.Target, m.Source, m.Opts...)
			}

			_, dt, _, _, err := execOp.Marshal(ctx, &ei.Constraints)
			if err != nil {
				return nil, errors.Wrap(err, "failed to marshal exec op")
			}

			pbOp := pb.Op{}
			if err := pbOp.Unmarshal(dt); err != nil {
				return nil, errors.Wrap(err, "failed to unmarshal execOp definition")
			}
			pbExec := pbOp.GetExec()

			// We need to emulate the execOp created by Run, so secrets and
			// ssh are missing and we cannot add them to the execOp before
			// we marshal because they are private fields.
			for _, s := range ei.Secrets {
				pbExec.Mounts = append(pbExec.Mounts, &pb.Mount{
					Dest:      s.Target,
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
				pbExec.Mounts = append(pbExec.Mounts, pm)
			}

			mounts := []gateway.Mount{}
			for _, m := range pbExec.Mounts {
				var ref client.Reference
				if st, ok := mountStates[m.Dest]; ok && m.MountType == pb.MountType_BIND {
					def, err := st.Marshal(ctx)
					if err != nil {
						return nil, errors.Wrapf(err, "failed to mount state for %s", m.Dest)
					}

					r, err := c.Solve(ctx, client.SolveRequest{
						Definition: def.ToPB(),
					})
					if err != nil {
						return nil, errors.Wrapf(err, "failed to solve mount state for %s", m.Dest)
					}
					ref = r.Ref
				}
				mounts = append(mounts, gateway.Mount{
					Selector:  m.Selector,
					Dest:      m.Dest,
					ResultID:  m.ResultID,
					Ref:       ref,
					Readonly:  m.Readonly,
					MountType: m.MountType,
					CacheOpt:  m.CacheOpt,
					SecretOpt: m.SecretOpt,
					SSHOpt:    m.SSHOpt,
				})
			}

			containerOpts := ContainerOptions{
				NewContainerRequest: gateway.NewContainerRequest{
					Mounts:      mounts,
					NetMode:     pbExec.Network,
					ExtraHosts:  pbExec.Meta.ExtraHosts,
					Platform:    pbOp.Platform,
					Constraints: pbOp.Constraints,
				},
				StartRequest: gateway.StartRequest{
					Args:         pbExec.Meta.Args,
					Env:          pbExec.Meta.Env,
					User:         pbExec.Meta.User,
					Cwd:          pbExec.Meta.Cwd,
					SecurityMode: pbExec.Security,
				},
			}
			for _, opt := range opts {
				opt.SetContainerOptions(&containerOpts)
			}

			ctr, err := c.NewContainer(ctx, containerOpts.NewContainerRequest)
			if err != nil {
				return nil, errors.Wrap(err, "failed to create breakpoint container")
			}
			defer ctr.Release(context.Background())

			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			eg, ctx := errgroup.WithContext(ctx)

			proc, err := ctr.Start(ctx, containerOpts.StartRequest)
			if err != nil {
				return nil, errors.Wrap(err, "failed to start breakpoint process")
			}

			LoadProgress(ctx).Pause()
			defer LoadProgress(ctx).Resume()

			for _, f := range containerOpts.Setup {
				if err := f(ctx); err != nil {
					return nil, errors.Wrap(err, "setup failed before container process created")
				}
			}
			eg.Go(func() error {
				<-ctx.Done() // context cancelled when container exits
				var err error
				for _, f := range containerOpts.Teardown {
					if err := f(); err != nil {
						err = goerrors.Join(err, err)
					}
				}
				if err != nil {
					return errors.Wrap(err, "teardown failed after container exit")
				}
				return nil
			})

			if containerOpts.Resize != nil {
				eg.Go(func() error {
					for {
						select {
						case <-ctx.Done():
							return nil
						case winsize := <-containerOpts.Resize:
							if err := proc.Resize(ctx, winsize); err != nil {
								return errors.Wrap(err, "failed to send resize to process")
							}
						}
					}
				})
			}
			if containerOpts.Signal != nil {
				eg.Go(func() error {
					for {
						select {
						case <-ctx.Done():
							return nil
						case sig := <-containerOpts.Signal:
							if err := proc.Signal(ctx, sig); err != nil {
								return errors.Wrap(err, "failed to send signal to process")
							}
						}
					}
				})
			}

			if err := proc.Wait(); err != nil {
				return nil, errors.Wrapf(err, "container process failed")
			}
			cancel()
			if err := eg.Wait(); err != nil {
				return nil, errors.Wrapf(err, "container process event error")
			}

			return gateway.NewResult(), nil
		}
		return Request{
			buildFunc: build,
		}
	}
}
