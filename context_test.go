package llblib

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithEnv(t *testing.T) {
	ctx := context.Background()

	// verify missing env condition
	require.Equal(t, "", Getenv(ctx, "FOO"))
	got, ok := LookupEnv(ctx, "FOO")
	require.False(t, ok)
	require.Equal(t, "", got)

	// verify fallback to os env
	os.Setenv("FOO", "BAR")
	require.Equal(t, "BAR", Getenv(ctx, "FOO"))
	got, ok = LookupEnv(ctx, "FOO")
	require.True(t, ok)
	require.Equal(t, "BAR", got)

	// verify context env, and os env unmodified
	ctx = WithEnv(ctx, "FOO", "BAZ")
	require.Equal(t, os.Getenv("FOO"), "BAR")
	require.Equal(t, "BAZ", Getenv(ctx, "FOO"))
	got, ok = LookupEnv(ctx, "FOO")
	require.True(t, ok)
	require.Equal(t, "BAZ", got)
}
