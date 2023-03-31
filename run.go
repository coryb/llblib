package llblib

import (
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/identity"
)

type RunOptions []llb.RunOption

func (ro RunOptions) SetRunOption(ei *llb.ExecInfo) {
	for _, o := range ro {
		o.SetRunOption(ei)
	}
}

func IgnoreCache() llb.RunOption {
	return llb.AddEnv("LLBLIB_IGNORE_CACHE", identity.NewID())
}

type copyOptionFunc func(*llb.CopyInfo)

func (f copyOptionFunc) SetCopyOption(ci *llb.CopyInfo) {
	f(ci)
}

func IncludePatterns(p []string) llb.CopyOption {
	return copyOptionFunc(func(ci *llb.CopyInfo) {
		ci.IncludePatterns = p
	})
}

type NullOption struct{}

func (NullOption) SetConstraintsOption(c *llb.Constraints) {}
func (NullOption) SetCopyOption(*llb.CopyInfo)             {}
func (NullOption) SetGitOption(*llb.GitInfo)               {}
func (NullOption) SetHTTPOption(*llb.HTTPInfo)             {}
func (NullOption) SetImageOption(*llb.ImageInfo)           {}
func (NullOption) SetLocalOption(*llb.LocalInfo)           {}
func (NullOption) SetMkdirOption(*llb.MkdirInfo)           {}
func (NullOption) SetMkfileOption(*llb.MkfileInfo)         {}
func (NullOption) SetOCILayoutOption(*llb.OCILayoutInfo)   {}
func (NullOption) SetRmOption(*llb.RmInfo)                 {}
func (NullOption) SetRunOption(*llb.ExecInfo)              {}
func (NullOption) SetSSHOption(*llb.SSHInfo)               {}
func (NullOption) SetSecretOption(*llb.SecretInfo)         {}
func (NullOption) SetTmpfsOption(*llb.TmpfsInfo)           {}
