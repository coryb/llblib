package llblib

import (
	"context"

	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
)

type progressKey struct{}

func WithProgress(ctx context.Context, p progress.Progress) context.Context {
	return context.WithValue(ctx, progressKey{}, p)
}

func Progress(ctx context.Context) progress.Progress {
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

func (p nullProgress) Channel() chan *client.SolveStatus {
	ch := make(chan *client.SolveStatus)
	go func() {
		for range ch {
			// toss
		}
	}()
	return ch
}
