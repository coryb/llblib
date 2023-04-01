package llblib

import (
	"context"
	"sync"

	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

type resolveResponse struct {
	digest digest.Digest
	config []byte
	err    error
}

type resolveRequest struct {
	ref      string
	opt      llb.ResolveImageConfigOpt
	response chan<- resolveResponse
}

func (s *solver) Resolver() llb.ImageMetaResolver {
	if s.resolver != nil {
		return s.resolver
	}
	s.resolver = &resolver{}
	return s.resolver
}

type cacheKey struct {
	resolveType llb.ResolverType
	ref         string
	os          string
	arch        string
	variant     string
	mode        string
	store       llb.ResolveImageConfigOptStore
}

type cacheValue struct {
	digest digest.Digest
	config []byte
}

type resolver struct {
	resolveCh chan resolveRequest
	done      chan error
	mu        sync.Mutex
	cacheMu   sync.RWMutex
	cache     map[cacheKey]cacheValue
}

func (r *resolver) start(ctx context.Context, cln *client.Client, p progress.Progress) error {
	r.mu.Lock()
	if r.resolveCh != nil || r.done != nil {
		return errors.New("resolver started multiple times")
	}
	r.resolveCh = make(chan resolveRequest)
	r.done = make(chan error)
	r.mu.Unlock()

	go func() {
		_, err := cln.Build(ctx, client.SolveOpt{}, "resolver", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			for req := range r.resolveCh {
				req := req
				go func() {
					key := cacheKey{
						resolveType: req.opt.ResolverType,
						ref:         req.ref,
						mode:        req.opt.ResolveMode,
						store:       req.opt.Store,
					}
					if req.opt.Platform != nil {
						key.os = req.opt.Platform.OS
						key.arch = req.opt.Platform.Architecture
						key.variant = req.opt.Platform.Variant
					}
					r.cacheMu.RLock()
					val, ok := r.cache[key]
					r.cacheMu.RUnlock()
					if ok {
						req.response <- resolveResponse{
							digest: val.digest,
							config: val.config,
						}
					}
					digest, config, err := c.ResolveImageConfig(ctx, req.ref, req.opt)
					if err != nil {
						r.cacheMu.Lock()
						r.cache[key] = cacheValue{
							digest: digest,
							config: config,
						}
						r.cacheMu.Unlock()
					}
					select {
					case <-ctx.Done():
						close(req.response)
					case req.response <- resolveResponse{digest: digest, config: config, err: err}:
					}
				}()
			}
			return nil, nil
		}, p.Channel())
		if err != nil {
			r.done <- errors.Wrap(err, "resolver build failed")
		}
		close(r.done)
	}()
	return nil
}

func (r *resolver) stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	close(r.resolveCh)
	err := <-r.done
	r.resolveCh = nil
	r.done = nil
	return err
}

func (r *resolver) ResolveImageConfig(ctx context.Context, ref string, opt llb.ResolveImageConfigOpt) (digest.Digest, []byte, error) {
	respCh := make(chan resolveResponse)
	req := resolveRequest{
		ref:      ref,
		opt:      opt,
		response: respCh,
	}
	r.mu.Lock()
	if r.resolveCh == nil {
		r.mu.Unlock()
		return "", nil, errors.New("resolver not running")
	}
	r.resolveCh <- req
	r.mu.Unlock()

	select {
	case <-ctx.Done():
		return "", nil, ctx.Err()
	case resp, ok := <-respCh:
		if !ok {
			return "", nil, errors.New("resolver did not respond")
		}
		return resp.digest, resp.config, resp.err
	}
}
