package llblib

import (
	"context"

	"github.com/moby/buildkit/client/llb"
)

// ResolvedImage returns an llb.State where the image will be resolved with the
// image configuration applied to the state.  The resolved image digest will
// also be applied to the state to ensure this state is always consistent
// during the solve execution.  Note that `WithImageResolver` should be used
// on the context provided to the buildkit solve requests, otherwise an
// un-resolved llb.Image will be used.
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
