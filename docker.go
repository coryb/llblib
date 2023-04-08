package llblib

import (
	"context"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/solver/pb"
	specsv1 "github.com/opencontainers/image-spec/specs-go/v1"
)

// DockerfileOpts alias dockerfile2llb.ConvertOpt
type DockerfileOpts = dockerfile2llb.ConvertOpt

// DockerfileOption can be used to modify a Dockerfile request.
type DockerfileOption interface {
	SetDockerfileOption(*DockerfileOpts)
}

type dockerfileOptionFunc func(*DockerfileOpts)

func (f dockerfileOptionFunc) SetDockerfileOption(o *DockerfileOpts) {
	f(o)
}

// WithTarget will set the target for the Dockerfile build.
func WithTarget(t string) DockerfileOption {
	return dockerfileOptionFunc(func(o *DockerfileOpts) {
		o.Target = t
	})
}

// WithBuildArg can be used to set build args for the Dockerfile build.
func WithBuildArg(k, v string) DockerfileOption {
	return dockerfileOptionFunc(func(o *DockerfileOpts) {
		if o.BuildArgs == nil {
			o.BuildArgs = map[string]string{
				k: v,
			}
			return
		}
		o.BuildArgs[k] = v
	})
}

// WithTargetPlatform will set the platform for the Dockerfile build.
func WithTargetPlatform(p *specsv1.Platform) DockerfileOption {
	return dockerfileOptionFunc(func(o *DockerfileOpts) {
		o.TargetPlatform = p
	})
}

// Dockerfile will parse the provided dockerfile and construct an llb.State
// represented by the provided dockerfile instructions.
func Dockerfile(dockerfile []byte, buildContext llb.State, opts ...DockerfileOption) llb.State {
	return llb.Scratch().Async(func(ctx context.Context, _ llb.State, c *llb.Constraints) (llb.State, error) {
		caps := pb.Caps.CapSet(pb.Caps.All())
		docOpts := DockerfileOpts{
			LLBCaps:      &caps,
			MetaResolver: LoadImageResolver(ctx),
			BuildContext: &buildContext,
		}
		for _, opt := range opts {
			opt.SetDockerfileOption(&docOpts)
		}
		state, _, _, err := dockerfile2llb.Dockerfile2LLB(ctx, dockerfile, docOpts)
		if err != nil {
			return llb.Scratch(), err
		}
		return *state, nil
	})
}
