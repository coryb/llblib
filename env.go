package llblib

import (
	"sort"

	"github.com/moby/buildkit/client/llb"
	"golang.org/x/exp/maps"
)

func AddEnvs(envs map[string]string) llb.StateOption {
	keys := maps.Keys(envs)
	sort.Strings(keys)
	return func(s llb.State) llb.State {
		for _, k := range keys {
			s = s.AddEnv(k, envs[k])
		}
		return s
	}
}
