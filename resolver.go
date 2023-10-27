package llblib

import (
	"context"
	"sync"

	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	bksess "github.com/moby/buildkit/session"
	"github.com/opencontainers/go-digest"
)

type resolver struct {
	cache *resolveImageCache
	cln   *client.Client
	sess  *bksess.Session
	prog  progress.Progress
}

func newResolver(cln *client.Client, cache *resolveImageCache, sess *bksess.Session, p progress.Progress) *resolver {
	return &resolver{
		cache: cache,
		cln:   cln,
		sess:  sess,
		prog:  p,
	}
}

func (r *resolver) ResolveImageConfig(ctx context.Context, ref string, opt llb.ResolveImageConfigOpt) (string, digest.Digest, []byte, error) {
	return r.cache.lookup(ctx, ref, opt, func() (string, digest.Digest, []byte, error) {
		opts := client.SolveOpt{}
		if r.sess != nil {
			opts.SharedSession = r.sess
			opts.SessionPreInitialized = true
		}
		var (
			d      digest.Digest
			config []byte
			err    error
		)
		_, buildErr := r.cln.Build(ctx, opts, "resolver", func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
			ref, d, config, err = c.ResolveImageConfig(ctx, ref, opt)
			return nil, nil
		}, r.prog.Channel())
		if buildErr != nil {
			return "", "", nil, buildErr
		}
		return ref, d, config, err
	})
}

type resolveImageCacheKey struct {
	resolveType llb.ResolverType
	ref         string
	os          string
	arch        string
	variant     string
	mode        string
	store       llb.ResolveImageConfigOptStore
}

type resolveImageCacheValue struct {
	ref      string
	digest   digest.Digest
	config   []byte
	err      error
	inflight chan struct{}
}

type resolveImageCache struct {
	mu    sync.Mutex
	cache map[resolveImageCacheKey]*resolveImageCacheValue
}

func (r *resolveImageCache) lookup(
	ctx context.Context,
	ref string,
	opt llb.ResolveImageConfigOpt,
	resolver func() (string, digest.Digest, []byte, error),
) (string, digest.Digest, []byte, error) {
	key := resolveImageCacheKey{
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

	r.mu.Lock()
	val, ok := r.cache[key]
	if !ok {
		val = &resolveImageCacheValue{
			inflight: make(chan struct{}),
		}
		r.cache[key] = val
	}
	r.mu.Unlock()
	if ok {
		return val.fetch(ctx)
	}
	val.store(resolver())
	return val.fetch(ctx)
}

func (v *resolveImageCacheValue) fetch(ctx context.Context) (string, digest.Digest, []byte, error) {
	select {
	case <-ctx.Done():
		return "", "", nil, ctx.Err()
	case <-v.inflight:
		return v.ref, v.digest, v.config, v.err
	}
}

func (v *resolveImageCacheValue) store(ref string, d digest.Digest, config []byte, err error) {
	v.ref = ref
	v.digest = d
	v.config = config
	v.err = err
	close(v.inflight)
}
