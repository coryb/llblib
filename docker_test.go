package llblib_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestDockerfile(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t)

	linux := ocispec.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	dockerfile := `
		FROM busybox AS start
		RUN echo start > start
		FROM busybox AS hi
		RUN echo hi > hi
		FROM scratch AS download
		COPY --from=start start start
		COPY --from=hi hi hi
		FROM busybox
		RUN false # <- should not run
	`
	st := llblib.Dockerfile(
		[]byte(dockerfile),
		llb.Scratch(),
		llblib.WithTarget("download"),
		llblib.WithTargetPlatform(linux),
	)

	tdir := t.TempDir()
	req := r.Solver.Build(st, llblib.Download(tdir))

	_, err := r.Run(t, req)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(tdir, "start"))
	require.NoError(t, err)
	require.Equal(t, "start\n", string(got))

	got, err = os.ReadFile(filepath.Join(tdir, "hi"))
	require.NoError(t, err)
	require.Equal(t, "hi\n", string(got))
}

func TestDockerfileSyntax(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t)

	linux := ocispec.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	dockerfile := `# syntax=docker/dockerfile:1.3-labs
FROM busybox AS input
ARG MSG=msg
RUN <<EOM
echo start > start
echo hi > hi
echo ${MSG} > msg
EOM
FROM scratch AS download
COPY --from=input start hi msg .
FROM busybox
RUN false # <- should not run
`
	st := llblib.Dockerfile(
		[]byte(dockerfile),
		llb.Scratch(),
		llblib.WithTarget("download"),
		llblib.WithTargetPlatform(linux),
		llblib.WithBuildArg("MSG", "custom"),
	)

	tdir := t.TempDir()
	req := r.Solver.Build(st, llblib.Download(tdir))

	_, err := r.Run(t, req)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(tdir, "start"))
	require.NoError(t, err)
	require.Equal(t, "start\n", string(got))

	got, err = os.ReadFile(filepath.Join(tdir, "hi"))
	require.NoError(t, err)
	require.Equal(t, "hi\n", string(got))

	got, err = os.ReadFile(filepath.Join(tdir, "msg"))
	require.NoError(t, err)
	require.Equal(t, "custom\n", string(got))
}

func TestDockerfileBuildContexts(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t)

	linux := ocispec.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	dockerfile1 := `
		FROM busybox
		# copy from default build-context
		COPY file1 file1
		# copy from extra build-context
		COPY --from=extra file2 file2
	`

	dockerfile2 := `
		FROM busybox
		# copy from default build-context
		COPY file3 file3
		# copy from extra build-context
		COPY --from=extra file2 file4
	`

	tdir := t.TempDir()
	err := os.WriteFile(filepath.Join(tdir, "file2"), nil, 0o644)
	require.NoError(t, err)

	st1 := llblib.Dockerfile(
		[]byte(dockerfile1),
		llb.Scratch().File(llb.Mkfile("file1", 0o644, nil)),
		llblib.WithTargetPlatform(linux),
		llblib.WithBuildContext("extra", r.Solver.Local(tdir)),
	)

	st := llblib.Dockerfile(
		[]byte(dockerfile2),
		llb.Scratch().File(llb.Mkfile("file3", 0o644, nil)),
		llblib.WithTargetPlatform(linux),
		llblib.WithBuildContext("extra", st1),
	)

	req := r.Solver.Build(st)
	_, err = r.Run(t, req)
	require.NoError(t, err)
}

func TestDockerfileRunMounts(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t)

	linux := ocispec.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	dockerfile := `
		FROM busybox
		RUN --mount=type=bind,from=my.input,source=file1,target=/opt/file1 ls -l /opt/file1
		RUN --mount=type=cache,from=my.cache,target=/cache ls -l /cache/file1
		RUN --mount=type=tmpfs,target=/tmp touch /tmp/foobar
		RUN --mount=type=secret,id=my.secret,target=/secret test $(cat /secret) = "shhh"
	`
	inputDir := t.TempDir()
	err := os.WriteFile(filepath.Join(inputDir, "file1"), nil, 0o644)
	require.NoError(t, err)

	secretDir := t.TempDir()
	err = os.WriteFile(filepath.Join(secretDir, "secret"), []byte("shhh"), 0o644)
	require.NoError(t, err)

	st := llblib.Dockerfile(
		[]byte(dockerfile),
		llb.Scratch(),
		llblib.WithTargetPlatform(linux),
		llblib.WithBuildContext("my.input", r.Solver.Local(inputDir)),
		llblib.WithBuildContext("my.cache", r.Solver.Local(inputDir)),
	)

	r.Solver.AddSecretFile(filepath.Join(secretDir, "secret"), "", llb.SecretID("my.secret"))

	req := r.Solver.Build(st)
	_, err = r.Run(t, req)
	require.NoError(t, err)
}
