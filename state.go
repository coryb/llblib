package llblib

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"braces.dev/errtrace"
	"github.com/kballard/go-shellquote"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/llbbuild"
	"github.com/moby/buildkit/solver/pb"
	mdispec "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/exp/maps"
)

// Digest returns the digest for the state.
func Digest(st llb.State) (digest.Digest, error) {
	ctx := context.Background()
	c := &llb.Constraints{}
	dgst, _, _, _, err := st.Output().Vertex(ctx, c).Marshal(ctx, c)
	return dgst, errtrace.Wrap(err)
}

// MarshalWithImageConfig marshals the state to a definition ensuring the
// image config state is preserved in the definition.
func MarshalWithImageConfig(ctx context.Context, st llb.State) (*llb.Definition, error) {
	config, err := LoadImageConfig(ctx, st)
	if err != nil {
		return nil, err
	}
	rawConfig, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}

	return st.Marshal(ctx, llb.WithDescription(map[string]string{
		imageConfigDescriptionKey: string(rawConfig),
	}))
}

// BuildDefinition builds a definition and returns a state.
func BuildDefinition(def *llb.Definition) llb.State {
	buf := bytes.Buffer{}
	llb.WriteTo(def, &buf)

	st := llb.Scratch().File(
		llb.Mkfile(pb.LLBDefinitionInput, 0o644, buf.Bytes()),
	).With(llbbuild.Build())

	if def.Constraints != nil {
		if config, ok := def.Constraints.Metadata.Description[imageConfigDescriptionKey]; ok {
			imageConfig := &ImageConfig{}
			if err := json.Unmarshal([]byte(config), imageConfig); err != nil {
				panic(err)
			}
			return withImageConfig(st, imageConfig)
		}
	}
	return st
}

type layerHistory struct {
	empty     bool
	desc      string
	sessionID string
}

func commitHistory(img *ImageConfig, commit layerHistory) {
	img.History = append(img.History, History{
		History: ocispec.History{
			// Set a zero value on Created for more reproducible builds
			Created:    &time.Time{},
			CreatedBy:  commit.desc,
			Comment:    "llblib.v0",
			EmptyLayer: commit.empty,
		},
		sessionID: commit.sessionID,
	})
	// Set a zero value on Created for more reproducible builds
	img.Created = &time.Time{}
}

// commitHistoryKV will add key/value instructions to the image history, if
// the most recent history record is for the same instruction and created within
// the current session then we will append the key/value to the existing record.
func commitHistoryKV(ctx context.Context, img *ImageConfig, instruction, key, value string) {
	// In Dockerfile for some instructions (ENV, LABEL) multiple values can be
	// specified in one instruction leading to one history element.  This checks
	// if the previous history committed was also the same instruction, in which
	// case it should just add to the previous history element.
	sessID := sessionID(ctx)
	numHistory := len(img.History)
	if numHistory > 0 && strings.HasPrefix(img.History[numHistory-1].CreatedBy, instruction) && img.History[numHistory-1].sessionID == sessID {
		img.History[numHistory-1].CreatedBy += " " + shellquote.Join(key) + "=" + shellquote.Join(value)
	} else {
		commitHistory(img, layerHistory{
			empty:     true,
			desc:      instruction + " " + shellquote.Join(key) + "=" + shellquote.Join(value),
			sessionID: sessID,
		})
	}
}

// Merge is similar to llb.Merge but also commits history to the image config.
func Merge(states []llb.State, opts ...llb.ConstraintsOpt) llb.State {
	switch len(states) {
	case 0:
		return llb.Scratch()
	case 1:
		return states[0]
	}
	return llb.Merge(states, opts...).Async(func(ctx context.Context, st llb.State, c *llb.Constraints) (llb.State, error) {
		// if any of the merged states has an image config, then preserve
		// the first one we see.
		for _, ms := range states {
			cs, err := loadImageConfigState(ctx, ms)
			if err != nil {
				continue
			}
			if cs.config == nil {
				continue
			}
			st = st.WithValue(imageConfigKey{}, cs)
			break
		}

		msgs := []string{}
		for _, ms := range states {
			dgst, _, _, _, err := ms.Output().Vertex(ctx, c).Marshal(ctx, c)
			if err != nil {
				return llb.State{}, errtrace.Wrap(err)
			}
			msgs = append(msgs, dgst.Encoded()+":/")
		}

		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			commitHistory(img, layerHistory{
				empty: false,
				desc:  "MERGE " + strings.Join(msgs, " "),
			})
			return nil
		}), nil
	})
}

