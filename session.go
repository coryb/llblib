package llblib

import (
	"context"
	"log"

	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
	bksess "github.com/moby/buildkit/session"
	"github.com/pkg/errors"
	"golang.org/x/exp/slices"
)

type Session interface {
	Release() error
	Do(ctx context.Context, req Request, p progress.Progress) (*client.SolveResponse, error)
}

type session struct {
	allSessions map[bksess.Attachable]*bksess.Session
	localDirs   map[string]string
	attachables []bksess.Attachable
	releasers   []func() error
	client      *client.Client
}

var _ Session = (*session)(nil)

func (s *session) Release() error {
	for _, sess := range s.allSessions {
		sess.Close()
	}
	// FIXME group errors
	for _, r := range s.releasers {
		if err := r(); err != nil {
			log.Printf("release error: %s", err)
		}
	}
	return nil
}

func (s *session) Do(ctx context.Context, req Request, p progress.Progress) (*client.SolveResponse, error) {
	sess := s.allSessions[req.download]

	attachables := slices.Clone(s.attachables)
	if req.download != nil {
		attachables = append(attachables, req.download)
	}

	solveOpt := client.SolveOpt{
		SharedSession:         sess,
		SessionPreInitialized: true,
		LocalDirs:             s.localDirs,
		Session:               attachables,
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
