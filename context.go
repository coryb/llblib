package llblib

import (
	"context"
	"os"

	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
)

type (
	progressKey      struct{}
	gatewayClientKey struct{}
	imageResolverKey struct{}
	sessionIDKey     struct{}
	envKey           string
)

// WithProgress returns a context with the provided Progress stored.
func WithProgress(ctx context.Context, p progress.Progress) context.Context {
	return context.WithValue(ctx, progressKey{}, p)
}

// LoadProgress returns a progress stored on the context, or a no-op progress.
func LoadProgress(ctx context.Context) progress.Progress {
	p, ok := ctx.Value(progressKey{}).(progress.Progress)
	if !ok {
		return nullProgress{}
	}
	return p
}

type nullProgress struct{}

var _ progress.Progress = (*nullProgress)(nil)

func (p nullProgress) Close() error { return nil }
func (p nullProgress) Sync() error  { return nil }
func (p nullProgress) Pause() error { return nil }
func (p nullProgress) Resume()      {}
func (p nullProgress) Label(string) progress.Progress {
	return p
}

func (p nullProgress) Channel(opts ...progress.ChannelOption) chan *client.SolveStatus {
	ch := make(chan *client.SolveStatus)
	go func() {
		for range ch { //nolint:revive
			// toss
		}
	}()
	return ch
}

func withSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, id)
}

func sessionID(ctx context.Context) string {
	t, ok := ctx.Value(sessionIDKey{}).(string)
	if !ok {
		return ""
	}
	return t
}

func withGatewayClient(ctx context.Context, c gateway.Client) context.Context {
	return context.WithValue(ctx, gatewayClientKey{}, c)
}

func gatewayClient(ctx context.Context) gateway.Client {
	c, ok := ctx.Value(gatewayClientKey{}).(gateway.Client)
	if !ok {
		return nil
	}
	return c
}

// WithImageResolver returns a context with the provided llb.ImageMetaResovler
// stored.
func WithImageResolver(ctx context.Context, r llb.ImageMetaResolver) context.Context {
	return context.WithValue(ctx, imageResolverKey{}, r)
}

// LoadImageResolver returns a llb.ImageMetaResolver stored on the context,
// or will return `nil` if no resolver is found.
func LoadImageResolver(ctx context.Context) llb.ImageMetaResolver {
	r, ok := ctx.Value(imageResolverKey{}).(llb.ImageMetaResolver)
	if !ok {
		return nil
	}
	return r
}

// WithEnv is used to set env vars on a context.
func WithEnv(ctx context.Context, key, value string) context.Context {
	return context.WithValue(ctx, envKey(key), value)
}

// LookupEnv will fetch a env var stored on the context or default to calling
// os.LookupEnv
func LookupEnv(ctx context.Context, key string) (value string, ok bool) {
	if val, ok := ctx.Value(envKey(key)).(string); ok {
		return val, ok
	}
	return os.LookupEnv(key)
}

// Getenv will fetch an env var stored on the context or default to calling
// os.Getenv
func Getenv(ctx context.Context, key string) (value string) {
	val, _ := LookupEnv(ctx, key)
	return val
}
