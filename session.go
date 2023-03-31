package llblib

import (
	"context"
	"log"

	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
	bksess "github.com/moby/buildkit/session"
	"github.com/pkg/errors"
)

type Session interface {
	Release() error
	Do(ctx context.Context, req Request, p progress.Progress) (*client.SolveResponse, error)
}

type session struct {
	session     *bksess.Session
	localDirs   map[string]string
	attachables []bksess.Attachable
	releasers   []func() error
	client      *client.Client
}

var _ Session = (*session)(nil)

func (s *session) Release() error {
	s.session.Close()
	// FIXME group errors
	for _, r := range s.releasers {
		if err := r(); err != nil {
			log.Printf("release error: %s", err)
		}
	}
	return nil
}

func (s *session) Do(ctx context.Context, req Request, p progress.Progress) (*client.SolveResponse, error) {
	solveOpt := client.SolveOpt{
		SharedSession:         s.session,
		SessionPreInitialized: true,
		LocalDirs:             s.localDirs,
		Session:               s.attachables,
	}

	ctx = WithProgress(ctx, p)
	ctx = WithSession(ctx, s)

	if req.buildFunc != nil {
		res, err := s.client.Build(ctx, solveOpt, "llblib", req.buildFunc, p.Channel(progress.AddLabel(req.Label)))
		if err != nil {
			return nil, errors.Wrap(err, "build failed")
		}
		return res, nil
	}

	solveOpt.Exports = req.exports
	def, err := req.state.Marshal(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to marshal state")
	}

	res, err := s.client.Solve(ctx, def, solveOpt, p.Channel(progress.AddLabel(req.Label)))
	if err != nil {
		return nil, errors.Wrap(err, "solve failed")
	}
	return res, nil
}
