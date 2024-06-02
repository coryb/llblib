package llblib

import (
	"io"

	"github.com/distribution/reference"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session/filesync"
)

// DockerSave will stream the build state as docker image tar, the tar will
// be written to the output argument.
func DockerSave(ref reference.Reference, output io.WriteCloser) RequestOption {
	return requestOptionFunc(func(r *Request) {
		exportIndex := len(r.exports)
		outputFunc := func(map[string]string) (io.WriteCloser, error) {
			return output, nil
		}
		r.exports = append(r.exports, client.ExportEntry{
			Type: client.ExporterDocker,
			Attrs: map[string]string{
				"name": ref.String(),
			},
		})
		r.download = filesync.NewFSSyncTarget(
			filesync.WithFSSync(exportIndex, outputFunc),
		)
	})
}
