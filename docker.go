package llblib

import (
	"context"
	"path"

	"braces.dev/errtrace"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/solver/pb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// DockerfileOpts alias dockerfile2llb.ConvertOpt
type DockerfileOpts = dockerfile2llb.ConvertOpt

type dockerfileOpts struct {
	DockerfileOpts
	buildContexts map[string]llb.State
	frontendOpts  map[string]string
}

// DockerfileOption can be used to modify a Dockerfile request.
type DockerfileOption interface {
	SetDockerfileOption(*dockerfileOpts)
}

type dockerfileOptionFunc func(*dockerfileOpts)

func (f dockerfileOptionFunc) SetDockerfileOption(o *dockerfileOpts) {
	f(o)
}

// WithTarget will set the target for the Dockerfile build.
func WithTarget(t string) DockerfileOption {
	return dockerfileOptionFunc(func(o *dockerfileOpts) {
		o.Target = t
	})
}

// WithBuildArg can be used to set build args for the Dockerfile build.
func WithBuildArg(k, v string) DockerfileOption {
	return dockerfileOptionFunc(func(o *dockerfileOpts) {
		o.BuildArgs[k] = v
	})
}

// WithBuildArgs can be used to set build args for the Dockerfile build.
func WithBuildArgs(args map[string]string) DockerfileOption {
	return dockerfileOptionFunc(func(o *dockerfileOpts) {
		for k, v := range args {
			o.BuildArgs[k] = v
		}
	})
}

// WithTargetPlatform will set the platform for the Dockerfile build.
func WithTargetPlatform(p ocispec.Platform) DockerfileOption {
	return dockerfileOptionFunc(func(o *dockerfileOpts) {
		o.TargetPlatform = &p
	})
}

// WithBuildContext will set an additional build context for the Dockerfile
// build.
func WithBuildContext(name string, st llb.State) DockerfileOption {
	return dockerfileOptionFunc(func(o *dockerfileOpts) {
		o.buildContexts[name] = st
	})
}

// WithRemoteBuildContext will set an additional build context for the Dockerfile
// build from a remote source.  Examples:
//
//   - git repo:
//     llblib.WithRemoveBuildContext("my-repo", "https://github.com/myorg/my-repo.git")
//
//   - url:
//     llblib.WithRemoveBuildContext("my-url", "https://example.com/my-url/README.md")
//
//   - docker image:
//     llblib.WithRemoveBuildContext("my-image", "docker-image://myorg/my-image:latest")
//
//   - target from the Dockerfile being built:
//     llblib.WithRemoveBuildContext("my-target", "target:my-target")
func WithRemoteBuildContext(name string, src string) DockerfileOption {
	return dockerfileOptionFunc(func(o *dockerfileOpts) {
		o.frontendOpts["context:"+name] = src
	})
}

// WithDockerfileName will set the name of the Dockerfile to use for the build.
// This is to make the build output consistent with a user provided filename.
func WithDockerfileName(name string) DockerfileOption {
	return dockerfileOptionFunc(func(o *dockerfileOpts) {
		if name == defaultDockerfileName {
			return
		}
		o.frontendOpts["filename"] = name
	})
}

// Dockerfile will parse the provided dockerfile and construct an llb.State
// represented by the provided dockerfile instructions.
func Dockerfile(dockerfile []byte, buildContext llb.State, opts ...DockerfileOption) llb.State {
	return llb.Scratch().Async(func(ctx context.Context, _ llb.State, c *llb.Constraints) (llb.State, error) {
		caps := pb.Caps.CapSet(pb.Caps.All())
		docOpts := dockerfileOpts{
			DockerfileOpts: DockerfileOpts{
				LLBCaps:      &caps,
				MetaResolver: LoadImageResolver(ctx),
				MainContext:  &buildContext,
				Config: dockerui.Config{
					BuildArgs: map[string]string{},
				},
			},

			buildContexts: map[string]llb.State{},
			frontendOpts:  map[string]string{},
		}
		for _, opt := range opts {
			opt.SetDockerfileOption(&docOpts)
		}
		if source, _, _, ok := parser.DetectSyntax(dockerfile); ok {
			return errtrace.Wrap2(frontendDockerfileSolve(source, dockerfile, docOpts))
		}
		if len(docOpts.buildContexts) > 0 || len(docOpts.frontendOpts) > 0 {
			// we cannot use the direct solve if we have additional inputs
			return errtrace.Wrap2(frontendDockerfileSolve("dockerfile.v0", dockerfile, docOpts))
		}
		return errtrace.Wrap2(directSolve(ctx, dockerfile, docOpts.DockerfileOpts))
	})
}

func directSolve(ctx context.Context, dockerfile []byte, opts DockerfileOpts) (llb.State, error) {
	state, img, _, _, err := dockerfile2llb.Dockerfile2LLB(ctx, dockerfile, opts)
	if err != nil {
		return llb.Scratch(), errtrace.Wrap(err)
	}
	var history []History
	for _, h := range img.History {
		history = append(history, History{History: h})
	}
	imageConfig := ImageConfig{
		DockerOCIImage: *img,
		ContainerConfig: ContainerConfig{
			Cmd:    img.Config.Cmd,
			Labels: img.Config.Labels,
		},
		History: history,
	}
	return withImageConfig(*state, &imageConfig), nil
}

const (
	// copying private const variables from:
	// github.com/moby/buildkit/frontend/dockerfile/builder
	keyTarget             = "target"
	keyTargetPlatform     = "platform"
	buildArgPrefix        = "build-arg:"
	defaultDockerfileName = "Dockerfile"
)

func frontendDockerfileSolve(
	source string,
	dockerfile []byte,
	opts dockerfileOpts,
) (llb.State, error) {
	filename := defaultDockerfileName
	if name, ok := opts.frontendOpts["filename"]; ok {
		filename = name
	}

	var fileAction *llb.FileAction
	dir := path.Clean(path.Dir(filename))
	if dir != "." {
		fileAction = llb.Mkdir(dir, 0o755, llb.WithParents(true)).Mkfile(filename, 0o644, dockerfile)
	} else {
		fileAction = llb.Mkfile(filename, 0o644, dockerfile)
	}

	feOpts := []FrontendOption{
		FrontendInput(dockerui.DefaultLocalNameDockerfile, llb.Scratch().File(fileAction, llb.WithCustomName("load build definition from "+filename))),
	}
	if opts.MainContext != nil {
		feOpts = append(feOpts, FrontendInput(dockerui.DefaultLocalNameContext, *opts.MainContext))
	}

	for name, value := range opts.frontendOpts {
		feOpts = append(feOpts, FrontendOpt(name, value))
	}

	for name, state := range opts.buildContexts {
		feOpts = append(feOpts,
			FrontendOpt("context:"+name, "input:"+name),
			FrontendInput(name, state),
		)
	}

	if opts.TargetPlatform != nil {
		feOpts = append(feOpts, FrontendOpt(keyTargetPlatform,
			path.Join(
				opts.TargetPlatform.OS,
				opts.TargetPlatform.Architecture,
				opts.TargetPlatform.Variant,
			),
		))
	}

	if opts.Target != "" {
		feOpts = append(feOpts, FrontendOpt(keyTarget, opts.Target))
	}

	for k, v := range opts.BuildArgs {
		feOpts = append(feOpts, FrontendOpt(buildArgPrefix+k, v))
	}

	return Frontend(source, feOpts...), nil
}
