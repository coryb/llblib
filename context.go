package llblib

import (
	"context"

	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
)

type (
	progressKey struct{}
	sessionKey  struct{}
)

func WithProgress(ctx context.Context, p progress.Progress) context.Context {
	return context.WithValue(ctx, progressKey{}, p)
}

func LoadProgress(ctx context.Context) progress.Progress {
	p, ok := ctx.Value(progressKey{}).(progress.Progress)
	if !ok {
		return nullProgress{}
	}
	return p
}

type nullProgress struct{}

var _ progress.Progress = (*nullProgress)(nil)

func (p nullProgress) Release() {}
func (p nullProgress) Sync()    {}
func (p nullProgress) Pause()   {}
func (p nullProgress) Resume()  {}
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

func WithSession(ctx context.Context, s Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

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

func (nullSession) Do(ctx context.Context, req Request, p progress.Progress) (*client.SolveResponse, error) {
	return nil, nil
}
