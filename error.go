package llblib

import (
	"context"
	"errors"

	"braces.dev/errtrace"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
)

// DropBuildError will cause OnError handlers to suppress the original error
// that triggered the OnError event.
func DropBuildError(b bool) ContainerOption {
	return containerOptionFunc(func(co *ContainerOptions) {
		co.dropErr = b
	})
}

// OnError can be used to modify a Request to run a customized container with
// with the mount context from the failed buildkit request.  This can be used
// for special handling of solves that are not "exportable" from buildkit since
// buildkit will not export a failed solve request.  For example to get a
// shell in a container with the same modified "dirty" state of the files post
//
//	llblib.OnError(
//		llblib.WithTTY(os.Stdin, os.Stdout, os.Stderr),
//		llblib.WithRun(llb.Args([]string{"/bin/sh"})),
//	)
func OnError(opts ...ContainerOption) RequestOption {
	co := &ContainerOptions{}
	for _, opt := range opts {
		opt.SetContainerOptions(co)
	}
	return requestOptionFunc(func(r *Request) {
		r.evaluate = true
		r.onError = func(ctx context.Context, c gateway.Client, err error) (bool, error) {
			if err == nil {
				return false, nil
			}
			var se *errdefs.SolveError
			if errors.As(err, &se) {
				return co.dropErr, errtrace.Wrap(errContainer(ctx, c, se, opts...))
			}
			return false, nil // the original error is handled externally
		}
	})
}

func errContainer(ctx context.Context, c gateway.Client, se *errdefs.SolveError, opts ...ContainerOption) error {
	if se.Op.GetExec() == nil {
		return nil
	}

	// first get the run opts so we can setup defaults, then we will
	// re-apply the container options again to the "real" ContainerOptions
	// later
	tmp := ContainerOptions{}
	for _, opt := range opts {
		opt.SetContainerOptions(&tmp)
	}

	// ensure there is a default llb.Args available, otherwise we will get
	// an error when we try to marshal the execOp later into a pb.Op
	runOpts := append([]llb.RunOption{llb.Args([]string{unsetArgsSentinel})}, tmp.runOpts...)
	ei := llb.ExecInfo{
		State: llb.Scratch(),
	}
	for _, opt := range runOpts {
		opt.SetRunOption(&ei)
	}

	mountStates := map[string]llb.State{}
	for _, m := range ei.Mounts {
		mountStates[m.Target] = llb.NewState(m.Source)
	}

	op, err := opFromInfo(ctx, ei)
	if err != nil {
		return errtrace.Errorf("failed to extract op from exec info: %w", err)
	}

	mergeExecOp(se.Op.GetExec(), op.GetExec())

	containerOpts := containerFromOp(se.Op)
	for i, m := range containerOpts.Mounts {
		if i >= len(se.MountIDs) {
			if st, ok := mountStates[m.Dest]; ok && m.MountType == pb.MountType_BIND {
				def, err := st.Marshal(ctx)
				if err != nil {
					return errtrace.Errorf("failed to mount state for %s: %w", m.Dest, err)
				}

				r, err := c.Solve(ctx, gateway.SolveRequest{
					Definition: def.ToPB(),
				})
				if err != nil {
					return errtrace.Errorf("failed to solve mount state for %s: %w", m.Dest, err)
				}
				containerOpts.Mounts[i].Ref = r.Ref
			}
			continue
		}
		containerOpts.Mounts[i].ResultID = se.MountIDs[i]
	}

	for _, opt := range opts {
		opt.SetContainerOptions(containerOpts)
	}
	return errtrace.Wrap(runContainer(ctx, c, containerOpts))
}
