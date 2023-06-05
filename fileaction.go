package llblib

import "github.com/moby/buildkit/client/llb"

// CopyDirContentsOnly is an llb.CopyOption to indicate that the contents of the
// src directory should be copied, rather than the directory itself.
var CopyDirContentsOnly = copyOptionFunc(func(ci *llb.CopyInfo) {
	ci.CopyDirContentsOnly = true
})
