package llblib

import "github.com/moby/buildkit/client/llb"

type RunOptions []llb.RunOption

func (ro RunOptions) SetRunOption(ei *llb.ExecInfo) {
	for _, o := range ro {
		o.SetRunOption(ei)
	}
}
