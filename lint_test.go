package llblib_test

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	specsv1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

const GolangCILintVersion = "v1.58.1"

func TestLint(t *testing.T) {
	t.Parallel()
	// 5m timeout b/c github actions can be slow to pull the golangci image
	r := newTestRunner(t, withTimeout(300*time.Second))

	cwd, err := os.Getwd()
	require.NoError(t, err)

	currentPlatform := specsv1.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	sources := r.Solver.Local(".", llb.IncludePatterns([]string{
		"**/*.go",
		"examples/fail/differ.sh", // <- for go:embed
		"go.mod",
		"go.sum",
		".golangci.yaml",
	}))

	st := llblib.ResolvedImage(
		"golangci/golangci-lint:"+GolangCILintVersion,
		llb.Platform(currentPlatform),
	).Run(
		llb.Args([]string{"golangci-lint", "run", "--timeout", "3m", "--fast"}),
		// ensure go mod cache location is in our persistent cache dir
		llb.AddEnv("GOMODCACHE", "/root/.cache/go-mod"),
		llb.Dir(cwd),
		llb.AddMount(cwd, sources),
		llb.AddMount(
			"/root/.cache",
			llb.Scratch(),
			llb.AsPersistentCacheDir(
				"golangci-lint:"+GolangCILintVersion,
				llb.CacheMountShared,
			),
		),
	).Root()

	err = r.Run(t, r.Solver.Build(st))
	require.NoError(t, err)
}
