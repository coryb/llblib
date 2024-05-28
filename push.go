package llblib

import (
	"strconv"

	"github.com/distribution/reference"
	"github.com/moby/buildkit/client"
)

type registryPushOpts struct {
	attrs map[string]string
}

// RegistryPushOption can be used to modify a registry push request.
type RegistryPushOption interface {
	SetRegistryPushOption(*registryPushOpts)
}

type registryPushOptionFunc func(*registryPushOpts)

func (f registryPushOptionFunc) SetRegistryPushOption(o *registryPushOpts) {
	f(o)
}

// WithInsecurePush will allow pushing to an insecure registry.
func WithInsecurePush() RegistryPushOption {
	return registryPushOptionFunc(func(o *registryPushOpts) {
		o.attrs["registry.insecure"] = "true"
	})
}

// WithPushByDigest will push the image by digest.
func WithPushByDigest() RegistryPushOption {
	return registryPushOptionFunc(func(o *registryPushOpts) {
		o.attrs["push-by-digest"] = "true"
	})
}

// WithCompression will set the compression type for the push.
func WithCompression(compression string, force bool) RegistryPushOption {
	return registryPushOptionFunc(func(o *registryPushOpts) {
		o.attrs["compression"] = compression
		o.attrs["force-compression"] = strconv.FormatBool(force)
		if compression == "zstd" || compression == "estargz" {
			o.attrs["oci-mediatypes"] = "true"
		}
	})
}

// RegistryPush will push the request build state to the registry.
func RegistryPush(ref reference.Named, opts ...RegistryPushOption) RequestOption {
	o := &registryPushOpts{
		attrs: map[string]string{
			"name": ref.String(),
			"push": "true",
		},
	}
	for _, opt := range opts {
		opt.SetRegistryPushOption(o)
	}
	return requestOptionFunc(func(r *Request) {
		r.exports = append(r.exports, client.ExportEntry{
			Type:  client.ExporterImage,
			Attrs: o.attrs,
		})
	})
}
