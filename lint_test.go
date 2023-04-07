package llblib_test

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client/llb"
	specsv1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const GolangCILintVersion = "v1.51.2"

type testWriter struct {
	t *testing.T
}

func (tw testWriter) Write(p []byte) (n int, err error) {
	tw.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func TestLint(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	cln, err := llblib.NewClient(ctx, "")
	if err != nil {
		t.Errorf("Failed to create client: %s", err)
	}
	t.Cleanup(func() {
		cln.Close()
	})

	cwd, _ := os.Getwd()
	currentPlatform := specsv1.Platform{
		OS:           "linux",
		Architecture: runtime.GOARCH,
	}

	slv := llblib.NewSolver()

	sources := slv.Local(".", llb.IncludePatterns([]string{
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
		llb.Args([]string{"golangci-lint", "run"}),
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

	req := slv.Build(st)

	prog := progress.NewProgress(progress.WithOutput(&testWriter{t}))
	t.Cleanup(func() {
		prog.Close()
	})

	sess, err := slv.NewSession(ctx, cln, prog)
	if err != nil {
		t.Errorf("failed to create session: %s", err)
	}
	t.Cleanup(func() {
		sess.Release()
	})
	_, err = sess.Do(ctx, req)
	if err != nil {
		t.Errorf("solve failed: %s", err)
	}
}
