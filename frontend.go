package llblib

import (
	"context"

	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
)

type FrontendOption interface {
	SetFrontendOption(*frontendOptions)
}

type frontendOptionFunc func(*frontendOptions)

func (f frontendOptionFunc) SetFrontendOption(fo *frontendOptions) {
	f(fo)
}

func FrontendInput(name string, st llb.State) FrontendOption {
	return frontendOptionFunc(func(fo *frontendOptions) {
		fo.Inputs[name] = st
	})
}

func FrontendOpt(name, value string) FrontendOption {
	return frontendOptionFunc(func(fo *frontendOptions) {
		fo.Opts[name] = value
	})
}

func WithCustomName(name string) FrontendOption {
	return frontendOptionFunc(func(fo *frontendOptions) {
		fo.ConstraintsOpts = append(fo.ConstraintsOpts, llb.WithCustomName(name))
	})
}

type frontendOptions struct {
	Inputs          map[string]llb.State
	Opts            map[string]string
	ConstraintsOpts []llb.ConstraintsOpt
}

type constraintsToOptions struct {
	NullOption
	source *llb.Constraints
	opts   []llb.ConstraintsOpt
}

func (o constraintsToOptions) SetConstraintsOption(c *llb.Constraints) {
	if o.source != nil {
		*c = *o.source
	}
	for _, opt := range o.opts {
		opt.SetConstraintsOption(c)
	}
}

func Frontend(source string, opts ...FrontendOption) llb.State {
	return llb.Scratch().Async(func(ctx context.Context, st llb.State, constraints *llb.Constraints) (llb.State, error) {
		fo := frontendOptions{
			Inputs: map[string]llb.State{},
			Opts: map[string]string{
				"source": source,
			},
		}
		for _, opt := range opts {
			opt.SetFrontendOption(&fo)
		}

		sess := LoadSession(ctx)

		var constrainOpt llb.ConstraintsOpt = constraintsToOptions{
			source: constraints,
			opts:   fo.ConstraintsOpts,
		}

		var result llb.State
		req := Request{
			buildFunc: func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
				inputs := map[string]*pb.Definition{}
				for name, input := range fo.Inputs {
					def, err := input.Marshal(ctx, constrainOpt)
					if err != nil {
						return nil, err
					}
					inputs[name] = def.ToPB()
				}
				req := gateway.SolveRequest{
					Frontend:       "gateway.v0",
					FrontendOpt:    fo.Opts,
					FrontendInputs: inputs,
				}
				res, err := c.Solve(ctx, req)
				if err != nil {
					return nil, errors.Wrap(err, "failed to solve frontend request")
				}

				ref, err := res.SingleRef()
				if err != nil {
					return nil, errors.Wrap(err, "failed to extract ref from frontend request")
				}
				if ref == nil {
					result = llb.Scratch()
				} else {
					result, err = ref.ToState()
					if err != nil {
						return nil, errors.Wrap(err, "failed to convert ref to state")
					}
				}
				return nil, nil
			},
		}
		_, err := sess.Do(ctx, req)

		return result, err
	})
}
