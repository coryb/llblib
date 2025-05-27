package llblib_test

import (
	"runtime"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

const GolangCILintVersion = "v1.64.6"

func goSource(s llblib.Solver) llb.State {
	return s.Local(".", llb.IncludePatterns([]string{
		"**/*.go",
		"examples/fail/differ.sh", // <- for go:embed
		"go.mod",
		"go.sum",
		".golangci.yaml",
	}))
}

func TestLint(t *testing.T) {
	t.Parallel()
	// 5m timeout b/c github actions can be slow to pull the golangci image
	r := newTestRunner(t, withTimeout(300*time.Second))

	currentPlatform := ocispec.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	st := llblib.ResolvedImage(
		"golangci/golangci-lint:"+GolangCILintVersion,
		llb.Platform(currentPlatform),
	).Run(
		llb.Args([]string{"golangci-lint", "run", "--timeout", "9m"}),
		// ensure go mod cache location is in our persistent cache dir
		llb.AddEnv("GOMODCACHE", "/root/.cache/go-mod"),
		llb.Dir(r.WorkDir),
		llb.AddMount(r.WorkDir, goSource(r.Solver)),
		llb.AddMount(
			"/root/.cache",
			llb.Scratch(),
			llb.AsPersistentCacheDir(
				"golangci-lint:"+GolangCILintVersion,
				llb.CacheMountShared,
			),
		),
	).Root()

	_, err := r.Run(t, r.Solver.Build(st))
	require.NoError(t, err)
}
