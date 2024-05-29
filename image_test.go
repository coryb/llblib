package llblib

import (
	"context"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/imagemetaresolver"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

// mockResolver1 is a mock sourceresolver.ImageMetaResolver that is implemented
// with a value receiver to test some edge cases with the reflection logic in
// imageResolverOption.
type mockResolver1 struct{}

func (mockResolver1) ResolveImageConfig(ctx context.Context, ref string, opt sourceresolver.Opt) (string, digest.Digest, []byte, error) {
	return "", "", nil, nil
}

// mockResolver2 is a mock sourceresolver.ImageMetaResolver that is implemented
// with a pointer receiver to test some edge cases with the reflection logic in
// imageResolverOption.  We can also verify that our resolver is called by
// looking at the resolved value.
type mockResolver2 struct {
	resolved string
}

func (r *mockResolver2) ResolveImageConfig(ctx context.Context, ref string, opt sourceresolver.Opt) (string, digest.Digest, []byte, error) {
	r.resolved = ref
	return "", "", nil, nil
}

func TestImageResolverOption(t *testing.T) {
	ctx := context.Background()

	// verify imageResolverOption with value receiver resolver
	var resolverA sourceresolver.ImageMetaResolver = mockResolver1{}
	got := imageResolverOption(ctx, llb.WithMetaResolver(resolverA))
	_, _, _, err := got.ResolveImageConfig(ctx, "test", sourceresolver.Opt{})
	require.NoError(t, err)
	require.Equal(t, resolverA, got)

	// verify imageResolverOption with pointer to value receiver resolver
	resolverB := &mockResolver1{}
	got = imageResolverOption(ctx, llb.WithMetaResolver(resolverB))
	_, _, _, err = got.ResolveImageConfig(ctx, "test", sourceresolver.Opt{})
	require.NoError(t, err)
	require.Equal(t, resolverB, got)

	// verify imageResolverOption with pointer receiver resolver
	resolverC := &mockResolver2{}
	got = imageResolverOption(ctx, llb.WithMetaResolver(resolverC))
	require.Equal(t, resolverC, got)
	_, _, _, err = got.ResolveImageConfig(ctx, "test", sourceresolver.Opt{})
	require.NoError(t, err)
	require.Equal(t, "test", resolverC.resolved)

	// verify imageResolverOption with "real" default resolver, we will actually
	// resolve the image here.
	resolverD := imagemetaresolver.Default()
	got = imageResolverOption(ctx, llb.WithMetaResolver(resolverD))
	require.Equal(t, resolverD, got)
	_, _, _, err = got.ResolveImageConfig(ctx, "docker.io/library/busybox:latest", sourceresolver.Opt{})
	require.NoError(t, err)

	// verify imageResolverOption with our capturing resolver, wrapping the
	// "real" default resolver, we will also verify the config is captured
	// after resolving.
	resolverE := &capturingMetaResolver{
		resolver: imagemetaresolver.Default(),
	}
	got = imageResolverOption(ctx, llb.WithMetaResolver(resolverE))
	require.Equal(t, resolverE, got)
	_, _, cfg, err := got.ResolveImageConfig(ctx, "docker.io/library/busybox:latest", sourceresolver.Opt{})
	require.NoError(t, err)
	require.Equal(t, resolverE.config, cfg)
}
