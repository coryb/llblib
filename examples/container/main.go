package main

import (
	"context"
	"log"
	"os"
	"runtime"

	"github.com/containerd/console"
	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client/llb"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func main() {
	localCwd, _ := os.Getwd()
	platform := ocispecs.Platform{OS: "linux", Architecture: runtime.GOARCH}

	// ----

	ctx := context.Background()
	cli, err := llblib.NewClient(ctx, os.Getenv("BUILDKIT_HOST"))
	if err != nil {
		log.Fatalf("Failed to create client: %s", err)
	}

	slv := llblib.NewSolver(llblib.WithCwd(localCwd))
	root := llb.Image("alpine:latest@sha256:e2e16842c9b54d985bf1ef9242a313f36b856181f188de21313820e177002501", llb.Platform(platform)).Dir("/")
	mounts := llblib.RunOptions{
		llb.AddMount("/scratch", llb.Scratch()),
		llb.AddMount("/local", slv.Local(".", llb.IncludePatterns([]string{"*"}), llb.ExcludePatterns([]string{"*"}))),
		llb.AddMount("/git", llb.Git("https://github.com/moby/buildkit.git", "baaf67ba976460a51ef198abab88baae376c32d8", llb.KeepGitDir())),
		llb.AddMount("/http", llb.HTTP("https://raw.githubusercontent.com/moby/buildkit/master/README.md", llb.Filename("README.md"), llb.Chmod(0o600))),
		llb.AddMount("/image", llb.Image("busybox:latest@sha256:acaddd9ed544f7baf3373064064a51250b14cfe3ec604d65765a53da5958e5f5", llb.Platform(platform))),
		slv.AddSecretFile("go.mod", "/secret/go.mod"),
	}

	req := slv.Container(root,
		llblib.WithRun(llb.Shlex("/bin/sh"), mounts),
		llblib.WithTTY(os.Stdin, os.Stdout, os.Stderr),
	)

	prog := progress.NewProgress(progress.WithConsole(console.Current()))
	defer prog.Release()

	sess, err := slv.NewSession(ctx, cli)
	if err != nil {
		log.Panicf("failed to create session: %+v", err)
	}
	defer sess.Release()

	_, err = sess.Do(ctx, req, prog)
	if err != nil {
		log.Panicf("build failed: %+v", err)
	}
}
