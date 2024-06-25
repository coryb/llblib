package llblib_test

import (
	"context"
	"testing"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/stretchr/testify/require"
)

func TestResultHandler(t *testing.T) {
	r := newTestRunner(t)

	st := llb.Scratch().File(
		llb.Mkfile("foo", 0o644, []byte("bar")),
	)

	var fooContent []byte
	req := r.Solver.Build(st, llblib.WithResultHandler(func(ctx context.Context, c gateway.Client, res *gateway.Result) error {
		ref, err := res.SingleRef()
		if err != nil {
			return err
		}
		fooContent, err = ref.ReadFile(ctx, gateway.ReadRequest{
			Filename: "foo",
		})
		if err != nil {
			return err
		}
		return nil
	}))
	_, err := r.Run(t, req)
	require.NoError(t, err)
	require.Equal(t, "bar", string(fooContent))
}
