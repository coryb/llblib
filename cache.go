package llblib

import "github.com/moby/buildkit/client/llb"

func AddCacheMounts(paths []string, id string, mode llb.CacheMountSharingMode) llb.RunOption {
	options := RunOptions{}
	for _, path := range paths {
		options = append(options, llb.AddMount(path, llb.Scratch(), llb.AsPersistentCacheDir(id+path, mode)))
	}
	return options
}
