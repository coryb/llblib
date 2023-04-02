package llblib

import (
	"context"

	"github.com/moby/buildkit/client/llb"
)

func ResolvedImage(ref string, opts ...llb.ImageOption) llb.State {
	return llb.Scratch().Async(func(ctx context.Context, _ llb.State, c *llb.Constraints) (llb.State, error) {
		if resolver := LoadImageResolver(ctx); resolver != nil {
			opts = append(opts,
				llb.WithMetaResolver(resolver),
				llb.ResolveDigest(true),
			)
			return llb.Image(ref, opts...), nil
		}
		return llb.Image(ref, opts...), nil
	})
}
