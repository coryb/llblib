package llblib

import (
	"maps"
	"slices"
	"sort"

	"github.com/moby/buildkit/client/llb"
)

// AddEnvs is a helper function over `llb.AddEnv` where the provided map
// will be added to the returned llb.StateOption. The env vars are applied
// in key sort order to allow for a consistent state for caching.
func AddEnvs(envs map[string]string) llb.StateOption {
	keys := slices.Collect(maps.Keys(envs))
	sort.Strings(keys)
	return func(s llb.State) llb.State {
		for _, k := range keys {
			s = s.AddEnv(k, envs[k])
		}
		return s
	}
}
