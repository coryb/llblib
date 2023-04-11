package llblib

import (
	"context"
	"net"

	dockerclient "github.com/docker/docker/client"
	"github.com/moby/buildkit/client"
	"github.com/pkg/errors"
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
func NewClient(ctx context.Context, addr string, opts ...client.ClientOpt) (*client.Client, error) {
	cln, err := newClient(ctx, addr, opts)
	if err != nil {
		return nil, err
	}

	if _, err := cln.Info(ctx); err != nil {
		// Info API added in v0.11
		if !ErrUnimplemented(err) {
			cln.Close()
			return nil, errors.Wrapf(err, "unable to connect to buildkitd")
		}
	} else {
		return cln, nil
	}

	// If we are still here then Info is Unimplemented, so fallback to
	// ListWorkers which can be a bit slower
	if _, err := cln.ListWorkers(ctx); err != nil {
		cln.Close()
		return nil, errors.Wrap(err, "unable to connect to buildkitd")
	}
	return cln, nil
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

func newClient(ctx context.Context, addr string, opts ...client.ClientOpt) (*client.Client, error) {
	if addr != "" {
		return client.New(ctx, addr, opts...)
	}

	// no address, attempt to use docker built-in buildkit
	dc, err := dockerclient.NewClientWithOpts(
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, errors.Wrap(err, "buildkitd address empty and failed to connect to docker")
	}
	opts = append(opts, client.WithContextDialer(func(context.Context, string) (net.Conn, error) {
		return dc.DialHijack(ctx, "/grpc", "h2c", nil)
	}), client.WithSessionDialer(func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
		return dc.DialHijack(ctx, "/session", proto, meta)
	}))
	return client.New(ctx, "", opts...)
}
