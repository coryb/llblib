package llblib

import (
	"context"

	"github.com/moby/buildkit/client/llb"
	"github.com/opencontainers/go-digest"
)

// Digest returns the digest for the state.
func Digest(st llb.State) (digest.Digest, error) {
	ctx := context.Background()
	c := &llb.Constraints{}
	dgst, _, _, _, err := st.Output().Vertex(ctx, c).Marshal(ctx, c)
	return dgst, err
}
