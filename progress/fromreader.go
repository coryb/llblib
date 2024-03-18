package progress

import (
	"io"
	"time"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/opencontainers/go-digest"
)

// FromReader is a lightly modified copy version from the buildx progress here:
// https://github.com/docker/buildx/blob/v0.10.4/util/progress/fromreader.go#L12

// FromReader will report the progress of reading from the ReadCloser.
func FromReader(p Progress, name string, rc io.ReadCloser) {
	dgst := digest.FromBytes([]byte(identity.NewID()))
	tm := time.Now()

	vtx := client.Vertex{
		Digest:  dgst,
		Name:    name,
		Started: &tm,
	}

	ch := p.Channel()
	defer close(ch)

	ch <- &client.SolveStatus{
		Vertexes: []*client.Vertex{&vtx},
	}

	tm2 := time.Now()
	vtx2 := vtx
	vtx2.Completed = &tm2
	if _, err := io.Copy(io.Discard, rc); err != nil {
		vtx2.Error = err.Error()
	}
	ch <- &client.SolveStatus{
		Vertexes: []*client.Vertex{&vtx2},
	}
}
