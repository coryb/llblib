package llblib

import (
	"context"

	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/errdefs"
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
	// latera
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

	op, err := opFromInfo(ctx, ei)
	if err != nil {
		return errors.Wrap(err, "failed to extract op from exec info")
	}

	mergeExecOp(se.Op.GetExec(), op.GetExec())

	containerOpts := containerFromOp(se.Op)
	for i := range containerOpts.Mounts {
		if i >= len(se.MountIDs) {
			break
		}
		containerOpts.Mounts[i].ResultID = se.MountIDs[i]
	}
	// // also add input as mounts so we might be able to to compare inputs
	// // from outputs in case we need to detect or manually export changes.
	// inputMounts := containerOpts.Mounts
	// for i, inputMount := range inputMounts {
	// 	if inputMount.MountType == pb.MountType_BIND {
	// 		inputMount.ResultID = se.InputIDs[i]
	// 		if inputMount.Dest == "/" {
	// 			inputMount.Dest = "/input"
	// 		} else {
	// 			inputMount.Dest += "-input"
	// 		}
	// 		containerOpts.Mounts = append(containerOpts.Mounts, inputMount)
	// 	}
	// }
	for _, opt := range opts {
		opt.SetContainerOptions(containerOpts)
	}
	return runContainer(ctx, c, containerOpts)
}
