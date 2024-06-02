// Package main demonstrates how to use llblib.Dockerfile to solve a Dockerfile
// This is roughly equivalent to running `docker build .` where the Dockerfile
// is using `#syntax docker/dockerfile`.
package main

import (
	"context"
	"log"
	"os"
	"runtime"

	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func main() {
	ctx := context.Background()
	cli, isMoby, err := llblib.NewClient(ctx, os.Getenv("BUILDKIT_HOST"))
	if err != nil {
		log.Fatalf("Failed to create client: %s", err)
	}

	slv := llblib.NewSolver()

	p := ocispec.Platform{OS: "linux", Architecture: runtime.GOARCH}

	dockerfile := `
		FROM alpine
		COPY / /
	`
	context := llb.Scratch().File(llb.Mkfile("foobar", 0o644, []byte("something")))

	root := llblib.Dockerfile([]byte(dockerfile), context,
		llblib.WithTargetPlatform(p),
	)

	req := slv.Container(root,
		llblib.WithRun(llb.Shlex("/bin/sh")),
		llblib.WithTTY(os.Stdin, os.Stdout, os.Stderr),
	)

	prog := progress.NewProgress()
	defer prog.Close()

	sess, err := slv.NewSession(ctx, cli, prog, isMoby)
	if err != nil {
		log.Panicf("failed to create session: %+v", err)
	}
	defer sess.Release()

	_, err = sess.Do(ctx, req)
	if err != nil {
		log.Panicf("build failed: %+v", err)
	}
}
