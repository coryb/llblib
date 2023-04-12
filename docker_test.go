package llblib_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	specsv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestDockerfile(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t)

	linux := specsv1.Platform{
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
		llblib.WithTargetPlatform(&linux),
	)

	tdir := t.TempDir()
	req := r.Solver.Build(st, llblib.Download(tdir))

	err := r.Run(t, req)
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

	linux := specsv1.Platform{
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
		llblib.WithTargetPlatform(&linux),
		llblib.WithBuildArg("MSG", "custom"),
	)

	tdir := t.TempDir()
	req := r.Solver.Build(st, llblib.Download(tdir))

	err := r.Run(t, req)
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
