package llblib

import (
	"maps"

	"github.com/moby/buildkit/client/llb"
)

// MountPropagator manages a collection of llb.States and run Mounts. As
// Runs are applies to the MountPropagator the modified state of the root
// and all attached mounts are propagated such that future Runs will see
// changes made by prior Runs.
type MountPropagator interface {
	// Add will append a RunOption to the list of propagated options.
	Add(...llb.RunOption)
	// ApplyMount will apply the provide llb.StateOptions to the mount at
	// `mountpoint`.  If there is no mount present matching `mountpoint` then a
	// new llb.Scratch will be created and the llb.StateOptions will be applied
	// to that state.
	ApplyMount(mountpoint string, opts ...llb.StateOption)
	// ApplyRoot will apply the provided llb.StateOptions to the root mount.
	ApplyRoot(...llb.StateOption)
	// AsRun is used to extract the current state of all mounts (and associated
	// RunOptions via Add) so that they can be applied to an `llb.Run`
	// operation.
	AsRun() llb.RunOption
	// Copy creates a new MountPropagator with copies of the parents RunOptions
	// and llb.States.  Note that the RunOptions and llb.States themselves are
	// only shallow copied.
	Copy() MountPropagator
	// GetMount returns the llb.State for the modified mount found at
	// `mountpoint`.  If the mountpoint is not found, then llb.Scratch will
	// returned and `ok` will be set to false.
	GetMount(mountpoint string) (state llb.State, ok bool)
	// Run will mutate the state by applying the Run to the root and mountpoints
	// while preserving any changes for future Run statements.
	Run(...llb.RunOption)
	// Root will return the llb.State for the modified root "/" mount.
	Root() llb.State
}

// Persistent returns a MountPropagator for the provided root llb.State and
// any mounts found in the llb.RunOptions.
func Persistent(root llb.State, opts ...llb.RunOption) MountPropagator {
	pm := &persistentMounts{
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

func (pm *persistentMounts) Run(opts ...llb.RunOption) {
	runOpts := []llb.RunOption{}
	mountpoints := []string{}
	for _, o := range pm.opts {
		ei := llb.ExecInfo{}
		o.SetRunOption(&ei)
		for i, eim := range ei.Mounts {
			if st, ok := pm.states[eim.Target]; ok {
				ei.Mounts[i].Source = st.Output()
			}
			mountpoints = append(mountpoints, eim.Target)
		}
		runOpts = append(runOpts, mountPropagatorRunOption{&ei})
	}

	runOpts = append(runOpts, opts...)

	execState := Run(pm.root, runOpts...)
	pm.root = execState.Root()
	for _, mountpoint := range mountpoints {
		pm.states[mountpoint] = execState.GetMount(mountpoint)
	}
}

func (pm *persistentMounts) AsRun() llb.RunOption {
	runOpts := RunOptions{}
	for _, o := range pm.opts {
		ei := llb.ExecInfo{}
		o.SetRunOption(&ei)
		for i, eim := range ei.Mounts {
			if st, ok := pm.states[eim.Target]; ok {
				ei.Mounts[i].Source = st.Output()
			}
		}
		runOpts = append(runOpts, mountPropagatorRunOption{&ei})
	}
	return runOpts
}

func (pm *persistentMounts) Add(opts ...llb.RunOption) {
	pm.opts = append(pm.opts, opts...)
}

func (pm *persistentMounts) Copy() MountPropagator {
	newPM := &persistentMounts{
		root:   pm.root,
		opts:   make([]llb.RunOption, len(pm.opts)),
		states: map[string]llb.State{},
	}
	copy(newPM.opts, pm.opts)
	maps.Copy(newPM.states, pm.states)
	return newPM
}

func (pm *persistentMounts) ApplyRoot(opts ...llb.StateOption) {
	for _, opt := range opts {
		pm.root = opt(pm.root)
	}
}

func (pm *persistentMounts) ApplyMount(mountpoint string, opts ...llb.StateOption) {
	mount, ok := pm.states[mountpoint]
	if !ok {
		mount = llb.Scratch()
	}
	for _, opt := range opts {
		mount = opt(mount)
	}
	pm.states[mountpoint] = mount
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

func (pm *persistentMounts) Root() llb.State {
	return pm.root
}

func (pm *persistentMounts) GetMount(mountpoint string) (llb.State, bool) {
	st, ok := pm.states[mountpoint]
	if !ok {
		return llb.Scratch(), false
	}
	return st, true
}

// File implements an llb.StateOption where the provided llb.FileAction is
// applied to the llb.State.
func File(a *llb.FileAction, opts ...llb.ConstraintsOpt) llb.StateOption {
	return func(st llb.State) llb.State {
		return st.File(a, opts...)
	}
}
