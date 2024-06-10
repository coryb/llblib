package llblib_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	"github.com/stretchr/testify/require"
)

func TestFromDefinition(t *testing.T) {
	t.Parallel()
	tdir := t.TempDir()

	r := newTestRunner(t, withTimeout(10*time.Minute))

	def, err := llblib.MarshalWithImageConfig(
		context.Background(),
		buildExample(r, llb.AddEnv("GOARCH", "amd64")),
	)
	require.NoError(t, err)

	_, err = r.Run(t, r.Solver.Build(
		llblib.BuildDefinition(def),
		llblib.Download(filepath.Join(tdir, "build", "amd64")),
		llblib.WithLabel("linux/amd64"),
	))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(tdir, "build", "amd64", "build"))
	require.NoError(t, err)
	out, err := exec.Command("go", "version", "-m", filepath.Join(tdir, "build", "amd64", "build")).CombinedOutput()
	require.NoError(t, err)
	require.Contains(t, string(out), "GOARCH=amd64")
}
