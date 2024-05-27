package llblib_test

import (
	"bufio"
	"context"
	"io"
	"os"
	"testing"
	"time"

	"braces.dev/errtrace"
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

	w := newTestWriter(t)
	prog := progress.NewProgress(progress.WithOutput(w))
	t.Cleanup(func() {
		prog.Close()
		w.Close()
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
	return errtrace.Wrap(err)
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

type tWriter struct {
	testing.TB
	reader *io.PipeReader
	writer *io.PipeWriter
	done   chan struct{}
}

var _ io.WriteCloser = (*tWriter)(nil)

func newTestWriter(t testing.TB) io.WriteCloser {
	t.Helper()
	r, w := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			t.Log(scanner.Text())
		}
	}()
	return &tWriter{TB: t, reader: r, writer: w, done: done}
}

func (w *tWriter) Close() error {
	w.writer.Close()
	<-w.done
	return errtrace.Wrap(w.reader.Close())
}

func (w tWriter) Write(p []byte) (n int, err error) {
	return errtrace.Wrap2(w.writer.Write(p))
}
