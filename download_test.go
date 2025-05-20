package llblib_test

import (
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func buildExample(r testRunner, opts ...llb.RunOption) llb.State {
	currentPlatform := ocispec.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	return llblib.Image("golang:1.24", llb.Platform(currentPlatform)).Run(
		llb.Args([]string{"go", "build", "-o", "build/", "./examples/build"}),
		llb.Dir(r.WorkDir),
		llb.AddMount(r.WorkDir, goSource(r.Solver)),
		llb.AddMount(path.Join(r.WorkDir, "build"), llb.Scratch()),
		llblib.AddCacheMounts(
			[]string{"/go/pkg/mod", "/root/.cache/go-build"},
			"gocache",
			llb.CacheMountShared,
		),
		llblib.RunOptions(opts),
	).GetMount(path.Join(r.WorkDir, "build"))
}

func TestParallelDownloads(t *testing.T) {
	t.Parallel()

	tdir := t.TempDir()

	r := newTestRunner(t, withTimeout(10*time.Minute))

	var eg errgroup.Group
	eg.Go(func() error {
		_, err := r.Run(t, r.Solver.Build(
			buildExample(r, llb.AddEnv("GOARCH", "amd64")),
			llblib.Download(filepath.Join(tdir, "build", "amd64")),
			llblib.WithLabel("linux/amd64"),
		))
		return err
	})
	eg.Go(func() error {
		_, err := r.Run(t, r.Solver.Build(
			buildExample(r, llb.AddEnv("GOARCH", "arm64")),
			llblib.Download(filepath.Join(tdir, "build", "arm64")),
			llblib.WithLabel("linux/arm64"),
		))
		return err
	})
	err := eg.Wait()
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(tdir, "build", "amd64", "build"))
	require.NoError(t, err)
	out, err := exec.Command("go", "version", "-m", filepath.Join(tdir, "build", "amd64", "build")).CombinedOutput()
	require.NoError(t, err)
	require.Contains(t, string(out), "GOARCH=amd64")
	_, err = os.Stat(filepath.Join(tdir, "build", "arm64", "build"))
	require.NoError(t, err)
	out, err = exec.Command("go", "version", "-m", filepath.Join(tdir, "build", "arm64", "build")).CombinedOutput()
	require.NoError(t, err)
	require.Contains(t, string(out), "GOARCH=arm64")
}
