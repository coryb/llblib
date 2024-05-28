// Package main demonstrates llblib.OnError handling to run a command in the
// modified state of a failed solve request.
package main

import (
	"context"
	_ "embed"
	"log"
	"os"

	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client/llb"
)

//go:embed differ.sh
var differScript []byte

func main() {
	ctx := context.Background()
	cli, isMoby, err := llblib.NewClient(ctx, os.Getenv("BUILDKIT_HOST"))
	if err != nil {
		log.Fatalf("Failed to create client: %s", err)
	}

	slv := llblib.NewSolver()

	root := llblib.Image("golang:1.20", llb.LinuxArm64).Dir("/").File(llb.Mkdir("/scratch", 0o777))

	mounts := llblib.RunOptions{
		llb.AddMount("/scratch", llb.Scratch()),
		llb.AddMount("/src", slv.Local(".")),
	}

	p := llblib.Persistent(root, mounts)
	p.Run(llb.Shlex("touch /scratch/scratch-foobar"), llblib.IgnoreCache())
	p.Run(llb.Shlex("touch /src/src-foobar"), llblib.IgnoreCache())
	p.Run(llb.Shlex("false")) // <- trigger /bin/bash on error

	scratch, ok := p.GetMount("/scratch")
	if !ok {
		log.Panic("mount for /scratch missing!")
	}

	req := slv.Build(scratch,
		llblib.Download("."),
		llblib.OnError(
			llblib.WithTTY(os.Stdin, os.Stdout, os.Stderr),
			llblib.WithRun(
				// swap out golang with busybox but preserve /scratch and /src
				llb.AddMount("/",
					llb.Image("busybox", llb.LinuxArm64).File(
						llb.Mkfile("/differ.sh", 0o755, differScript),
					),
				),
				llb.AddMount("/original", slv.Local(".")),
				llb.AddMount("/output", llb.Scratch()),
				// run example differ, this find changes between /original
				// and the modified /src directory and copies them to /output
				llb.Args([]string{"/differ.sh", "/original", "/src", "/output"}),
			),
		),
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
		log.Panicf("solve failed: %+v", err)
	}
}
