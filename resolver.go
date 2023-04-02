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
	digest   digest.Digest
	config   []byte
	err      error
	inflight chan struct{}
}

type resolver struct {
	resolveCh chan resolveRequest
	done      chan error
	mu        sync.Mutex
	cacheMu   sync.RWMutex
	cache     map[cacheKey]*cacheValue
}

func newResolver(ctx context.Context, cln *client.Client, p progress.Progress) *resolver {
	r := &resolver{
		cache:     map[cacheKey]*cacheValue{},
		resolveCh: make(chan resolveRequest),
		done:      make(chan error),
	}

	go func() {
		_, err := cln.Build(ctx, client.SolveOpt{}, "resolver", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			for req := range r.resolveCh {
				req := req
				go func() {
					digest, config, err := c.ResolveImageConfig(ctx, req.ref, req.opt)
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
	return r
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
	key := cacheKey{
		resolveType: opt.ResolverType,
		ref:         ref,
		mode:        opt.ResolveMode,
		store:       opt.Store,
	}
	if opt.Platform != nil {
		key.os = opt.Platform.OS
		key.arch = opt.Platform.Architecture
		key.variant = opt.Platform.Variant
	}
	r.cacheMu.RLock()
	val, ok := r.cache[key]
	r.cacheMu.RUnlock()
	if ok {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-val.inflight:
			return val.digest, val.config, val.err
		}
	}

	r.cacheMu.Lock()
	// double check now that we have the write lock that someone
	// didnt beat us to it
	waiting := false
	val, ok = r.cache[key]
	if ok {
		// someone beat us, we should wait
		waiting = true
	} else {
		val = &cacheValue{
			inflight: make(chan struct{}),
		}
		r.cache[key] = val
	}
	r.cacheMu.Unlock()
	if waiting {
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-val.inflight:
			return val.digest, val.config, val.err
		}
	}

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
		val.digest = resp.digest
		val.config = resp.config
		val.err = resp.err
		close(val.inflight)
		return resp.digest, resp.config, resp.err
	}
}
