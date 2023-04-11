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
	require.Equal(t, []byte("start\n"), got)

	got, err = os.ReadFile(filepath.Join(tdir, "hi"))
	require.NoError(t, err)
	require.Equal(t, []byte("hi\n"), got)
}
