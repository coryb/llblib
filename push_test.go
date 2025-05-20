package llblib_test

import (
	"context"
	"io"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/distribution/reference"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	specsv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func startRegistry(ctx context.Context, t *testing.T) int {
	t.Helper()
	dockerClient, err := client.NewClientWithOpts(
		client.WithAPIVersionNegotiation(),
	)
	require.NoError(t, err)
	defer dockerClient.Close()

	cfg := container.Config{
		Image: "registry:2.8.3",
	}

	hostCfg := &container.HostConfig{
		PublishAllPorts: true,
		AutoRemove:      true,
	}

	body, err := dockerClient.ImagePull(ctx, cfg.Image, image.PullOptions{})
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, body)
	require.NoError(t, err)
	body.Close()

	service, err := dockerClient.ContainerCreate(ctx, &cfg, hostCfg, nil, nil, "")
	require.NoError(t, err)

	err = dockerClient.ContainerStart(ctx, service.ID, container.StartOptions{})
	require.NoError(t, err)

	t.Cleanup(func() {
		err := dockerClient.ContainerStop(context.Background(), service.ID, container.StopOptions{})
		require.NoError(t, err)
	})

	inspect, err := dockerClient.ContainerInspect(ctx, service.ID)
	require.NoError(t, err)

	for _, port := range inspect.NetworkSettings.Ports[nat.Port("5000/tcp")] {
		// ignore v6
		if !strings.Contains(port.HostIP, ":") {
			port, err := strconv.Atoi(port.HostPort)
			require.NoError(t, err)
			return port
		}
	}
	t.Fatalf("failed to find container port 5000/tcp")
	return 0
}

func registryRef(t *testing.T, registryPort int, name string) reference.Named {
	t.Helper()
	ref, err := reference.ParseNormalizedNamed("127.0.0.1:" + strconv.Itoa(registryPort) + "/" + name)
	require.NoError(t, err)
	return ref
}

func TestRegistryPushImage(t *testing.T) {
	r := newTestRunner(t, withTimeout(60*time.Second))

	currentPlatform := specsv1.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	regPort1 := startRegistry(r.Context, t)
	regPort2 := startRegistry(r.Context, t)

	req := r.Solver.Build(
		llblib.Image("alpine:latest", llb.Platform(currentPlatform)),
		llblib.RegistryPush(registryRef(t, regPort1, "llblib-test/registry-push-image")),
		llblib.RegistryPush(registryRef(t, regPort2, "llblib-test/registry-push-image")),
	)

	resp, err := r.Run(t, req)
	require.NoError(t, err)
	require.NotNil(t, resp.ExporterResponse)
	require.Contains(t, resp.ExporterResponse[exptypes.ExporterImageDigestKey], "sha256:")
}

func TestRegistryPushDockerfile(t *testing.T) {
	r := newTestRunner(t, withTimeout(60*time.Second))

	currentPlatform := specsv1.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	regPort1 := startRegistry(r.Context, t)
	regPort2 := startRegistry(r.Context, t)

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
		llblib.WithTargetPlatform(currentPlatform),
	)

	req := r.Solver.Build(
		st,
		llblib.RegistryPush(registryRef(t, regPort1, "llblib-test/registry-push-dockerfile")),
		llblib.RegistryPush(registryRef(t, regPort2, "llblib-test/registry-push-dockerfile")),
	)

	resp, err := r.Run(t, req)
	require.NoError(t, err)
	require.NotNil(t, resp.ExporterResponse)
	require.Contains(t, resp.ExporterResponse[exptypes.ExporterImageDigestKey], "sha256:")
}
