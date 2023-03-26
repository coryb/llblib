package main

import (
	"context"
	"log"
	"os"
	"runtime"

	"github.com/coryb/llblib"
	"github.com/moby/buildkit/client/llb"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func main() {
	setup := []string{
		"apk add -U go",
	}

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
	platform := ocispecs.Platform{OS: "linux", Architecture: runtime.GOARCH}

	// ----

	ctx := context.Background()
	cli, err := llblib.NewClient(ctx, os.Getenv("BUILDKIT_HOST"))
	if err != nil {
		log.Fatalf("Failed to create client: %s", err)
	}

	sess := llblib.NewSession(localCwd)

	root := llb.Image("alpine:latest@sha256:e2e16842c9b54d985bf1ef9242a313f36b856181f188de21313820e177002501", llb.Platform(platform)).Dir("/")

	mounts := llblib.RunOptions{
		llb.AddMount("/scratch", llb.Scratch()),
		llb.AddMount("/local", sess.Local(".", llb.IncludePatterns([]string{"*"}), llb.ExcludePatterns([]string{"*"}))),
		llb.AddMount("/git", llb.Git("https://github.com/moby/buildkit.git", "baaf67ba976460a51ef198abab88baae376c32d8", llb.KeepGitDir())),
		llb.AddMount("/http", llb.HTTP("https://raw.githubusercontent.com/moby/buildkit/master/README.md", llb.Filename("README.md"), llb.Chmod(0o600))),
		llb.AddMount("/image", llb.Image("busybox:latest@sha256:acaddd9ed544f7baf3373064064a51250b14cfe3ec604d65765a53da5958e5f5", llb.Platform(platform))),
	}

	for _, cmd := range setup {
		root = root.Run(
			llb.Shlex(cmd),
			llblib.AddEnvs(env),
			llblib.AddCacheMounts(cachePaths, cacheID, llb.CacheMountPrivate),
			mounts,
		).Root()
	}

	root = root.File(llb.Mkfile("/helper", 0o755, []byte("#!/bin/sh\necho hi\n")))

	workspace := sess.Local(".", llb.IncludePatterns(source))

	p := llblib.Persistent(root, mounts, llb.AddMount(localCwd, workspace))
	for _, step := range steps {
		p = p.Run(
			llb.Shlex(step),
			llb.Dir(localCwd),
			llblib.AddEnvs(env),
			llblib.AddCacheMounts(cachePaths, cacheID, llb.CacheMountPrivate),
		)
	}

	sess.Download(llb.Diff(workspace, p.GetMount(localCwd)), ".")

	err = sess.Execute(ctx, cli)
	if err != nil {
		log.Fatalf("solve failed: %+v", err)
	}
}
