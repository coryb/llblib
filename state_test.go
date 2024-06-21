package llblib_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	v1 "github.com/moby/docker-image-spec/specs-go/v1"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const (
	alpine = "alpine@sha256:216266c86fc4dcef5619930bd394245824c2af52fd21ba7c6fa0e618657d4c3b"
)

func expectConfig(t *testing.T, cfgData string) {
	t.Helper()
	// serialize the cfg to yaml for easier test comparison
	config := map[string]any{}
	err := json.Unmarshal([]byte(cfgData), &config)
	require.NoError(t, err)
	got, err := yaml.Marshal(config)
	require.NoError(t, err)

	shortName := filepath.Base(t.Name())
	file := filepath.Join("test-data", "configs", shortName+".yaml")
	expected, err := os.ReadFile(file)
	require.NoError(t, err, "reading file: %s", file)
	require.Equal(t, string(expected), string(got))
}

func TestImageConfigMods(t *testing.T) {
	t.Parallel()
	def, err := llblib.MarshalWithImageConfig(
		context.Background(),
		llblib.Image(alpine, llb.LinuxAmd64),
	)
	require.NoError(t, err)
	for _, tt := range []struct {
		name string
		base llb.State
	}{{
		name: "image",
		base: llblib.Image(
			alpine,
			llb.LinuxAmd64,
		),
	}, {
		name: "dockerfile",
		base: llblib.Dockerfile(
			[]byte(`FROM `+alpine),
			llb.Scratch(),
			llblib.WithTargetPlatform(ocispec.Platform{OS: "linux", Architecture: "amd64"}),
		),
	}, {
		name: "frontend",
		base: llblib.Frontend("docker/dockerfile",
			llblib.FrontendInput("context", llb.Scratch()),
			llblib.FrontendInput("dockerfile", llb.Scratch().File(
				llb.Mkfile("Dockerfile",
					0o644,
					[]byte(`FROM --platform=linux/amd64 `+alpine),
				),
			)),
		),
	}, {
		name: "def",
		base: llblib.BuildDefinition(def),
	}} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			testImageConfigMods(t, tt.base)
		})
	}
}

func testImageConfigMods(t *testing.T, base llb.State) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		state    func(base llb.State) llb.State
		skipMoby bool
	}{{
		name: "merge",
		state: func(base llb.State) llb.State {
			a := base.File(llb.Mkfile("/file1", 0o644, []byte("file1")))
			b := llb.Scratch().File(llb.Mkfile("/file2", 0o644, []byte("file2")))
			return llblib.Merge([]llb.State{a, b})
		},
		skipMoby: true,
	}, {
		name: "diff",
		state: func(base llb.State) llb.State {
			added := base.File(llb.Mkfile("/file1", 0o644, []byte("file1")))
			return llblib.Diff(base, added, llb.LinuxAmd64)
		},
		skipMoby: true,
	}, {
		name: "run",
		state: func(base llb.State) llb.State {
			return llblib.Run(base, llb.Shlex("echo hello")).Root()
		},
	}, {
		name: "applyRun",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.ApplyRun(llb.Shlex("echo hello")),
			)
		},
	}, {
		name: "copy",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.Copy(
					llb.Scratch().File(llb.Mkfile("/file1", 0o644, []byte("file1"))),
					"/file1",
					"/copied",
				),
			)
		},
	}, {
		name: "defaultDir",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.DefaultDir("/tmp"),
			)
		},
	}, {
		name: "defaultUser",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.DefaultUser("nobody"),
			)
		},
	}, {
		name: "defaultEnv",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.AddDefaultEnv("A", "one"),
				llblib.AddDefaultEnv("B", "two"),
			)
		},
	}, {
		name: "entrypoint",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.Entrypoint("/bin/true"),
			)
		},
	}, {
		name: "cmd",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.Cmd("/bin/true"),
			)
		},
	}, {
		name: "label",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.AddLabel("A", "one"),
				llblib.AddLabel("B", "two"),
			)
		},
	}, {
		name: "exposePort",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.AddExposedPort("80/tcp"),
			)
		},
	}, {
		name: "addVolume",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.AddVolume("/some/cache"),
			)
		},
	}, {
		name: "stopsignal",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.StopSignal("SIGUSR1"),
			)
		},
	}, {
		name: "healthcheck",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.DockerHealthcheck(v1.HealthcheckConfig{
					Test:        []string{"CMD", "/bin/true"},
					Interval:    10 * time.Minute,
					Timeout:     5 * time.Second,
					StartPeriod: time.Minute,
					Retries:     3,
				}),
			)
		},
	}, {
		name: "onbuild",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.AddDockerOnBuild("RUN echo hello"),
			)
		},
	}, {
		name: "runshell",
		state: func(base llb.State) llb.State {
			return base.With(
				llblib.DockerRunShell("/bin/bash -c"),
			)
		},
	}} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := newTestRunner(t, withTimeout(60*time.Second))
			if tt.skipMoby && r.isMoby {
				t.Skip("skipping test because moby doesn't support the operation")
			}
			req := r.Solver.Build(tt.state(base))
			resp, err := r.Run(t, req)
			require.NoError(t, err)
			require.NotEmpty(t, resp.ExporterResponse[llblib.ExporterImageConfigKey])
			expectConfig(t, resp.ExporterResponse[llblib.ExporterImageConfigKey])
		})
	}
}
