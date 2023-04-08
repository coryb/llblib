package llblib_test

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// there is a 3s sleep in (*Client).solve to ensure the sessions are
		// closed on errors, even if using SharedSession
		goleak.IgnoreTopFunction("github.com/moby/buildkit/client.(*Client).solve.func2.1.1"),
	)
}
