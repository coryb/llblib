package llblib_test

import (
	"runtime"
	"testing"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

var currentPlatform = ocispec.Platform{
	OS:           "linux",
	Architecture: runtime.GOARCH,
}

func TestCustomFrontend(t *testing.T) {
	r := newTestRunner(t)

	cachePaths := []string{
		"/root/.cache/go-build",
		"/go/pkg/mod",
	}

	// We are building our custom frontend from ./examples/custom-frontend,
	// within the same solve.
	build := llblib.Image("golang:1.22", llb.Platform(currentPlatform)).Run(
		llb.Args([]string{"go", "build", "-C", "/src", "-o", "/out/frontend", "./examples/custom-frontend"}),
		llb.AddMount("/out", llb.Scratch()),
		llblib.AddCacheMounts(cachePaths, "go-cache", llb.CacheMountPrivate),
		llb.AddMount("/src", r.Solver.Local(".", llb.IncludePatterns([]string{"**/*.go", "go.mod", "go.sum"}))),
		llb.AddEnv("CGO_ENABLED", "0"),
	).GetMount("/out")

	// Create the frontend with our binary.
	frontend := llb.Image("alpine", llb.Platform(currentPlatform)).File(
		llb.Copy(build, "/", "/"),
	).With(
		llblib.Entrypoint("/frontend"),
		llblib.AddLabel("moby.buildkit.frontend.caps", "moby.buildkit.frontend.inputs"),
	)

	st := llblib.Frontend(
		"image-loader",
		llblib.FrontendOpt("context:image-loader", "input:my-frontend"),
		llblib.FrontendInput("my-frontend", frontend),
		// options for our custom frontend
		llblib.FrontendOpt("custom:load-image", alpine),
	).With(
		// verify that the image we get back is alpine
		llblib.ApplyRun(llb.Args([]string{"test", "-f", "/etc/alpine-release"})),
	)
	req := r.Solver.Build(st)
	resp, err := r.Run(t, req)
	require.NoError(t, err)
	expectConfig(t, resp.ExporterResponse[llblib.ExporterImageConfigKey])
}
