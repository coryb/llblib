package llblib

import (
	"context"
	"net"

	dockerclient "github.com/docker/docker/client"
	"github.com/moby/buildkit/client"
	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
	_ "github.com/moby/buildkit/client/connhelper/kubepod"
	"github.com/pkg/errors"
)

func NewClient(ctx context.Context, addr string, opts ...client.ClientOpt) (*client.Client, error) {
	cln, err := newClient(ctx, addr, opts)
	if err != nil {
		return nil, err
	}
	// if _, err = cln.ListWorkers(ctx); err != nil {
	// 	cln.Close()
	// 	return nil, errors.Wrap(err, "unable to connect to buildkitd")
	// }
	return cln, nil
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
