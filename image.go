package llblib

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"

	"braces.dev/errtrace"
	"github.com/brunoga/deep"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/imagemetaresolver"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	mdispec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	imageConfigDescriptionKey = "llblib.imageconfig"
)

type imageConfigKey struct{}

type imageConfigState struct {
	config  *ImageConfig
	mutator func(context.Context, *ImageConfig) error
}

// ContainerConfig is the schema1-compatible configuration of the container
// that is committed into the image.
type ContainerConfig struct {
	Cmd    []string          `json:"Cmd"`
	Labels map[string]string `json:"Labels"`
}

// History wraps ocispec.History but allows us to track which session
// added the history entry so we can safely mutate existing records.
type History struct {
	ocispec.History
	// sessionID is the unique identifier for the session that created this
	// history entry, this value is not persisted to the config and is only
	// used to determine if we should append to an existing history record
	// or append a new one for some key/value history commits.
	sessionID string
}

// ImageConfig holds the configuration for an image.
type ImageConfig struct {
	mdispec.DockerOCIImage
	ContainerConfig ContainerConfig `json:"container_config,omitempty"`
	History         []History       `json:"history,omitempty"`
}

func imageConfigToContainerConfig(img ImageConfig) ContainerConfig {
	cmd := img.ContainerConfig.Cmd
	if cmd == nil {
		cmd = img.Config.Cmd
	}
	// for consistent formatting depending on the source of the image
	// we might see odd mis-parsed container_config.cmd values like:
	// [`/bin/sh`, `-c`, `#(nop) `, `CMD ["/bin/sh"]`]
	// so we will normalize this to the parsed `CMD` values (["/bin/sh"]
	// in this example).
	if len(cmd) > 3 && cmd[2] == `#(nop) ` {
		jsonCmd := strings.TrimPrefix(cmd[3], "CMD ")
		parsedCmd := []string{}
		// if we fail to parse the json, we will just use the original
		if err := json.Unmarshal([]byte(jsonCmd), &parsedCmd); err == nil {
			cmd = parsedCmd
		}
	}
	labels := img.ContainerConfig.Labels
	if labels == nil {
		labels = img.Config.Labels
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return ContainerConfig{
		Cmd:    cmd,
		Labels: labels,
	}
}

// LoadImageConfig will attempt to build the image config from values stored on the
// llb.State.
func LoadImageConfig(ctx context.Context, st llb.State) (*ImageConfig, error) {
	cs, err := loadImageConfigState(ctx, st)
	if err != nil {
		return nil, errtrace.Wrap(err)
	}
	// copy the config so we don't mutate it multiple times if LoadImageConfig
	// is called multiple times
	cfg, err := deep.Copy(cs.config)
	if err != nil {
		return nil, errtrace.Errorf("failed to copy image config: %w", err)
	}
	if cs.mutator != nil {
		if err := cs.mutator(ctx, cfg); err != nil {
			return nil, errtrace.Errorf("failed to apply image config mutator: %w", err)
		}
	}
	return cfg, nil
}

// loadImageConfigState will return the most recent imageConfigState stored
// on the llb.State.
func loadImageConfigState(ctx context.Context, st llb.State) (imageConfigState, error) {
	v, err := st.Value(ctx, imageConfigKey{})
	if err != nil {
		return imageConfigState{}, errtrace.Wrap(err)
	}
	if cs, ok := v.(imageConfigState); ok {
		return cs, nil
	} else if v != nil {
		return imageConfigState{}, errtrace.Errorf("unexpected type %T for image config", v)
	}

	// if we don't have an image config stored on the state, then it might be
	// stored in the metadata description which is only accessible by
	// marshalling the state.
	def, err := st.Marshal(ctx)
	if err != nil {
		return imageConfigState{}, errtrace.Wrap(err)
	}
	imageConfig, ok := def.Constraints.Metadata.Description[imageConfigDescriptionKey]
	if !ok {
		return imageConfigState{}, nil
	}
	cs := imageConfigState{
		config: &ImageConfig{},
	}
	if err := json.Unmarshal([]byte(imageConfig), cs.config); err != nil {
		return imageConfigState{}, errtrace.Errorf("failed to unmarshal image config: %w", err)
	}
	return cs, nil
}

// withImageConfig will store the image config on the llb.State.
func withImageConfig(st llb.State, config *ImageConfig) llb.State {
	return st.WithValue(imageConfigKey{}, imageConfigState{config: config})
}

// withImageConfigMutator will store the image config mutator on the llb.State.
// The mutator will be applied to the image config when imageConfig is called.
func withImageConfigMutator(st llb.State, m func(context.Context, *ImageConfig) error) llb.State {
	return st.Async(func(ctx context.Context, st llb.State, c *llb.Constraints) (llb.State, error) {
		cs, err := loadImageConfigState(ctx, st)
		if err != nil {
			return llb.State{}, errtrace.Wrap(err)
		}
		if cs.config == nil {
			var plat ocispec.Platform
			if c.Platform != nil {
				plat = *c.Platform
			}
			cs.config = &ImageConfig{
				DockerOCIImage: mdispec.DockerOCIImage{
					Image: ocispec.Image{
						Platform: plat,
					},
				},
			}
		}
		if cs.mutator == nil {
			cs.mutator = m
			return st.WithValue(imageConfigKey{}, cs), nil
		}
		// chain previous mutator to the one passed in
		prev := cs.mutator
		cs.mutator = func(ctx context.Context, c *ImageConfig) error {
			if err := prev(ctx, c); err != nil {
				return errtrace.Wrap(err)
			}
			return errtrace.Wrap(m(ctx, c))
		}
		return st.WithValue(imageConfigKey{}, cs), nil
	})
}

// ResolvedImage returns an llb.State where the image will be resolved with the
// image configuration applied to the state.  The resolved image digest will
// also be applied to the state to ensure this state is always consistent
// during the solve execution.
func ResolvedImage(ref string, opts ...llb.ImageOption) llb.State {
	return Image(ref, append(opts, llb.ResolveDigest(true))...)
}

// Image is similar to llb.Image but the image config will be preserved
// so that the llb.State can be pushed to a registry.
func Image(ref string, opts ...llb.ImageOption) llb.State {
	return llb.Scratch().Async(func(ctx context.Context, _ llb.State, c *llb.Constraints) (llb.State, error) {
		capturingMetaResolver := &capturingMetaResolver{
			resolver: LoadImageResolver(ctx),
		}

		img := llb.Image(ref,
			append(opts, llb.WithMetaResolver(capturingMetaResolver))...,
		)
		// this will un-async the image allowing us to capture the image config
		img.Output().Vertex(ctx, c)
		return img.Async(func(ctx context.Context, st llb.State, c *llb.Constraints) (llb.State, error) {
			if len(capturingMetaResolver.config) == 0 {
				return st, nil
			}
			var config ImageConfig
			if err := json.Unmarshal(capturingMetaResolver.config, &config); err != nil {
				return llb.State{}, errtrace.Errorf("failed to unmarshal image config: %w", err)
			}
			config.ContainerConfig = imageConfigToContainerConfig(config)
			return withImageConfig(st, &config), nil
		}), nil
	})
}

// imageResolverOption is a helper to extract the ImageMetaResolver if provided
// on the llb.ImageOptions.
func imageResolverOption(ctx context.Context, opts ...llb.ImageOption) sourceresolver.ImageMetaResolver {
	ii := llb.ImageInfo{}
	for _, opt := range opts {
		opt.SetImageOption(&ii)
	}
	// HACK: the metaResolver field is private, so we have to use reflection to
	// access it.  TODO maybe we can make this exported or add an accessor
	// to llb.ImageInfo?
	f := reflect.ValueOf(&ii).Elem().FieldByName("metaResolver")
	if !f.IsValid() {
		return LoadImageResolver(ctx)
	}
	if f.IsNil() || f.IsZero() {
		return LoadImageResolver(ctx)
	}
	// We won't be modifying the resolver, just using it.  Also the lifespan
	// of the resolver is longer than the places we call this function,
	// so this should be safe.
	f = reflect.NewAt(f.Type(), f.Addr().UnsafePointer()).Elem()
	return f.Interface().(sourceresolver.ImageMetaResolver)
}

type capturingMetaResolver struct {
	config   []byte
	resolver sourceresolver.ImageMetaResolver
}

func (s *capturingMetaResolver) ResolveImageConfig(ctx context.Context, ref string, opt sourceresolver.Opt) (string, digest.Digest, []byte, error) {
	if s.resolver == nil {
		s.resolver = imagemetaresolver.Default()
	}
	ref, dgst, config, err := s.resolver.ResolveImageConfig(ctx, ref, opt)
	if err != nil {
		return "", "", nil, err
	}
	s.config = config
	return ref, dgst, config, nil
}
