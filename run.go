package llblib

import (
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/identity"
)

// RunOptions is a helper to return a list of llb.RunOptions as a single
// llb.RunOption.
type RunOptions []llb.RunOption

// SetRunOption implements the llb.RunOption interface for RunOptions
func (ro RunOptions) SetRunOption(ei *llb.ExecInfo) {
	for _, o := range ro {
		o.SetRunOption(ei)
	}
}

// IgnoreCache can be used to invalidate existing cache for the Run operation
// forcing the Run operation to be executed again.  This allows the result state
// to be reused within the session.
// This is different from `llb.IgnoreCache` in that `llb.IgnoreCache` will
// prevent any cache from being written from the state, so the Run operation
// will be executed multiple times within a session if the session evaluates the
// same vertex multiple times.
func IgnoreCache() llb.RunOption {
	return llb.AddEnv("LLBLIB_IGNORE_CACHE", identity.NewID())
}

type copyOptionFunc func(*llb.CopyInfo)

func (f copyOptionFunc) SetCopyOption(ci *llb.CopyInfo) {
	f(ci)
}

// IncludePatterns provides a llb.CopyOption that sets the provided patterns
// on the llb.Copy instruction.
func IncludePatterns(p []string) llb.CopyOption {
	return copyOptionFunc(func(ci *llb.CopyInfo) {
		ci.IncludePatterns = p
	})
}

// NullOption implements a generic no-op option for all of the option
// interfaces.
type NullOption struct{}

func (NullOption) SetSolverOption(*solver)               {} //nolint:revive
func (NullOption) SetRequestOption(*Request)             {} //nolint:revive
func (NullOption) SetFrontendOption(*frontendOptions)    {} //nolint:revive
func (NullOption) SetConstraintsOption(*llb.Constraints) {} //nolint:revive
func (NullOption) SetCopyOption(*llb.CopyInfo)           {} //nolint:revive
func (NullOption) SetGitOption(*llb.GitInfo)             {} //nolint:revive
func (NullOption) SetHTTPOption(*llb.HTTPInfo)           {} //nolint:revive
func (NullOption) SetImageOption(*llb.ImageInfo)         {} //nolint:revive
func (NullOption) SetLocalOption(*llb.LocalInfo)         {} //nolint:revive
func (NullOption) SetMkdirOption(*llb.MkdirInfo)         {} //nolint:revive
func (NullOption) SetMkfileOption(*llb.MkfileInfo)       {} //nolint:revive
func (NullOption) SetOCILayoutOption(*llb.OCILayoutInfo) {} //nolint:revive
func (NullOption) SetRmOption(*llb.RmInfo)               {} //nolint:revive
func (NullOption) SetRunOption(*llb.ExecInfo)            {} //nolint:revive
func (NullOption) SetSSHOption(*llb.SSHInfo)             {} //nolint:revive
func (NullOption) SetSecretOption(*llb.SecretInfo)       {} //nolint:revive
func (NullOption) SetTmpfsOption(*llb.TmpfsInfo)         {} //nolint:revive
