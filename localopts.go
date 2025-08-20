package llblib

import (
	"io"

	"github.com/moby/buildkit/client/llb"
)

// ContentHashSentinel is the sentinel value used internally to indicate
// that content-based hashing should be used for local directories.
const ContentHashSentinel = "LLBLIB_CONTENT_HASH_ENABLED"

// TracedContentHashSentinel is the sentinel value used internally to indicate
// that traced content-based hashing should be used for local directories.
const TracedContentHashSentinel = "LLBLIB_TRACED_CONTENT_HASH_ENABLED"

// withContentHashOption implements llb.LocalOption to enable content-based hashing
// instead of modification time for localUniqueID calculation.
type withContentHashOption struct{}

// SetLocalOption implements llb.LocalOption interface
func (withContentHashOption) SetLocalOption(li *llb.LocalInfo) {
	// Set LocalUniqueID to a sentinel value that we'll detect and override
	// during hash computation in localUniqueID function
	li.LocalUniqueID = ContentHashSentinel
}

// WithContentHash is a LocalOption that enables content-based hashing for the
// local directory instead of using modification time. This provides more
// accurate change detection but is slower for large directories.
//
// Example usage:
//
//	solver.Local("src", llb.IncludePatterns([]string{"**/*.go"}), llblib.WithContentHash)
var WithContentHash llb.LocalOption = withContentHashOption{}

// withTracedContentHashOption implements llb.LocalOption to enable traced content-based hashing
// for debugging what inputs are being hashed.
type withTracedContentHashOption struct {
	output io.Writer
}

// SetLocalOption implements llb.LocalOption interface
func (t withTracedContentHashOption) SetLocalOption(li *llb.LocalInfo) {
	// Set LocalUniqueID to the traced sentinel value
	// The actual io.Writer will be extracted by inspecting the original options
	li.LocalUniqueID = TracedContentHashSentinel
}

// WithTracedContentHash returns a LocalOption that enables traced content-based hashing
// for debugging. All hashed inputs (file paths, modes, content chunks) are written
// to the provided io.Writer for analysis.
//
// Example usage:
//
//	var trace bytes.Buffer
//	solver.Local("src", llb.IncludePatterns([]string{"**/*.go"}), llblib.WithTracedContentHash(&trace))
//	fmt.Print(trace.String()) // See what was hashed
func WithTracedContentHash(output io.Writer) llb.LocalOption {
	return withTracedContentHashOption{output: output}
}

// extractTracedWriter looks through the LocalOptions to find a withTracedContentHashOption
// and returns the last io.Writer found, or nil if none found
func extractTracedWriter(opts ...llb.LocalOption) io.Writer {
	var lastWriter io.Writer
	for _, opt := range opts {
		if tracedOpt, ok := opt.(withTracedContentHashOption); ok {
			lastWriter = tracedOpt.output
		}
	}
	return lastWriter
}

// isTracedContentHash checks if the unique ID indicates traced content hashing
func isTracedContentHash(uniqueID string) bool {
	return uniqueID == TracedContentHashSentinel
}
