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

type NullOption struct{}

func (NullOption) SetConstraintsOption(c *llb.Constraints) {}
func (NullOption) SetRunOption(*llb.ExecInfo)              {}
func (NullOption) SetLocalOption(*llb.LocalInfo)           {}
func (NullOption) SetHTTPOption(*llb.HTTPInfo)             {}
func (NullOption) SetImageOption(*llb.ImageInfo)           {}
func (NullOption) SetGitOption(*llb.GitInfo)               {}
func (NullOption) SetOCILayoutOption(*llb.OCILayoutInfo)   {}
