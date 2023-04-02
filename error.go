package llblib

import (
	"context"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
)

func OnError(opts ...ContainerOption) RequestOption {
	return requestOptionFunc(func(r *Request) {
		r.evaluate = true
		r.onError = func(ctx context.Context, c gateway.Client, err error) error {
			if err == nil {
				return nil
			}
			var se *errdefs.SolveError
			if errors.As(err, &se) {
				return errContainer(ctx, c, se, opts...)
			}
			return nil // the original error is handled externally
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
		return errors.Wrap(err, "failed to extract op from exec info")
	}

	mergeExecOp(se.Op.GetExec(), op.GetExec())

	containerOpts := containerFromOp(se.Op)
	for i, m := range containerOpts.Mounts {
		if i >= len(se.MountIDs) {
			if st, ok := mountStates[m.Dest]; ok && m.MountType == pb.MountType_BIND {
				def, err := st.Marshal(ctx)
				if err != nil {
					return errors.Wrapf(err, "failed to mount state for %s", m.Dest)
				}

				r, err := c.Solve(ctx, client.SolveRequest{
					Definition: def.ToPB(),
				})
				if err != nil {
					return errors.Wrapf(err, "failed to solve mount state for %s", m.Dest)
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
	return runContainer(ctx, c, containerOpts)
}
