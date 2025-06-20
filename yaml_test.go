package llblib_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/coryb/walky"
	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestYAML(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, withTimeout(60*time.Second))

	def, err := llblib.MarshalWithImageConfig(context.Background(),
		llblib.Image("busybox@sha256:238497f91e51816fc6abe8acd5276c156cad31c38197318f3e8e5242aae8332e", llb.LinuxAmd64).Run(
			llb.Args([]string{"cat", "/tmp/unix.sock"}),
			r.Solver.Forward("unix://./unix.sock", "/tmp/unix.sock"),
		).Root(),
	)
	require.NoError(t, err)

	states := func(s ...llb.State) []llb.State {
		return s
	}

	for _, tt := range []struct {
		states   []llb.State
		unstable bool
		expected string
	}{{
		states:   states(llb.Scratch()),
		expected: "scratch",
	}, {
		states:   states(r.Solver.Local(".")),
		expected: "local",
	}, {
		states:   states(llblib.Image("golang:1.20.1", llb.LinuxAmd64)),
		expected: "image",
	}, {
		states: states(
			llblib.Diff(
				llblib.Image("golang:1.20.1", llb.LinuxAmd64),
				llblib.Image("golang:1.20.1", llb.LinuxAmd64).File(
					llb.Mkdir("/foobar", 0o755).Mkfile("/foobar/file", 0o644, []byte("contents")),
				),
			),
		),
		expected: "diff",
	}, {
		states: states(
			llblib.Merge([]llb.State{
				llblib.Image("golang:1.20.1", llb.LinuxAmd64),
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
			llblib.Image("golang:1.20.1", llb.LinuxAmd64).Run(
				llb.Args([]string{"/bin/true"}),
			).Root(),
		),
		expected: "run",
	}, {
		states: states(
			llblib.Image("golang:1.20.1", llb.LinuxAmd64).Run(
				llb.Args([]string{"/bin/true"}),
				llb.WithCustomName("good build"),
			).Root(),
			llblib.Image("golang:1.20.1", llb.LinuxAmd64).Run(
				llb.Args([]string{"/bin/false"}),
				llb.WithCustomName("bad build"),
			).Root(),
		),
		expected: "runs",
	}, {
		states: states(
			llblib.Image("golang:1.20.1", llb.LinuxAmd64).Run(
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
				llblib.Image("golang:1.20.1", llb.LinuxAmd64),
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
				FROM busybox@sha256:b5d6fe0712636ceb7430189de28819e195e8966372edfc2d9409d79402a0dc16 AS start
				RUN echo start > start
				FROM busybox@sha256:b5d6fe0712636ceb7430189de28819e195e8966372edfc2d9409d79402a0dc16 AS hi
				RUN echo hi > hi
				FROM scratch AS download
				COPY --from=start start start
				COPY --from=hi hi hi
				FROM busybox@sha256:b5d6fe0712636ceb7430189de28819e195e8966372edfc2d9409d79402a0dc16
				RUN false # <- should not run
			`),
			llb.Scratch(),
			llblib.WithTarget("download"),
			llblib.WithTargetPlatform(ocispec.Platform{
				OS: "linux", Architecture: "arm64",
			}),
		)),
		unstable: true,
		expected: "dockerfile",
	}, {
		states: states(llblib.Dockerfile(
			[]byte(`
				FROM busybox@sha256:b5d6fe0712636ceb7430189de28819e195e8966372edfc2d9409d79402a0dc16
				USER nobody
				WORKDIR /tmp
				RUN echo hi
			`),
			llb.Scratch(),
			llblib.WithTargetPlatform(ocispec.Platform{
				OS: "linux", Architecture: "arm64",
			}),
		)),
		expected: "dockerfile-user",
	}, {
		states: states(
			llblib.Image("busybox@sha256:238497f91e51816fc6abe8acd5276c156cad31c38197318f3e8e5242aae8332e", llb.LinuxAmd64).Run(
				llb.Shlex("cat /secret"),
				r.Solver.AddSecretFile("yaml_test.go", "/secret"),
			).Root(),
		),
		expected: "secrets",
	}, {
		states: states(
			llblib.Image("busybox@sha256:238497f91e51816fc6abe8acd5276c156cad31c38197318f3e8e5242aae8332e", llb.LinuxAmd64).Run(
				llb.Args([]string{"/bin/sh", "-c", "echo multi\necho line\necho statement"}),
			).Root(),
		),
		expected: "script",
	}, {
		states: states(
			llblib.Image("busybox@sha256:238497f91e51816fc6abe8acd5276c156cad31c38197318f3e8e5242aae8332e", llb.LinuxAmd64).Run(
				llb.Args([]string{"cat", "/tmp/unix.sock"}),
				r.Solver.Forward("unix://./unix.sock", "/tmp/unix.sock"),
			).Root(),
		),
		expected: "forward",
	}, {
		states: states(
			llb.Scratch().File(
				llb.Mkdir(
					"foo", 0o755,
				).Mkfile(
					"foo/bar", 0o644, []byte("bar content"),
				).Mkfile(
					"foo/bad", 0o644, []byte("bad content"),
				).Copy(
					llb.Scratch().File(llb.Mkfile("baz", 0o644, []byte("baz content"))), "/", "foo",
				).Rm(
					"foo/bad",
				),
			),
		),
		expected: "file",
	}, {
		states: states(
			llblib.BuildDefinition(def),
		),
		expected: "def",
	}} {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()
			sess := r.Session(t)
			ctx := llblib.WithSession(r.Context, sess)

			file := filepath.Join("test-data", tt.expected+".yaml")
			expected, err := os.ReadFile(file)
			require.NoError(t, err, "reading file: %s", file)

			attempt := func(t require.TestingT) bool {
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
				return assert.Equal(t, string(expected), string(got))
			}

			if tt.unstable {
				// llblib.ToYAML might not consistently serialize due to states
				// being resolved by frontends, in which case we might get a list of
				// operations in a random order.  To work around this we just
				// retry the serialization up to 10 times until it matches our
				// expected output.
				require.EventuallyWithT(t, func(t *assert.CollectT) {
					attempt(t)
				}, time.Second, 100*time.Millisecond)
			} else {
				require.True(t, attempt(t))
			}
		})
	}
}
