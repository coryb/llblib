// Package main demonstrates using llblib.OnError on get a shell in the modified
// state of a failed docker build using a request built with llblib.Frontend.
package main

import (
	"context"
	"log"
	"os"

	"github.com/containerd/console"
	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client/llb"
)

func main() {
	ctx := context.Background()
	cli, isMoby, err := llblib.NewClient(ctx, os.Getenv("BUILDKIT_HOST"))
	if err != nil {
		log.Fatalf("Failed to create client: %s", err)
	}

	slv := llblib.NewSolver()

	dockerfile := llb.Scratch().File(llb.Mkfile("Dockerfile", 0o644, []byte(`
		FROM alpine
		COPY / /
		RUN touch /tmp/foobar
		RUN false # <- this will fail, causing /bin/sh to run
	`)))
	context := llb.Scratch().File(llb.Mkfile("foobar", 0o644, []byte("something")))

	root := llblib.Frontend("docker/dockerfile",
		llblib.FrontendInput("context", context),
		llblib.FrontendInput("dockerfile", dockerfile),
	)

	req := slv.Build(root, llblib.OnError(
		llblib.WithRun(llb.Shlex("/bin/sh")),
		llblib.WithTTY(os.Stdin, os.Stdout, os.Stderr),
	))

	prog := progress.NewProgress(progress.WithOutput(console.Current()))
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
