// Package main demonstrates using llblib.Forward to allow the buildkit solve
// to connect to a service running on your local host.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"

	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func main() {
	platform := ocispec.Platform{OS: "linux", Architecture: runtime.GOARCH}
	localCwd, _ := os.Getwd()

	// ----

	ctx := context.Background()
	cli, isMoby, err := llblib.NewClient(ctx, os.Getenv("BUILDKIT_HOST"))
	if err != nil {
		log.Fatalf("Failed to create client: %s", err)
	}

	slv := llblib.NewSolver(llblib.WithCwd(localCwd))

	root := llblib.Image("alpine:latest@sha256:e2e16842c9b54d985bf1ef9242a313f36b856181f188de21313820e177002501", llb.Platform(platform)).Dir("/")
	root = llblib.Run(root, llb.Shlex("apk add -U curl socat")).Root()

	req := slv.Build(
		llblib.Run(root,
			llb.Shlex("curl -sf --unix /tmp/forward.sock -v http://unix -o /tmp/special"),
			slv.Forward("tcp://127.0.0.1:1234", "/tmp/forward.sock"),
			llblib.IgnoreCache,
		).Run(
			llb.Args([]string{"sh", "-c", `test "$(cat /tmp/special)" = "message-from-host"`}),
		).Root(),
	)

	var wg sync.WaitGroup
	defer wg.Wait()

	wg.Add(1)
	go func() {
		defer wg.Done()
		http.ListenAndServe("127.0.0.1:1234", http.HandlerFunc(
			func(rw http.ResponseWriter, r *http.Request) {
				rw.WriteHeader(http.StatusOK)
				rw.Write([]byte("message-from-host"))
			}),
		)
	}()

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
