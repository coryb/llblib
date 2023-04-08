package llblib_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client/llb"
	specsv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestDockerfile(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	cln, err := llblib.NewClient(ctx, os.Getenv("BUILDKIT_HOST"))
	if err != nil {
		t.Errorf("Failed to create client: %s", err)
	}
	t.Cleanup(func() {
		cln.Close()
	})

	linux := specsv1.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	slv := llblib.NewSolver()
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
	req := slv.Build(st, llblib.Download(tdir))

	prog := progress.NewProgress(progress.WithOutput(&testWriter{t}))
	t.Cleanup(func() {
		prog.Close()
	})

	sess, err := slv.NewSession(ctx, cln, prog)
	if err != nil {
		t.Errorf("failed to create session: %s", err)
	}
	t.Cleanup(func() {
		sess.Release()
	})

	_, err = sess.Do(ctx, req)
	if err != nil {
		t.Errorf("solve failed: %s", err)
	}

	got, err := os.ReadFile(filepath.Join(tdir, "start"))
	if err != nil {
		t.Errorf("failed to read downloaded file: %s", err)
	}
	require.Equal(t, []byte("start\n"), got)

	got, err = os.ReadFile(filepath.Join(tdir, "hi"))
	if err != nil {
		t.Errorf("failed to read downloaded file: %s", err)
	}
	require.Equal(t, []byte("hi\n"), got)
}
