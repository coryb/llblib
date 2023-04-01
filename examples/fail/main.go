package main

import (
	"context"
	"log"
	"os"

	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client/llb"
)

func main() {
	ctx := context.Background()
	cli, err := llblib.NewClient(ctx, os.Getenv("BUILDKIT_HOST"))
	if err != nil {
		log.Fatalf("Failed to create client: %s", err)
	}

	slv := llblib.NewSolver()

	root := llb.Image("golang:1.20", llb.LinuxArm64).Dir("/").File(llb.Mkdir("/scratch", 0o777))

	mounts := llblib.RunOptions{
		llb.AddMount("/scratch", llb.Scratch()),
		llb.AddMount("/src", slv.Local(".")),
	}

	p := llblib.Persistent(root, mounts)
	p.Run(llb.Shlex("touch /scratch/scratch-foobar"), llblib.IgnoreCache())
	p.Run(llb.Shlex("touch /src/src-foobar"), llblib.IgnoreCache())
	p.Run(llb.Shlex("false")) // <- trigger /bin/bash on error

	req := slv.Build(p.GetMount("/scratch"),
		llblib.Download("."),
		llblib.OnError(
			llblib.WithTTY(os.Stdin, os.Stdout, os.Stderr),
			llblib.WithRun(llb.Args([]string{"/bin/bash"})),
		),
	)

	prog := progress.NewProgress()
	defer prog.Release()

	sess, err := slv.NewSession(ctx, cli, prog)
	if err != nil {
		log.Panicf("failed to create session: %+v", err)
	}
	defer sess.Release()

	_, err = sess.Do(ctx, req)
	if err != nil {
		log.Panicf("solve failed: %+v", err)
	}
}
