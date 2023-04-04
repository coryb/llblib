package llblib

import (
	"context"

	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
)

type (
	progressKey      struct{}
	sessionKey       struct{}
	imageResolverKey struct{}
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
		for range ch {
			// toss
		}
	}()
	return ch
}

// WithSession returns a context with the provided session stored.
func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// LoadSession returns a session stored on the context, or a no-op session.
func LoadSession(ctx context.Context) Session {
	s, ok := ctx.Value(sessionKey{}).(Session)
	if !ok {
		return nullSession{}
	}
	return s
}

type nullSession struct{}

func (nullSession) Release() error {
	return nil
}

func (nullSession) Do(ctx context.Context, req Request) (*client.SolveResponse, error) {
	return nil, nil
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
