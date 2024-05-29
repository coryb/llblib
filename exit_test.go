package llblib_test

import (
	"testing"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	gwpb "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/stretchr/testify/require"
)

func TestExitError(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t)

	if _, err := r.Client.Info(r.Context); llblib.ErrUnimplemented(err) {
		// Info API added in v0.11 and the errdefs serialization for ExitError
		// was added in v0.10. This isn't perfect, but close enough.
		t.Skip("buildkit server too old")
	}

	req := r.Solver.Build(
		llblib.ResolvedImage("busybox", llb.LinuxAmd64).Run(
			llb.Args([]string{"/bin/sh", "-c", "exit 99"}),
		).Root(),
	)

	_, err := r.Run(t, req)
	var exitError *gwpb.ExitError
	require.ErrorAs(t, err, &exitError)
	require.Equal(t, uint32(99), exitError.ExitCode)
}
