package llblib_test

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/coryb/walky"
	"github.com/moby/buildkit/client/llb"
	specsv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestYAML(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, withTimeout(10*time.Second))

	states := func(s ...llb.State) []llb.State {
		return s
	}

	for _, tt := range []struct {
		states   []llb.State
		expected string
	}{{
		states:   states(llb.Scratch()),
		expected: "scratch",
	}, {
		states:   states(r.Solver.Local(".")),
		expected: "local",
	}, {
		states:   states(llb.Image("golang:1.20.1", llb.LinuxAmd64)),
		expected: "image",
	}, {
		states: states(
			llb.Diff(
				llb.Image("golang:1.20.1", llb.LinuxAmd64),
				llb.Image("golang:1.20.1", llb.LinuxAmd64).File(
					llb.Mkdir("/foobar", 0o755),
				).File(
					llb.Mkfile("/foobar/file", 0o644, []byte("contents")),
				),
			),
		),
		expected: "diff",
	}, {
		states: states(
			llb.Merge([]llb.State{
				llb.Image("golang:1.20.1", llb.LinuxAmd64),
				llb.Scratch().File(
					llb.Mkdir("/foobar", 0o755),
				).File(
					llb.Mkfile("/foobar/file", 0o644, []byte("contents")),
				),
			}),
		),
		expected: "merge",
	}, {
		states: states(
			llb.Image("golang:1.20.1", llb.LinuxAmd64).Run(
				llb.Args([]string{"/bin/true"}),
			).Root(),
		),
		expected: "run",
	}, {
		states: states(
			llb.Image("golang:1.20.1", llb.LinuxAmd64).Run(
				llb.Args([]string{"/bin/true"}),
				llb.WithCustomName("good build"),
			).Root(),
			llb.Image("golang:1.20.1", llb.LinuxAmd64).Run(
				llb.Args([]string{"/bin/false"}),
				llb.WithCustomName("bad build"),
			).Root(),
		),
		expected: "runs",
	}, {
		states: states(
			llb.Image("golang:1.20.1", llb.LinuxAmd64).Run(
				llb.Args([]string{"/bin/true"}),
				llb.Security(llb.SecurityModeInsecure),
				llb.AddEnv("FOO", "BAR"),
				llb.AddExtraHost("home", net.IPv4(127, 0, 0, 1)),
				llb.AddMount("/scratch", llb.Scratch()),
				llb.AddMount("/git",
					llb.Git("https://github.com/moby/buildkit.git", "baaf67ba976460a51ef198abab88baae376c32d8",
						llb.KeepGitDir(),
					),
					llb.Readonly,
				),
			).Root(),
		),
		expected: "mounts",
	}, {
		states: func() []llb.State {
			mp := llblib.Persistent(
				llb.Image("golang:1.20.1", llb.LinuxAmd64),
				llb.AddMount("/scratch", llb.Scratch()),
				llb.AddMount("/git",
					llb.Git("https://github.com/moby/buildkit.git", "baaf67ba976460a51ef198abab88baae376c32d8",
						llb.KeepGitDir(),
					),
					llb.Readonly,
				),
				llb.AddMount("/src", r.Solver.Local(".",
					llb.IncludePatterns([]string{".golangci.yaml"}),
				)),
			)
			mp.Run(
				llb.Args([]string{"/bin/true"}),
				llb.AddEnv("FOO", "BAR"),
				llb.AddMount("/cache", llb.Scratch(),
					llb.AsPersistentCacheDir("myid", llb.CacheMountPrivate),
				),
				llb.AddMount("/tmpfs", llb.Scratch(), llb.Tmpfs()),
			)
			mp.Run(
				llb.Args([]string{"/bin/false"}),
				llb.AddEnv("FOO", "BAZ"),
				llb.AddMount("/cache", llb.Scratch(),
					llb.AsPersistentCacheDir("myid", llb.CacheMountPrivate),
				),
				llb.AddMount("/tmpfs", llb.Scratch(), llb.Tmpfs()),
			)
			scratch, _ := mp.GetMount("/scratch")
			src, _ := mp.GetMount("/src")
			return states(scratch, src)
		}(),
		expected: "propagated",
	}, {
		states: states(llblib.Dockerfile(
			[]byte(`
				FROM busybox AS start
				RUN echo start > start
				FROM busybox AS hi
				RUN echo hi > hi
				FROM scratch AS download
				COPY --from=start start start
				COPY --from=hi hi hi
				FROM busybox
				RUN false # <- should not run
			`),
			llb.Scratch(),
			llblib.WithTarget("download"),
			llblib.WithTargetPlatform(&specsv1.Platform{
				OS: "linux", Architecture: "arm64",
			}),
		)),
		expected: "dockerfile",
	}, {
		states: states(
			llb.Image("busybox", llb.LinuxAmd64).Run(
				llb.Shlex("cat /secret"),
				slv.AddSecretFile("yaml_test.go", "/secret"),
			).Root(),
		),
		expected: "secrets",
	}, {
		states: states(
			llb.Image("busybox", llb.LinuxAmd64).Run(
				llb.Args([]string{"/bin/sh", "-c", "echo multi\necho line\necho statement"}),
			).Root(),
		),
		expected: "script",
	}, {
		states: states(
			llb.Image("busybox", llb.LinuxAmd64).Run(
				llb.Args([]string{"cat", "/tmp/unix.sock"}),
				slv.Forward("unix://./unix.sock", "/tmp/unix.sock"),
			).Root(),
		),
		expected: "forward",
	}} {
		tt := tt
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()
			sess := r.Session(t)
			ctx := llblib.WithSession(r.Context, sess)

			node, err := llblib.ToYAML(ctx, tt.states...)
			require.NoError(t, err, "converting state to YAML")

			for _, key := range []string{"local.sharedkeyhint", "local.unique", "secret"} {
				walky.Walk(node, walky.StringWalker(key, func(n *yaml.Node) error {
					n.Value = "test-constant"
					return nil
				}))
			}

			got, err := yaml.Marshal(node)
			require.NoError(t, err, "marshalling YAML")

			file := filepath.Join("test-data", tt.expected+".yaml")
			expected, err := os.ReadFile(file)
			require.NoError(t, err, "reading file: %s", file)
			require.Equal(t, string(expected), string(got))
		})
	}
}
