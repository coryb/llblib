package llblib

import (
	"fmt"

	"github.com/moby/buildkit/client/llb"
	"golang.org/x/exp/maps"
)

type MountPropagator interface {
	Run(...llb.RunOption) MountPropagator
	// Add will append a RunOption to the list of propagated mounts.
	Add(...llb.RunOption)
	ApplyRoot(...llb.StateOption)
	ApplyMount(target string, opts ...llb.StateOption)
	Root() llb.State
	GetMount(target string) llb.State
	Copy() MountPropagator
}

func Persistent(root llb.State, opts ...llb.RunOption) MountPropagator {
	pm := persistentMounts{
		opts:   opts,
		root:   root,
		states: map[string]llb.State{},
	}
	return pm
}

type persistentMounts struct {
	opts   []llb.RunOption
	root   llb.State
	states map[string]llb.State
}

var _ MountPropagator = (*persistentMounts)(nil)

func (pm persistentMounts) Run(opts ...llb.RunOption) MountPropagator {
	runOpts := make([]llb.RunOption, len(opts))
	copy(runOpts, opts)

	targets := []string{}
	for _, o := range pm.opts {
		ei := llb.ExecInfo{}
		o.SetRunOption(&ei)
		for i, eim := range ei.Mounts {
			if st, ok := pm.states[eim.Target]; ok {
				ei.Mounts[i].Source = st.Output()
			}
			targets = append(targets, eim.Target)
		}
		runOpts = append(runOpts, mountPropagatorRunOption{&ei})
	}

	execState := pm.root.Run(runOpts...)
	pm.root = execState.Root()
	for _, target := range targets {
		pm.states[target] = execState.GetMount(target)
	}
	return pm
}

func (pm persistentMounts) Add(opts ...llb.RunOption) {
	pm.opts = append(pm.opts, opts...)
}

func (pm persistentMounts) Copy() MountPropagator {
	newPM := persistentMounts{
		root:   pm.root,
		opts:   make([]llb.RunOption, len(pm.opts)),
		states: map[string]llb.State{},
	}
	copy(newPM.opts, pm.opts)
	maps.Copy(newPM.states, pm.states)
	return newPM
}

func (pm persistentMounts) ApplyRoot(opts ...llb.StateOption) {
	for _, opt := range opts {
		pm.root = opt(pm.root)
	}
}

func (pm persistentMounts) ApplyMount(target string, opts ...llb.StateOption) {
	mount, ok := pm.states[target]
	if !ok {
		mount = llb.Scratch()
	}
	for _, opt := range opts {
		mount = opt(mount)
	}
	pm.states[target] = mount
}

type mountPropagatorRunOption struct {
	ei *llb.ExecInfo
}

var _ llb.RunOption = (*mountPropagatorRunOption)(nil)

func (ro mountPropagatorRunOption) SetRunOption(ei *llb.ExecInfo) {
	ei.Constraints = ro.ei.Constraints
	if ro.ei.Platform != nil {
		ei.Platform = ro.ei.Platform
	}
	ei.WorkerConstraints = append(ei.WorkerConstraints, ro.ei.WorkerConstraints...)
	if ro.ei.Metadata.IgnoreCache {
		ei.Metadata.IgnoreCache = ro.ei.Metadata.IgnoreCache
	}
	for k, v := range ro.ei.Metadata.Description {
		ei.Metadata.Description[k] = v
	}
	if ro.ei.Metadata.ExportCache != nil {
		ei.Metadata.ExportCache = ro.ei.Metadata.ExportCache
	}
	for k, v := range ro.ei.Metadata.Caps {
		ei.Metadata.Caps[k] = v
	}
	if ro.ei.Metadata.ProgressGroup != nil {
		ei.Metadata.ProgressGroup = ro.ei.Metadata.ProgressGroup
	}
	if ro.ei.LocalUniqueID != "" {
		ei.LocalUniqueID = ro.ei.LocalUniqueID
	}
	if ro.ei.Caps != nil {
		ei.Caps = ro.ei.Caps
	}
	ei.SourceLocations = append(ei.SourceLocations, ro.ei.SourceLocations...)

	ei.Mounts = append(ei.Mounts, ro.ei.Mounts...)
	if ro.ei.ReadonlyRootFS {
		ei.ReadonlyRootFS = ro.ei.ReadonlyRootFS
	}
	if ro.ei.ProxyEnv != nil {
		ei.ProxyEnv = ro.ei.ProxyEnv
	}
	ei.Secrets = append(ei.Secrets, ro.ei.Secrets...)
	ei.SSH = append(ei.SSH, ro.ei.SSH...)
}

func (pm persistentMounts) Root() llb.State {
	return pm.root
}

func (pm persistentMounts) GetMount(target string) llb.State {
	st, ok := pm.states[target]
	if !ok {
		// this should never happen
		panic(fmt.Sprintf("mount state missing for target %q", target))
	}
	return st
}

func File(a *llb.FileAction, opts ...llb.ConstraintsOpt) llb.StateOption {
	return func(st llb.State) llb.State {
		return st.File(a, opts...)
	}
}
