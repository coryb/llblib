package llblib_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/coryb/llblib"
	"github.com/coryb/llblib/progress"
	"github.com/moby/buildkit/client"
)

type runnerOpts struct {
	timeout time.Duration
}

type runnerOption func(ro *runnerOpts)

func withTimeout(d time.Duration) runnerOption {
	return func(ro *runnerOpts) {
		ro.timeout = d
	}
}

type testRunner struct {
	T        *testing.T
	Context  context.Context
	Client   *client.Client
	Solver   llblib.Solver
	Progress progress.Progress
}

func newTestRunner(t *testing.T, opts ...runnerOption) testRunner {
	t.Helper()

	ro := runnerOpts{
		timeout: 60 * time.Second,
	}
	for _, opt := range opts {
		opt(&ro)
	}

	ctx, cancel := context.WithTimeout(context.Background(), ro.timeout)
	t.Cleanup(cancel)

	cln, err := llblib.NewClient(ctx, os.Getenv("BUILDKIT_HOST"))
	if err != nil {
		t.Fatalf("Failed to create client: %s", err)
	}
	t.Cleanup(func() {
		cln.Close()
	})

	var buf bytes.Buffer
	prog := progress.NewProgress(progress.WithOutput(&buf))
	t.Cleanup(func() {
		prog.Close()
		t.Helper()
		t.Logf("build output:\n%s", buf.String())
	})

	return testRunner{
		Context:  ctx,
		Client:   cln,
		Solver:   llblib.NewSolver(),
		Progress: prog,
	}
}

func (r testRunner) Run(t *testing.T, req llblib.Request) error {
	t.Helper()
	_, err := r.Session(t).Do(r.Context, req)
	return err
}

func (r testRunner) Session(t *testing.T) llblib.Session {
	t.Helper()
	sess, err := r.Solver.NewSession(r.Context, r.Client, r.Progress)
	if err != nil {
		t.Fatalf("failed to create session: %s", err)
	}
	t.Cleanup(func() {
		sess.Release()
	})
	return sess
}
