package llblib

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"braces.dev/errtrace"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	bksess "github.com/moby/buildkit/session"
	"gopkg.in/yaml.v3"
)

const (
	// ExporterImageConfigKey is the key used to store the image config in the
	// client.SolveResponse.ExporterResponse returned from Session.Do.
	ExporterImageConfigKey = "llblib.containerimage.config"
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
	allSessions map[bksess.Attachable]*bksess.Session
	localDirs   map[string]string
	attachables []bksess.Attachable
	releasers   []func() error
	client      *client.Client
	isMoby      bool
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
			err = errors.Join(err, e)
		}
	}

	for _, sess := range s.allSessions {
		err = errors.Join(err, errtrace.Wrap(sess.Close()))
	}

	return err
}

func (s *session) ToYAML(ctx context.Context, reqs ...Request) (*yaml.Node, error) {
	ctx = WithSession(ctx, s)
	states := []llb.State{}
	for _, req := range reqs {
		states = append(states, req.state)
	}
	return errtrace.Wrap2(ToYAML(ctx, states...))
}

func anyValue[K comparable, V any](m map[K]V) V {
	var zero V
	for _, v := range m {
		return v
	}
	return zero
}

func (s *session) Do(ctx context.Context, req Request) (*client.SolveResponse, error) {
	// default to a random session, but if there is a download attachable
	// for this request we need to use that specific session (only one download
	// attachable per session is supported by buildkit)
	sess := anyValue(s.allSessions)

	if req.download != nil {
		sess = s.allSessions[req.download]
	}

	attachables := slices.Clone(s.attachables)
	if req.download != nil {
		attachables = append(attachables, req.download)
	}

	entitlements := make([]string, len(req.entitlements))
	for i, e := range req.entitlements {
		entitlements[i] = e.String()
	}

	solveOpt := client.SolveOpt{
		SharedSession:         sess,
		SessionPreInitialized: true,
		LocalDirs:             s.localDirs,
		Session:               attachables,
		AllowedEntitlements:   entitlements,
	}

	prog := s.progress.Label(req.Label)
	ctx = WithProgress(ctx, prog)
	ctx = WithSession(ctx, s)
	ctx = withSessionID(ctx, sess.ID())

	if req.buildFunc != nil {
		res, err := s.client.Build(ctx, solveOpt, "llblib", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			res, err := req.buildFunc(ctx, c)
			if err != nil && req.onError != nil {
				dropErr, moreErr := req.onError(ctx, c, err)
				if dropErr {
					return nil, errtrace.Wrap(moreErr)
				}
				return nil, errors.Join(err, errtrace.Wrap(moreErr))
			}
			return res, errtrace.Wrap(err)
		}, prog.Channel())
		if err != nil {
			return nil, errtrace.Errorf("build failed: %w", err)
		}
		return res, nil
	}

	def, err := req.state.Marshal(ctx)
	if err != nil {
		return nil, errtrace.Errorf("failed to marshal state: %w", err)
	}

	solveOpt.Exports = slices.Clone(req.exports)
	if s.isMoby {
		for i, export := range solveOpt.Exports {
			if export.Type == client.ExporterImage {
				// I don't know why this is necessary, but if we are using
				// buildkit inside of docker service, then we need to use the
				// "moby" type exporter instead of the "image" exporter used
				// with "normal" buildkit clients.
				solveOpt.Exports[i].Type = "moby"
				solveOpt.Exports[i].Output = nil
			}
		}
	}

	var imageconfig []byte
	res, err := s.client.Build(ctx, solveOpt, "llblib", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		gwReq := gateway.SolveRequest{
			Evaluate:   req.evaluate,
			Definition: def.ToPB(),
		}
		res, err := c.Solve(ctx, gwReq)
		if err != nil {
			if req.onError != nil {
				dropErr, moreErr := req.onError(ctx, c, err)
				if dropErr {
					return nil, errtrace.Wrap(moreErr)
				}
				return nil, errors.Join(err, errtrace.Wrap(moreErr))
			}
			return nil, errtrace.Wrap(err)
		}
		for _, h := range req.handlers {
			if err := h(ctx, c, res); err != nil {
				return nil, errtrace.Errorf("result handler failed: %w", err)
			}
		}
		if spec, err := LoadImageConfig(ctx, req.state); err != nil {
			return nil, errtrace.Wrap(err)
		} else if spec != nil {
			imageconfig, err = json.Marshal(spec)
			if err != nil {
				return nil, errtrace.Wrap(err)
			}
			res.AddMeta(exptypes.ExporterImageConfigKey, imageconfig)
		}
		return res, errtrace.Wrap(err)
	}, prog.Channel())
	if err != nil {
		return nil, errtrace.Errorf("solve failed: %w", err)
	}
	if len(imageconfig) > 0 {
		if res.ExporterResponse == nil {
			res.ExporterResponse = map[string]string{}
		}
		res.ExporterResponse[ExporterImageConfigKey] = string(imageconfig)
	}
	return res, nil
}
