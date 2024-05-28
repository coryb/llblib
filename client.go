package llblib

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"

	"braces.dev/errtrace"
	dockerclient "github.com/docker/docker/client"
	"github.com/moby/buildkit/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	// provide the docker-container://<name> scheme for buildkit address
	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
	// provide the kube-pod://<pod> scheme for the buildkit address
	_ "github.com/moby/buildkit/client/connhelper/kubepod"
)

// NewClient will return a new buildkit client.  It will verify the client
// connection by calling the client.Info function when available, otherwise will
// call client.ListWorkers.  If the provided addr is empty, we attempt to use
// the buildkit service running in your local docker daemon.
//
// To use the latest buildkit you can run buildkit via docker or run your own
// service deployment.  To run via docker first start the container:
//
//	docker run -d --name buildkitd --privileged moby/buildkit:latest
//
// Then use this `addr` value: `docker-container://buildkitd`
func NewClient(ctx context.Context, addr string, opts ...client.ClientOpt) (c *client.Client, isMoby bool, err error) {
	cln, isMoby, err := newClient(ctx, addr, opts...)
	if err != nil {
		return nil, false, errtrace.Wrap(err)
	}

	_, err = cln.Info(ctx)
	if err == nil {
		return cln, isMoby, nil
	}

	// Info API added in v0.11
	if !ErrUnimplemented(err) {
		cln.Close()
		return nil, false, errtrace.Errorf("unable to connect to buildkitd: %w", err)
	}

	// If we are still here then Info is Unimplemented, so fallback to
	// ListWorkers which can be a bit slower
	if _, err := cln.ListWorkers(ctx); err != nil {
		cln.Close()
		return nil, false, errtrace.Errorf("unable to connect to buildkitd: %w", err)
	}
	return cln, isMoby, nil
}

type grpcError interface{ GRPCStatus() *status.Status }

// ErrUnimplemented will return true if the provided error is a GRPC error
// and the GRPC status code matches `codes.Unimplemented`.
func ErrUnimplemented(err error) bool {
	var grpcErr grpcError
	if errors.As(err, &grpcErr) && grpcErr.GRPCStatus().Code() == codes.Unimplemented {
		return true
	}
	return false
}

func newClient(ctx context.Context, addr string, opts ...client.ClientOpt) (c *client.Client, isMoby bool, err error) {
	if addr != "" && !strings.HasPrefix(addr, "docker://") {
		c, err := client.New(ctx, addr, opts...)
		return c, false, errtrace.Wrap(err)
	}

	host, err := DockerHost(ctx)
	if err != nil {
		return nil, false, errtrace.Errorf("unable to determine docker host: %w", err)
	}
	// no address, attempt to use docker built-in buildkit
	dc, err := dockerclient.NewClientWithOpts(
		dockerclient.WithHost(host),
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, false, errtrace.Errorf("buildkitd address empty and failed to connect to docker: %w", err)
	}
	opts = append(opts, client.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return errtrace.Wrap2(dc.DialHijack(ctx, "/grpc", "h2c", nil))
	}), client.WithSessionDialer(func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
		return errtrace.Wrap2(dc.DialHijack(ctx, "/session", proto, meta))
	}))
	c, err = client.New(ctx, "", opts...)
	return c, true, errtrace.Wrap(err)
}

// DockerDir returns the path to the user's Docker config dir.
// Reads DOCKER_CONFIG, and HOME env vars.
func DockerDir(ctx context.Context) string {
	dockerConfigDir := Getenv(ctx, "DOCKER_CONFIG")
	if dockerConfigDir != "" {
		return dockerConfigDir
	}

	home := Getenv(ctx, "HOME")
	if home == "" {
		return ""
	}

	return filepath.Join(home, ".docker")
}

// DockerConf returns the path to the user's Docker config.json.
func DockerConf(dockerDir string) string {
	if dockerDir == "" {
		return ""
	}
	return filepath.Join(dockerDir, "config.json")
}

// DockerHost returns the value of the DOCKER_HOST env var, or the default.
func DockerHost(ctx context.Context) (string, error) {
	dockerHost := Getenv(ctx, "DOCKER_HOST")
	if dockerHost != "" {
		return dockerHost, nil
	}

	dockerDir := DockerDir(ctx)
	if dockerDir == "" {
		return dockerclient.DefaultDockerHost, nil
	}

	dockerConfigPath := DockerConf(dockerDir)
	var dockerConfig struct {
		CurrentContext string `json:"currentContext"`
	}
	configBytes, err := os.ReadFile(dockerConfigPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return dockerclient.DefaultDockerHost, nil
		}
		return "", errtrace.Errorf("reading %s: %w", dockerConfigPath, err)
	}

	if err := json.Unmarshal(configBytes, &dockerConfig); err != nil {
		return "", errtrace.Errorf("invalid json in %s: %w", dockerConfigPath, err)
	}

	if dockerConfig.CurrentContext != "" {
		var contextHost string
		contextDir := filepath.Join(dockerDir, "contexts")
		err := filepath.WalkDir(contextDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return errtrace.Wrap(err)
			}
			if contextHost != "" ||
				d.IsDir() ||
				!strings.HasSuffix(d.Name(), ".json") {
				return nil
			}

			var dockerContext struct {
				Name      string `json:"name"`
				Endpoints struct {
					Docker struct {
						Host string `json:"host"`
					} `json:"docker"`
				} `json:"endpoints"`
			}
			contextBytes, err := os.ReadFile(path)
			if err != nil {
				return errtrace.Wrap(err)
			}
			if err := json.Unmarshal(contextBytes, &dockerContext); err != nil {
				return errtrace.Errorf("failed to unmarshal %s: %w", path, err)
			}
			if dockerContext.Name == dockerConfig.CurrentContext {
				contextHost = dockerContext.Endpoints.Docker.Host
			}
			return nil
		})
		if err != nil {
			return "", errtrace.Errorf("failed to walk docker contexts dir: %s: %w", contextDir, err)
		}
		if contextHost != "" {
			return contextHost, nil
		}
	}
	return dockerclient.DefaultDockerHost, nil
}
