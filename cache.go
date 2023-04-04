package llblib

import "github.com/moby/buildkit/client/llb"

// AddCacheMounts is a helper function that calls llb.AddMount with the
// llb.AsPersistentCacheDir option with the provided mode. The mount ID will
// be the id argument + the path being added.
func AddCacheMounts(paths []string, id string, mode llb.CacheMountSharingMode) llb.RunOption {
	options := RunOptions{}
	for _, path := range paths {
		options = append(options, llb.AddMount(path, llb.Scratch(), llb.AsPersistentCacheDir(id+path, mode)))
	}
	return options
}
