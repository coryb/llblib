package llblib

import (
	"context"
	goerrors "errors"

	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	bksess "github.com/moby/buildkit/session"
	"github.com/pkg/errors"
	"golang.org/x/exp/slices"
	"gopkg.in/yaml.v3"
)

// Session provides a long running session used to solve requests.
type Session interface {
	// Do will attempt execute the provided request
	Do(ctx context.Context, req Request) (*client.SolveResponse, error)
	// ToYAML will serialize the requests to a yaml sequence to assist in
	// debugging or visualizing the solve requests.
	ToYAML(ctx context.Context, reqs ...Request) (*yaml.Node, error)
	// Release will ensure resources are released for the session.
	Release() error
}

type session struct {
	sess        *bksess.Session
	localDirs   map[string]string
	attachables []bksess.Attachable
	releasers   []func() error
	client      *client.Client
	resolver    *resolver
	progress    progress.Progress
}

var _ Session = (*session)(nil)

func (s *session) Release() error {
	// releasers are called in lifo order, then we finally close all the
	// active sessions
	var err error
	for i := len(s.releasers) - 1; i >= 0; i-- {
		if e := s.releasers[i](); err != nil {
			err = goerrors.Join(err, e)
		}
	}

	return s.sess.Close()
}

func (s *session) ToYAML(ctx context.Context, reqs ...Request) (*yaml.Node, error) {
	ctx = WithSession(ctx, s)
	states := []llb.State{}
	for _, req := range reqs {
		states = append(states, req.state)
	}
	return ToYAML(ctx, states...)
}

func (s *session) Do(ctx context.Context, req Request) (*client.SolveResponse, error) {
	attachables := slices.Clone(s.attachables)
	attachables = append(attachables, req.attachables...)

	solveOpt := client.SolveOpt{
		SharedSession:         s.sess,
		SessionPreInitialized: true,
		LocalDirs:             s.localDirs,
		Session:               attachables,
		AllowedEntitlements:   req.entitlements,
	}

	ctx = WithProgress(ctx, s.progress)
	ctx = WithSession(ctx, s)

	if req.buildFunc != nil {
		res, err := s.client.Build(ctx, solveOpt, "llblib", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			res, err := req.buildFunc(ctx, c)
			if err != nil && req.onError != nil {
				dropErr, moreErr := req.onError(ctx, c, err)
				if dropErr {
					return nil, moreErr
				}
				return nil, goerrors.Join(err, moreErr)
			}
			return res, err
		}, s.progress.Channel(progress.AddLabel(req.Label)))
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

	res, err := s.client.Build(ctx, solveOpt, "llblib", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		gwReq := gateway.SolveRequest{
			Evaluate:   req.evaluate,
			Definition: def.ToPB(),
		}
		res, err := c.Solve(ctx, gwReq)
		if err != nil && req.onError != nil {
			dropErr, moreErr := req.onError(ctx, c, err)
			if dropErr {
				return nil, moreErr
			}
			return nil, goerrors.Join(err, moreErr)
		}
		return res, err
	}, s.progress.Channel(progress.AddLabel(req.Label)))
	if err != nil {
		return nil, errors.Wrap(err, "solve failed")
	}
	return res, nil
}
