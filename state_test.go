package llblib_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	v1 "github.com/moby/docker-image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func alpine(r testRunner) llb.State {
	return llblib.Image(
		"alpine@sha256:216266c86fc4dcef5619930bd394245824c2af52fd21ba7c6fa0e618657d4c3b",
		llb.LinuxAmd64,
		llb.WithMetaResolver(r.Solver.ImageResolver(r.Client, r.Progress)),
	)
}

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
	for _, tt := range []struct {
		name     string
		state    func(testRunner) llb.State
		skipMoby bool
	}{{
		name: "merge",
		state: func(r testRunner) llb.State {
			a := alpine(r).File(llb.Mkfile("/file1", 0o644, []byte("file1")))
			b := llb.Scratch().File(llb.Mkfile("/file2", 0o644, []byte("file2")))
			return llblib.Merge([]llb.State{a, b})
		},
		skipMoby: true,
	}, {
		name: "diff",
		state: func(r testRunner) llb.State {
			alpine := alpine(r)
			added := alpine.File(llb.Mkfile("/file1", 0o644, []byte("file1")))
			return llblib.Diff(alpine, added, llb.LinuxAmd64)
		},
		skipMoby: true,
	}, {
		name: "run",
		state: func(r testRunner) llb.State {
			return llblib.Run(alpine(r), llb.Shlex("echo hello")).Root()
		},
	}, {
		name: "copy",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.Copy(
					llb.Scratch().File(llb.Mkfile("/file1", 0o644, []byte("file1"))),
					"/file1",
					"/copied",
				),
			)
		},
	}, {
		name: "defaultDir",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.DefaultDir("/tmp"),
			)
		},
	}, {
		name: "defaultUser",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.DefaultUser("nobody"),
			)
		},
	}, {
		name: "defaultEnv",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.AddDefaultEnv("A", "one"),
				llblib.AddDefaultEnv("B", "two"),
			)
		},
	}, {
		name: "entrypoint",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.Entrypoint("/bin/true"),
			)
		},
	}, {
		name: "cmd",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.Cmd("/bin/true"),
			)
		},
	}, {
		name: "label",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.AddLabel("A", "one"),
				llblib.AddLabel("B", "two"),
			)
		},
	}, {
		name: "exposePort",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.AddExposedPort("80/tcp"),
			)
		},
	}, {
		name: "addVolume",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.AddVolume("/some/cache"),
			)
		},
	}, {
		name: "stopsignal",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.StopSignal("SIGUSR1"),
			)
		},
	}, {
		name: "healthcheck",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
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
		state: func(r testRunner) llb.State {
			return alpine(r).With(
				llblib.AddDockerOnBuild("RUN echo hello"),
			)
		},
	}, {
		name: "runshell",
		state: func(r testRunner) llb.State {
			return alpine(r).With(
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
			req := r.Solver.Build(tt.state(r))
			resp, err := r.Run(t, req)
			require.NoError(t, err)
			require.NotEmpty(t, resp.ExporterResponse[llblib.ExporterImageConfigKey])
			expectConfig(t, resp.ExporterResponse[llblib.ExporterImageConfigKey])
		})
	}
}
