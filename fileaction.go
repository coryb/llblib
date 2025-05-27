package llblib

import (
	"os"

	"github.com/moby/buildkit/client/llb"
)

// Mode is an llb.CopyOption to set the file mode of the copied file.
func Mode(m os.FileMode) llb.CopyOption {
	return copyOptionFunc(func(ci *llb.CopyInfo) {
		ci.Mode = &llb.ChmodOpt{Mode: m}
	})
}

// FollowSymlinks is an llb.CopyOption to indicate that symlinks should be
// followed when copying.
var FollowSymlinks = copyOptionFunc(func(ci *llb.CopyInfo) {
	ci.FollowSymlinks = true
})

// CopyDirContentsOnly is an llb.CopyOption to indicate that the contents of the
// src directory should be copied, rather than the directory itself.
var CopyDirContentsOnly = copyOptionFunc(func(ci *llb.CopyInfo) {
	ci.CopyDirContentsOnly = true
})

// AttemptUnpack is an llb.CopyOption to indicate that the source should be
// unpacked if it is an archive.
var AttemptUnpack = copyOptionFunc(func(ci *llb.CopyInfo) {
	ci.AttemptUnpack = true
})

// CreateDestPath is an llb.CopyOption to indicate that the destination path
// should be created if it does not exist.
var CreateDestPath = copyOptionFunc(func(ci *llb.CopyInfo) {
	ci.CreateDestPath = true
})

// AllowWildcard is an llb.CopyOption to indicate that wildcards are allowed in
// the source path.
var AllowWildcard = copyOptionFunc(func(ci *llb.CopyInfo) {
	ci.AllowWildcard = true
})

// AllowEmptyWildcard is an llb.CopyOption to indicate that it is not an error
// for a wildcard to have no matches from the source path.
var AllowEmptyWildcard = copyOptionFunc(func(ci *llb.CopyInfo) {
	ci.AllowEmptyWildcard = true
})

// Chown is an llb.CopyOption to set the owner and group of the copied file.
func Chown(o llb.ChownOpt) llb.CopyOption {
	return copyOptionFunc(func(ci *llb.CopyInfo) {
		ci.ChownOpt = &o
	})
}