// Diff is similar to llb.Diff but also commits history to the image config.
func Diff(lower, upper llb.State, opts ...llb.ConstraintsOpt) llb.State {
	return llb.Diff(lower, upper, opts...).Async(func(ctx context.Context, st llb.State, c *llb.Constraints) (llb.State, error) {
		lowerDgst, _, _, _, err := lower.Output().Vertex(ctx, c).Marshal(ctx, c)
		if err != nil {
			return llb.State{}, errtrace.Wrap(err)
		}
		upperDgst, _, _, _, err := upper.Output().Vertex(ctx, c).Marshal(ctx, c)
		if err != nil {
			return llb.State{}, errtrace.Wrap(err)
		}
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			img.Author = ""
			img.RootFS = ocispec.RootFS{}
			img.Config = mdispec.DockerOCIImageConfig{
				ImageConfig: ocispec.ImageConfig{
					Labels: img.Config.Labels,
				},
			}
			img.ContainerConfig = ContainerConfig{
				Labels: img.ContainerConfig.Labels,
			}

			commitHistory(img, layerHistory{
				empty: false,
				desc:  "DIFF " + lowerDgst.Encoded() + ":/ " + upperDgst.Encoded() + ":/",
			})
			return nil
		}), nil
	})
}

// Run is similar to llb.Run but also commits history to the image config.
func Run(st llb.State, opts ...llb.RunOption) llb.ExecState {
	ei := &llb.ExecInfo{State: st}
	for _, opt := range opts {
		opt.SetRunOption(ei)
	}
	es := st.Run(opts...)
	es.State = es.State.Async(func(ctx context.Context, st llb.State, c *llb.Constraints) (llb.State, error) {
		args, err := ei.State.GetArgs(ctx)
		if err != nil {
			return llb.State{}, fmt.Errorf("failed to get args from state for history: %w", err)
		}
		return withImageConfigMutator(st, func(ctx context.Context, img *ImageConfig) error {
			commitHistory(img, layerHistory{
				empty: false,
				desc:  "RUN " + shellquote.Join(args...),
			})
			return nil
		}), nil
	})
	return es
}

// ApplyRun is similar to llb.Run but also commits history to the image config.
// It will always resolve the ExecState.Root from llb.Run, and can be used like:
//
//	st = st.With(
//		llblib.ApplyRun(llb.Shlex("touch /file1")),
//		llblib.ApplyRun(llb.Shlex("touch /file2")),
//	)
func ApplyRun(opts ...llb.RunOption) llb.StateOption {
	return func(st llb.State) llb.State {
		return Run(st, opts...).Root()
	}
}

// Copy will copy files from one state to another, and also commits history to
// the image config.
func Copy(src llb.State, srcPath, destPath string, opts ...llb.CopyOption) llb.StateOption {
	return func(dest llb.State) llb.State {
		st := dest.File(
			llb.Copy(src, srcPath, destPath, opts...),
		)
		return st.Async(func(ctx context.Context, st llb.State, c *llb.Constraints) (llb.State, error) {
			dgst, _, _, _, err := src.Output().Vertex(ctx, c).Marshal(ctx, c)
			if err != nil {
				return llb.State{}, errtrace.Wrap(err)
			}
			return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
				commitHistory(img, layerHistory{
					empty: false,
					desc:  shellquote.Join("COPY", dgst.Encoded()+":"+srcPath, destPath),
				})
				return nil
			}), nil
		})
	}
}

// DefaultDir sets the WORKDIR working directory for the image, also records the
// WorkingDir to the image config.
func DefaultDir(d string) llb.StateOption {
	return func(st llb.State) llb.State {
		st = st.Dir(d)
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			img.Config.WorkingDir = d
			commitHistory(img, layerHistory{
				empty: true,
				desc:  shellquote.Join("WORKDIR", d),
			})
			return nil
		})
	}
}

// DefaultUser sets the USER for the image, also records the User to the image
// config.
func DefaultUser(u string) llb.StateOption {
	return func(st llb.State) llb.State {
		st = st.User(u)
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			img.Config.User = u
			commitHistory(img, layerHistory{
				empty: true,
				desc:  shellquote.Join("USER", u),
			})
			return nil
		})
	}
}

// AddDefaultEnv sets an ENV environment variable for the image, also records
// the environment variable to the image config.
func AddDefaultEnv(key, value string) llb.StateOption {
	return func(st llb.State) llb.State {
		st = st.AddEnv(key, value)
		return withImageConfigMutator(st, func(ctx context.Context, img *ImageConfig) error {
			img.Config.Env = append(img.Config.Env, key+"="+value)
			commitHistoryKV(ctx, img, "ENV", key, value)
			return nil
		})
	}
}

// Entrypoint records the ENTRYPOINT to image config.
func Entrypoint(entrypoint ...string) llb.StateOption {
	return func(st llb.State) llb.State {
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			img.Config.Entrypoint = entrypoint
			out, err := json.Marshal(entrypoint)
			if err != nil {
				return fmt.Errorf("failed to marshal ENTRYPOINT as json: %w", err)
			}
			commitHistory(img, layerHistory{
				empty: true,
				desc:  "ENTRYPOINT " + string(out),
			})
			return nil
		})
	}
}

