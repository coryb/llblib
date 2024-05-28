// Package main demonstrates running a reasonably complex Go build with
// with persistent caching, and then using llblib.Download to export the results
// of the build to the local directory.
package main

import (
	"context"
	"log"
	"os"
	"runtime"

	"braces.dev/errtrace"
	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/errgroup"
)

func main() {
	setup := []string{}

	source := []string{"go.mod", "go.sum", "**/*.go"}
	steps := []string{
		"go mod download",
		"go build ./examples/build",
		"mount",
		"touch /scratch/foobar",
		"find /scratch",
	}

	localCwd, _ := os.Getwd()
	env := map[string]string{
		"CGO_ENABLED": "0",
		"GOPATH":      "/go", // for go mod cache
		"GOOS":        runtime.GOOS,
		"GOARCH":      runtime.GOARCH,
	}
	cacheID := "me"
	cachePaths := []string{
		"/root/.cache/go-build",
		"/go/pkg/mod",
	}
	platform := ocispec.Platform{OS: "linux", Architecture: runtime.GOARCH}

	// ----

	ctx := context.Background()
	cln, isMoby, err := llblib.NewClient(ctx, os.Getenv("BUILDKIT_HOST"))
	if err != nil {
		log.Fatalf("Failed to create client: %s", err)
	}

	slv := llblib.NewSolver(llblib.WithCwd(localCwd))

	root := llblib.ResolvedImage(
		"golang:1.20",
		llb.Platform(platform),
	).Dir("/")

	mounts := llblib.RunOptions{
		llb.AddMount("/scratch", llb.Scratch()),
		llb.AddMount("/local", slv.Local(".", llb.IncludePatterns([]string{"*"}), llb.ExcludePatterns([]string{"*"}))),
		llb.AddMount("/git", llb.Git("https://github.com/moby/buildkit.git", "baaf67ba976460a51ef198abab88baae376c32d8", llb.KeepGitDir())),
		llb.AddMount("/http", llb.HTTP("https://raw.githubusercontent.com/moby/buildkit/master/README.md", llb.Filename("README.md"), llb.Chmod(0o600))),
		llb.AddMount("/image", llblib.ResolvedImage(
			"busybox:latest",
			llb.Platform(platform),
		)),
	}

	for _, cmd := range setup {
		root = llblib.Run(root,
			llb.Shlex(cmd),
			llblib.AddEnvs(env),
			llblib.AddCacheMounts(cachePaths, cacheID, llb.CacheMountPrivate),
			mounts,
		).Root()
	}

	root = root.File(llb.Mkfile("/helper", 0o755, []byte("#!/bin/sh\necho hi\n")))

	workspace := slv.Local(".", llb.IncludePatterns(source))

	p := llblib.Persistent(root, mounts, llb.AddMount(localCwd, workspace))
	for _, step := range steps {
		p.Run(
			llb.Shlex(step),
			llb.Dir(localCwd),
			llblib.AddEnvs(env),
			llblib.AddCacheMounts(cachePaths, cacheID, llb.CacheMountPrivate),
		)
	}

	reqs := []llblib.Request{}
	changed, ok := p.GetMount(localCwd)
	if !ok {
		log.Panicf("mount for %s missing!", localCwd)
	}
	reqs = append(reqs,
		slv.Build(
			llb.Diff(workspace, changed),
			llblib.WithLabel("my-build"),
			llblib.Download("."),
		),
	)

	prog := progress.NewProgress()
	defer prog.Close()

	sess, err := slv.NewSession(ctx, cln, prog, isMoby)
	if err != nil {
		log.Panicf("failed to create session: %+v", err)
	}
	defer sess.Release()

	var eg errgroup.Group
	for _, r := range reqs {
		r := r
		eg.Go(func() error {
			_, err := sess.Do(ctx, r)
			return errtrace.Wrap(err)
		})
	}
	err = eg.Wait()
	if err != nil {
		log.Panicf("solve failed: %+v", err)
	}
}
