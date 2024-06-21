package llblib

import (
	"context"
	"encoding/json"

	"braces.dev/errtrace"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
)

// FrontendOption can be used to modify a Frontend request.
type FrontendOption interface {
	SetFrontendOption(*frontendOptions)
}

type frontendOptionFunc func(*frontendOptions)

func (f frontendOptionFunc) SetFrontendOption(fo *frontendOptions) {
	f(fo)
}

// FrontendInput will attach the provided llb.State with the given name to
// the Frontend request.
func FrontendInput(name string, st llb.State) FrontendOption {
	return frontendOptionFunc(func(fo *frontendOptions) {
		fo.Inputs[name] = st
	})
}

// FrontendOpt will add the name/value pair to the Opts for the Frontend
// request.
func FrontendOpt(name, value string) FrontendOption {
	return frontendOptionFunc(func(fo *frontendOptions) {
		fo.Opts[name] = value
	})
}

// WithCustomName allows using the provided text for the progress display when
// solving the Frontend request.
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

// Frontend will create an llb.State that is created via a frontend Request.
// One common frontend is the `docker/dockerfile` frontend that is used
// by `docker buildx` commands.  The `source` argument is the image ref
// that is run as the frontend.  A Frontend request is the same as
// using the  `#syntax` directive in a Dockerfile. For example:
//
//	image := llblib.Frontend("docker/dockerfile",
//		llblib.FrontendInput("context", context),
//		llblib.FrontendInput("dockerfile", dockerfile),
//	)
func Frontend(source string, opts ...FrontendOption) llb.State {
	// "gateway.v0" is used to trampoline the solve to the source docker image
	// however "dockerfile.v0" is a built-in frontend, so we dont need to
	// trampoline and can directly use it.
	frontend := "gateway.v0"
	if source == "dockerfile.v0" {
		frontend = source
	}
	return llb.Scratch().Async(func(ctx context.Context, st llb.State, constraints *llb.Constraints) (llb.State, error) {
		fo := frontendOptions{
			Inputs: map[string]llb.State{},
			Opts:   map[string]string{},
		}
		if source != "dockerfile.v0" {
			fo.Opts["source"] = source
		}
		for _, opt := range opts {
			opt.SetFrontendOption(&fo)
		}

		sess := LoadSession(ctx)
		if sess == nil {
			return llb.Scratch(), errtrace.New("frontend solve request without active session")
		}

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
						return nil, errtrace.Wrap(err)
					}
					// only add inputs that are non-nil (ie dont add scratch)
					// because otherwise we will get a solve error:
					// cannot marshal empty definition op
					if def.Def != nil {
						inputs[name] = def.ToPB()
					}
				}
				req := gateway.SolveRequest{
					Frontend:       frontend,
					FrontendOpt:    fo.Opts,
					FrontendInputs: inputs,
				}
				res, err := c.Solve(ctx, req)
				if err != nil {
					return nil, errtrace.Errorf("failed to solve frontend request: %w", err)
				}

				ref, err := res.SingleRef()
				if err != nil {
					return nil, errtrace.Errorf("failed to extract ref from frontend request: %w", err)
				}
				if ref == nil {
					result = llb.Scratch()
				} else {
					result, err = ref.ToState()
					if err != nil {
						return nil, errtrace.Errorf("failed to convert ref to state: %w", err)
					}
					if config, ok := res.Metadata["containerimage.config"]; ok {
						result, err = result.WithImageConfig(config)
						if err != nil {
							return nil, errtrace.Errorf("failed to apply image config from frontend request: %w", err)
						}
						// we need to parse the document again bc WithImageConfig
						// does not apply the USER config.
						var img ImageConfig
						if err := json.Unmarshal(config, &img); err != nil {
							return nil, errtrace.Errorf("failed to parse config from frontend request: %w", err)
						}
						img.ContainerConfig = imageConfigToContainerConfig(img)

						result = WithImageConfig(result, &img)
						if img.Config.User != "" {
							result = result.User(img.Config.User)
						}
					}
				}
				return nil, nil
			},
		}
		_, err := sess.Do(ctx, req)

		return result, errtrace.Wrap(err)
	})
}