// Cmd records the CMD command arguments to the image config.
func Cmd(cmd ...string) llb.StateOption {
	return func(st llb.State) llb.State {
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			img.Config.Cmd = cmd
			img.ContainerConfig.Cmd = cmd // legacy
			out, err := json.Marshal(cmd)
			if err != nil {
				return fmt.Errorf("failed to marshal CMD as json: %w", err)
			}
			commitHistory(img, layerHistory{
				empty: true,
				desc:  "CMD " + string(out),
			})
			return nil
		})
	}
}

// AddLabel records a LABEL to the image config.
func AddLabel(key, value string) llb.StateOption {
	return func(st llb.State) llb.State {
		return withImageConfigMutator(st, func(ctx context.Context, img *ImageConfig) error {
			if img.Config.Labels == nil {
				img.Config.Labels = make(map[string]string)
			}
			if img.ContainerConfig.Labels == nil {
				img.ContainerConfig.Labels = make(map[string]string)
			}

			if curVal, ok := img.Config.Labels[key]; ok && curVal == value {
				// No need to add the label if it already exists with the same value
				return nil
			}

			img.Config.Labels[key] = value
			img.ContainerConfig.Labels[key] = value // legacy
			commitHistoryKV(ctx, img, "LABEL", key, value)
			return nil
		})
	}
}

// AddLabels records a LABEL to the image config.
func AddLabels(labels map[string]string) llb.StateOption {
	return func(st llb.State) llb.State {
		keys := maps.Keys(labels)
		sort.Strings(keys)
		for _, key := range keys {
			st = AddLabel(key, labels[key])(st)
		}
		return st
	}
}

// AddExposedPort records an EXPOSE port to the image config.
func AddExposedPort(port string) llb.StateOption {
	return func(st llb.State) llb.State {
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			if img.Config.ExposedPorts == nil {
				img.Config.ExposedPorts = make(map[string]struct{})
			}
			if _, ok := img.Config.ExposedPorts[port]; ok {
				// No need to add the port if it already exists
				return nil
			}
			img.Config.ExposedPorts[port] = struct{}{}
			commitHistory(img, layerHistory{
				empty: true,
				desc:  "EXPOSE " + port,
			})
			return nil
		})
	}
}

// AddVolume records a VOLUME to the image config.
func AddVolume(mountpoint string) llb.StateOption {
	return func(st llb.State) llb.State {
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			if img.Config.Volumes == nil {
				img.Config.Volumes = make(map[string]struct{})
			}
			if _, ok := img.Config.Volumes[mountpoint]; ok {
				// No need to add the volume if it already exists
				return nil
			}
			img.Config.Volumes[mountpoint] = struct{}{}
			commitHistory(img, layerHistory{
				empty: true,
				desc:  "VOLUME " + mountpoint,
			})
			return nil
		})
	}
}

// StopSignal records the STOPSIGNAL to the image config.
func StopSignal(signal string) llb.StateOption {
	return func(st llb.State) llb.State {
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			img.Config.StopSignal = signal
			commitHistory(img, layerHistory{
				empty: true,
				desc:  "STOPSIGNAL " + signal,
			})
			return nil
		})
	}
}

// DockerHealthcheck records the HEALTHCHECK configuration to the image config.
func DockerHealthcheck(hc mdispec.HealthcheckConfig) llb.StateOption {
	return func(st llb.State) llb.State {
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			img.Config.Healthcheck = &hc
			out, err := json.Marshal(hc)
			if err != nil {
				return fmt.Errorf("failed to marshal HEALTHCHECK as json: %w", err)
			}
			commitHistory(img, layerHistory{
				empty: true,
				desc:  "HEALTHCHECK " + string(out),
			})
			return nil
		})
	}
}

// AddDockerOnBuild records the ONBUILD instruction to the image config.
func AddDockerOnBuild(instruction string) llb.StateOption {
	return func(st llb.State) llb.State {
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			img.Config.OnBuild = append(img.Config.OnBuild, instruction)
			commitHistory(img, layerHistory{
				empty: true,
				desc:  "ONBUILD " + instruction,
			})
			return nil
		})
	}
}

// DockerRunShell sets the SHELL for the image config.
func DockerRunShell(shell ...string) llb.StateOption {
	return func(st llb.State) llb.State {
		return withImageConfigMutator(st, func(_ context.Context, img *ImageConfig) error {
			img.Config.Shell = shell
			out, err := json.Marshal(shell)
			if err != nil {
				return fmt.Errorf("failed to marshal SHELL as json: %w", err)
			}
			commitHistory(img, layerHistory{
				empty: true,
				desc:  "SHELL " + string(out),
			})
			return nil
		})
	}
}
