package llblib_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"braces.dev/errtrace"
	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/distribution/reference"
	"github.com/docker/docker/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

var busybox = llblib.Image("busybox@sha256:50aa4698fa6262977cff89181b2664b99d8a56dbca847bf62f2ef04854597cf8", llb.LinuxAmd64)

func TestDockerLoad(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, withTimeout(60*time.Second))
	if r.isMoby {
		t.Skip("DockerLoad is not supported with moby clients yet")
	}

	reader, writer := io.Pipe()
	dockerClient, err := client.NewClientWithOpts(
		client.WithAPIVersionNegotiation(),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		dockerClient.Close()
	})

	var eg errgroup.Group
	eg.Go(func() error {
		defer reader.Close()
		resp, err := dockerClient.ImageLoad(r.Context, reader, true)
		if err != nil {
			return errtrace.Errorf("failed to download image: %w", err)
		}
		defer resp.Body.Close()
		progress.FromReader(r.Progress, "importing to docker", resp.Body)
		return nil
	})

	ref, err := reference.Parse("llblib-test/docker-load")
	require.NoError(t, err)

	req := r.Solver.Build(
		busybox,
		llblib.DockerSave(ref, writer),
	)

	resp, err := r.Run(t, req)
	require.NoError(t, err)
	require.NotNil(t, resp.ExporterResponse)
	require.Contains(t, resp.ExporterResponse[exptypes.ExporterImageDigestKey], "sha256:")

	err = eg.Wait()
	require.NoError(t, err)
}

func TestDockerSave(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t, withTimeout(60*time.Second))
	if r.isMoby {
		// TODO not sure how to export the state as a docker tar, we can
		// get it to load it directly to the docker daemon though.
		t.Skip("DockerSave is not supported with moby clients yet")
	}

	ref, err := reference.Parse("llblib-test/docker-save")
	require.NoError(t, err)

	tdir := t.TempDir()
	save, err := os.Create(filepath.Join(tdir, "docker-save.tar"))
	require.NoError(t, err)
	t.Cleanup(func() {
		save.Close()
	})

	req := r.Solver.Build(
		busybox,
		llblib.DockerSave(ref, save),
	)

	resp, err := r.Run(t, req)
	require.NoError(t, err)
	require.NotNil(t, resp.ExporterResponse)
	require.Contains(t, resp.ExporterResponse[exptypes.ExporterImageDigestKey], "sha256:")

	fs, err := os.Stat(save.Name())
	require.NoError(t, err)
	require.Equal(t, fs.Size(), int64(2161152))
}
