package llblib

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coryb/walky"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/exp/maps"
	"gopkg.in/yaml.v3"
)

// ToYAML will serialize the llb.States to a yaml sequence node where each
// node in the sequence represents the corresponding states passed in.
func ToYAML(ctx context.Context, states ...llb.State) (*yaml.Node, error) {
	counter := 0
	g := graphState{
		ops:          map[digest.Digest]*pb.Op{},
		meta:         map[digest.Digest]pb.OpMetadata{},
		cache:        map[digest.Digest]*yaml.Node{},
		outputs:      map[string]*yaml.Node{},
		aliases:      map[digest.Digest]string{},
		aliasCounter: &counter,
	}

	nodes := walky.NewSequenceNode()
	for _, st := range states {
		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "failed to marshal state")
		}

		root, g, err := g.newGraph(def.ToPB())
		if err != nil {
			return nil, err
		}
		if root == nil {
			// no error, so we must be solving llb.Scratch()
			walky.AppendNode(nodes, g.scratchNode())
			continue
		}

		node, err := g.visit(root)
		if err != nil {
			return nil, err
		}

		if root.Index > 0 {
			// We must be returning a mount on an ExecOp. We can't really
			// express this in the yaml tree structure, so generate a special
			// MOUNT node to express that we want to extract a mount from an
			// ExecOp.
			var output *yaml.Node
			if mounts := walky.GetKey(node, "mounts"); mounts != nil {
				for _, m := range mounts.Content {
					if outputIndex := walky.GetKey(m, "output"); outputIndex != nil {
						if outputIndex.Value == strconv.Itoa(int(root.Index)) {
							// found our mount, so set the output
							anchor := g.anchorName(root.Digest) + "_" + strconv.Itoa(int(root.Index))
							output = yamlAliasOf(m, anchor)
							break
						}
					}
				}
			} else if node.Kind == yaml.AliasNode {
				// so we want a mount (root index > 0), but it was already
				// aliased to the right mount.  So the "input" is actually the
				// parent of the aliased mount, and the "output" is the node we
				// have. The parent "input" should be in our cache under the
				// root digest, so lets reset things here and create an alias
				// to the parent "input".
				if parent, ok := g.cache[root.Digest]; ok {
					output = node
					anchor := g.anchorName(root.Digest)
					node = yamlAliasOf(parent, anchor)
				}
			}

			fromMount := walky.NewMappingNode()
			yamlAddKV(fromMount, "type", "MOUNT")
			yamlMapAdd(fromMount, walky.NewStringNode("input"), node)
			yamlMapAdd(fromMount, walky.NewStringNode("output"), output)
			// return our synthetic mount result opt
			node = fromMount
		}

		walky.AppendNode(nodes, node)
	}
	return nodes, nil
}

type graphState struct {
	ops          map[digest.Digest]*pb.Op
	meta         map[digest.Digest]pb.OpMetadata
	cache        map[digest.Digest]*yaml.Node
	outputs      map[string]*yaml.Node
	aliases      map[digest.Digest]string
	aliasCounter *int
}

func (g graphState) newGraph(def *pb.Definition) (*pb.Input, graphState, error) {
	ops := maps.Clone(g.ops)
	var dgst digest.Digest
	for _, dt := range def.Def {
		var pbOp pb.Op
		if err := (&pbOp).Unmarshal(dt); err != nil {
			return nil, graphState{}, err
		}
		dgst = digest.FromBytes(dt)
		ops[dgst] = &pbOp
	}
	meta := maps.Clone(g.meta)
	for d, m := range def.Metadata {
		meta[d] = m
	}

	if dgst == "" {
		return nil, graphState{
			meta:         meta,
			cache:        g.cache,
			aliases:      g.aliases,
			aliasCounter: g.aliasCounter,
		}, nil
	}
	terminal := ops[dgst]
	return terminal.Inputs[0], graphState{
		ops:          ops,
		meta:         meta,
		cache:        g.cache,
		aliases:      g.aliases,
		aliasCounter: g.aliasCounter,
	}, nil
}

func (g graphState) anchorName(d digest.Digest) string {
	if name, ok := g.aliases[d]; ok {
		return name
	}
	name := "ref" + strconv.Itoa(*g.aliasCounter)
	g.aliases[d] = name
	*g.aliasCounter++
	return name
}

type yamlOpt func(n *yaml.Node) *yaml.Node

func flowStyle(n *yaml.Node) *yaml.Node {
	n.Style = yaml.FlowStyle
	return n
}

func applyOpts(n *yaml.Node, opts ...yamlOpt) *yaml.Node {
	for _, o := range opts {
		n = o(n)
	}
	return n
}

func yamlAliasOf(n *yaml.Node, anchor string) *yaml.Node {
	n.Anchor = anchor
	alias := &yaml.Node{}
	alias.Kind = yaml.AliasNode
	alias.Alias = n
	alias.Value = n.Anchor
	return alias
}

func yamlMapAdd(n, k, v *yaml.Node) {
	n.Content = append(n.Content, k, v)
}

func yamlAddInt(n *yaml.Node, name string, i int64) {
	if i == -1 {
		return
	}
	yamlMapAdd(n,
		walky.NewStringNode(name),
		applyOpts(walky.NewIntNode(i)),
	)
}

func yamlAddBool(n *yaml.Node, name string, b bool) {
	if !b {
		return
	}
	yamlMapAdd(n,
		walky.NewStringNode(name),
		applyOpts(walky.NewBoolNode(b)),
	)
}

func yamlAddKV(n *yaml.Node, k string, v any, opts ...yamlOpt) {
	if v == "" {
		return
	}
	yamlMapAdd(n,
		walky.NewStringNode(k),
		applyOpts(walky.NewStringNode(fmt.Sprint(v)), opts...),
	)
}

func yamlAddMap(n *yaml.Node, name string, m map[string]string, opts ...yamlOpt) {
	if len(m) == 0 {
		return
	}
	attrs := walky.NewMappingNode()
	keys := maps.Keys(m)
	sort.Strings(keys)
	for _, key := range keys {
		yamlMapAdd(attrs,
			walky.NewStringNode(key),
			walky.NewStringNode(m[key]),
		)
	}
	yamlMapAdd(n,
		walky.NewStringNode(name),
		applyOpts(attrs, opts...),
	)
}

func yamlAddSeq(n *yaml.Node, name string, strs []string, opts ...yamlOpt) {
	if len(strs) == 0 {
		return
	}
	seq := walky.NewSequenceNode()
	for _, str := range strs {
		walky.AppendNode(seq,
			applyOpts(walky.NewStringNode(str), opts...),
		)
	}
	yamlMapAdd(n,
		walky.NewStringNode(name),
		applyOpts(seq, opts...),
	)
}

func yamlAddOwner(n *yaml.Node, owner *pb.ChownOpt, opts ...yamlOpt) {
	if owner != nil {
		ownerNode := walky.NewDocumentNode()
		yamlMapAdd(n, walky.NewStringNode("owner"),
			applyOpts(ownerNode, opts...),
		)
		switch u := owner.User.User.(type) {
		case *pb.UserOpt_ByName:
			yamlAddKV(ownerNode, "user", u.ByName.Name)
		case *pb.UserOpt_ByID:
			yamlAddInt(ownerNode, "user", int64(u.ByID))
		}
		switch u := owner.Group.User.(type) {
		case *pb.UserOpt_ByName:
			yamlAddKV(ownerNode, "group", u.ByName.Name)
		case *pb.UserOpt_ByID:
			yamlAddInt(ownerNode, "group", int64(u.ByID))
		}
	}
}

func yamlAddTime(n *yaml.Node, t int64, opts ...yamlOpt) {
	if t != -1 {
		yamlAddKV(n, "timestamp", time.Unix(t, 0).Format(time.RFC3339Nano), opts...)
	}
}

func yamlAddMode(n *yaml.Node, mode int32) {
	if mode != -1 {
		yamlAddKV(n, "mode", "0o"+strconv.FormatInt(int64(mode), 8))
	}
}

func (g graphState) scratchNode() *yaml.Node {
	if n, ok := g.cache["scratch"]; ok {
		return yamlAliasOf(n, "scratch")
	}
	scratch := walky.NewMappingNode()
	yamlAddKV(scratch, "type", "SOURCE")
	yamlAddKV(scratch, "source", "scratch")
	g.cache["scratch"] = scratch
	return scratch
}

func (g graphState) visit(input *pb.Input) (node *yaml.Node, err error) {
	defer func() {
		if node != nil && g.cache[input.Digest] == nil {
			g.cache[input.Digest] = node
			// add llb.WithCustomName as a comment for the node if found
			if name, ok := g.meta[input.Digest].Description["llb.customname"]; ok {
				node.HeadComment = name
			}
		}
	}()
	if cached, ok := g.cache[input.Digest]; ok {
		// if alias has mounts (ie ExecOp) then we want to alias the output
		// mount and not the entire op
		if mounts := walky.GetKey(cached, "mounts"); mounts != nil {
			for _, m := range mounts.Content {
				if output := walky.GetKey(m, "output"); output != nil {
					if output.Value == strconv.Itoa(int(input.Index)) {
						cached = m
						break
					}
				}
			}
		}
		anchor := g.anchorName(input.Digest) + "_" + strconv.Itoa(int(input.Index))
		node := yamlAliasOf(cached, anchor)
		return node, nil
	}

	op := g.ops[input.Digest]
	if op == nil {
		return nil, errors.Errorf("op %s not found", input.Digest)
	}

	// any op can have filter constraints, so defer apply them to the
	// node if there is no error
	defer func() {
		if err == nil && node != nil && op.Constraints != nil && len(op.Constraints.Filter) > 0 {
			constraints := walky.NewMappingNode()
			yamlMapAdd(node, walky.NewStringNode("constraints"), constraints)
			yamlAddSeq(constraints, "filters", op.Constraints.Filter)
		}
	}()

	switch v := op.Op.(type) {
	case *pb.Op_Exec:
		node, err := g.yamlExecOp(op, v.Exec)
		if err != nil {
			return nil, err
		}
		yamlAddBool(node, "ignore-cache", g.meta[input.Digest].IgnoreCache)
		return node, nil
	case *pb.Op_Source:
		node := g.yamlSourceOp(v.Source)
		if op.Platform != nil {
			yamlAddKV(node, "platform",
				fmt.Sprintf("%s/%s", op.Platform.OS, op.Platform.Architecture),
			)
		}
		return node, err
	case *pb.Op_File:
		return g.yamlFileOp(op, v.File)
	case *pb.Op_Build:
		return g.yamlBuildOp(op, v.Build)
	case *pb.Op_Merge:
		return g.yamlMergeOp(op, v.Merge)
	case *pb.Op_Diff:
		return g.yamlDiffOp(op, v.Diff)
	default:
		return nil, errors.Errorf("unexpected op type %T", op.Op)
	}
}

func (g graphState) yamlExecOp(op *pb.Op, e *pb.ExecOp) (*yaml.Node, error) {
	node := walky.NewMappingNode()
	yamlAddKV(node, "type", "EXEC")

	opts := []yamlOpt{flowStyle}
	// only use flowStyle if the args dont have multi-line statements
	for _, arg := range e.Meta.Args {
		if strings.Contains(arg, "\n") {
			opts = []yamlOpt{}
			break
		}
	}
	yamlAddSeq(node, "args", e.Meta.Args, opts...)
	yamlAddSeq(node, "env", e.Meta.Env, flowStyle)
	yamlAddKV(node, "cwd", e.Meta.Cwd)
	yamlAddKV(node, "user", e.Meta.User)
	yamlAddKV(node, "hostname", e.Meta.Hostname)
	if len(e.Meta.ExtraHosts) > 0 {
		extraHosts := walky.NewSequenceNode()
		yamlMapAdd(node,
			walky.NewStringNode("extra-hosts"),
			extraHosts)
		for _, host := range e.Meta.ExtraHosts {
			n := walky.NewMappingNode()
			n.Style = yaml.FlowStyle
			yamlMapAdd(n,
				walky.NewStringNode("host"),
				walky.NewStringNode(host.Host),
			)
			yamlMapAdd(n,
				walky.NewStringNode("ip"),
				walky.NewStringNode(host.IP),
			)
			walky.AppendNode(extraHosts, n)
		}
	}

	if e.Network != pb.NetMode_UNSET {
		yamlAddKV(node, "network-mode", e.Network)
	}
	if e.Security != pb.SecurityMode_SANDBOX {
		yamlAddKV(node, "security-mode", e.Security)
	}

	if len(e.Secretenv) > 0 {
		secretEnv := walky.NewMappingNode()
		yamlMapAdd(node, walky.NewStringNode("secret-envs"), secretEnv)
		for _, se := range e.Secretenv {
			walky.AppendNode(secretEnv,
				walky.NewStringNode(se.Name+"="+se.ID),
			)
		}
	}

	mounts := walky.NewSequenceNode()
	yamlMapAdd(node, walky.NewStringNode("mounts"), mounts)
	for _, m := range e.Mounts {
		mountNode, err := g.yamlMount(op, m)
		if err != nil {
			return nil, err
		}
		walky.AppendNode(mounts, mountNode)
	}
	return node, nil
}

func (g graphState) yamlMount(op *pb.Op, m *pb.Mount) (*yaml.Node, error) {
	mount := walky.NewMappingNode()
	yamlAddKV(mount, "mountpoint", m.Dest)
	yamlAddKV(mount, "type", m.MountType)
	if m.MountType == pb.MountType_BIND && !m.Readonly {
		// CACHE, SECRET, SSH, TMPFS mounts dont have outputs
		yamlAddInt(mount, "output", int64(m.Output))
	}
	yamlAddBool(mount, "readonly", m.Readonly)
	yamlAddKV(mount, "source-path", m.Selector)
	if m.CacheOpt != nil {
		yamlAddKV(mount, "cache-id", m.CacheOpt.ID)
		yamlAddKV(mount, "sharing", m.CacheOpt.Sharing)
	}
	if m.SecretOpt != nil {
		yamlAddKV(mount, "secret", m.SecretOpt.ID)
		if m.SecretOpt.Uid != 0 {
			yamlAddInt(mount, "uid", int64(m.SecretOpt.Uid))
		}
		if m.SecretOpt.Gid != 0 {
			yamlAddInt(mount, "gid", int64(m.SecretOpt.Gid))
		}
		yamlAddMode(mount, int32(m.SecretOpt.Mode))
	}
	if m.SSHOpt != nil {
		yamlAddKV(mount, "ssh", m.SSHOpt.ID)
		if m.SSHOpt.Uid != 0 {
			yamlAddInt(mount, "uid", int64(m.SSHOpt.Uid))
		}
		if m.SSHOpt.Gid != 0 {
			yamlAddInt(mount, "gid", int64(m.SSHOpt.Gid))
		}
		yamlAddMode(mount, int32(m.SSHOpt.Mode))
	}

	if m.MountType != pb.MountType_BIND {
		// CACHE, SECRET, SSH TMPFS mounts dont have inputs, return now.
		return mount, nil
	}

	if m.Input < 0 {
		yamlMapAdd(mount, walky.NewStringNode("input"), g.scratchNode())
		return mount, nil
	}
	if int(m.Input) >= len(op.Inputs) {
		return nil, errors.Errorf("invalid op, impossible input %d from op with %d inputs", m.Input, len(op.Inputs))
	}

	input, err := g.visit(op.Inputs[m.Input])
	if err != nil {
		return nil, errors.Wrapf(err, "visiting mount %s", m.Dest)
	}
	yamlMapAdd(mount, walky.NewStringNode("input"), input)
	return mount, nil
}

func (g graphState) yamlSourceOp(s *pb.SourceOp) *yaml.Node {
	node := walky.NewMappingNode()
	yamlAddKV(node, "type", "SOURCE")
	yamlAddKV(node, "source", s.Identifier)
	yamlAddMap(node, "attrs", s.Attrs, flowStyle)
	return node
}

func (g graphState) yamlFileOp(op *pb.Op, f *pb.FileOp) (*yaml.Node, error) {
	node := walky.NewMappingNode()
	yamlAddKV(node, "type", "FILE")
	actions := walky.NewSequenceNode()
	yamlMapAdd(node, walky.NewStringNode("actions"), actions)
	for _, act := range f.Actions {
		var action *yaml.Node
		var err error
		switch a := act.Action.(type) {
		case *pb.FileAction_Copy:
			action = g.yamlFileCopy(a.Copy)
		case *pb.FileAction_Mkdir:
			action = g.yamlFileMkdir(a.Mkdir)
		case *pb.FileAction_Mkfile:
			action = g.yamlFileMkFile(a.Mkfile)
		case *pb.FileAction_Rm:
			action = g.yamlFileRm(a.Rm)
		default:
			err = errors.Errorf("unexpected file action type %T", a)
		}
		if err != nil {
			return nil, err
		}
		inputName := "input"
		if act.SecondaryInput >= 0 {
			// for copy we use src-input and dest-input
			inputName = "dest-input"
		}
		if act.Input == -1 {
			yamlMapAdd(action, walky.NewStringNode(inputName), g.scratchNode())
		} else {
			input, err := g.visit(op.Inputs[act.Input])
			if err != nil {
				return nil, errors.Wrap(err, "visiting copy input")
			}
			yamlMapAdd(action, walky.NewStringNode(inputName), input)
		}
		if act.SecondaryInput >= 0 {
			input, err := g.visit(op.Inputs[act.SecondaryInput])
			if err != nil {
				return nil, errors.Wrap(err, "visiting copy source input")
			}
			yamlMapAdd(action, walky.NewStringNode("src-input"), input)
		}
		walky.AppendNode(actions, action)
	}
	return node, nil
}

func (g graphState) yamlFileCopy(c *pb.FileActionCopy) *yaml.Node {
	copy := walky.NewMappingNode()
	yamlAddKV(copy, "type", "COPY")
	yamlAddKV(copy, "src", c.Src)
	yamlAddKV(copy, "dest", c.Dest)
	yamlAddOwner(copy, c.Owner)
	yamlAddMode(copy, c.Mode)
	yamlAddBool(copy, "follow-symlinks", c.FollowSymlink)
	yamlAddBool(copy, "contents-only", c.DirCopyContents)
	yamlAddBool(copy, "unpack-archive", c.AttemptUnpackDockerCompatibility)
	yamlAddBool(copy, "create-dest-path", c.CreateDestPath)
	yamlAddBool(copy, "allow-wildcard", c.AllowWildcard)
	yamlAddBool(copy, "allow-empty-wildcard", c.AllowEmptyWildcard)
	yamlAddTime(copy, c.Timestamp)
	yamlAddSeq(copy, "include-patterns", c.IncludePatterns)
	yamlAddSeq(copy, "exclude-patterns", c.ExcludePatterns)
	return copy
}

func (g graphState) yamlFileMkdir(m *pb.FileActionMkDir) *yaml.Node {
	mkdir := walky.NewMappingNode()
	yamlAddKV(mkdir, "type", "MKDIR")
	yamlAddKV(mkdir, "path", m.Path)
	yamlAddMode(mkdir, m.Mode)
	yamlAddBool(mkdir, "create-parents", m.MakeParents)
	yamlAddOwner(mkdir, m.Owner)
	yamlAddTime(mkdir, m.Timestamp)
	return mkdir
}

func (g graphState) yamlFileMkFile(m *pb.FileActionMkFile) *yaml.Node {
	mkfile := walky.NewMappingNode()
	yamlAddKV(mkfile, "type", "MKFILE")
	yamlAddKV(mkfile, "path", m.Path)
	yamlAddMode(mkfile, m.Mode)
	yamlAddKV(mkfile, "data", string(m.Data))
	yamlAddOwner(mkfile, m.Owner)
	yamlAddTime(mkfile, m.Timestamp)
	return mkfile
}

func (g graphState) yamlFileRm(a *pb.FileActionRm) *yaml.Node {
	rm := walky.NewMappingNode()
	yamlAddKV(rm, "type", "RM")
	yamlAddKV(rm, "path", a.Path)
	yamlAddKV(rm, "allow-not-found", a.AllowNotFound)
	yamlAddKV(rm, "allow-wildcard", a.AllowWildcard)
	return rm
}

func (g graphState) yamlBuildOp(op *pb.Op, b *pb.BuildOp) (*yaml.Node, error) {
	node := walky.NewMappingNode()
	yamlAddKV(node, "type", "BUILD")
	yamlAddMap(node, "attrs", b.Attrs)
	if b.Builder == -1 {
		yamlMapAdd(node, walky.NewStringNode("source"), g.scratchNode())
	} else {
		source, err := g.visit(op.Inputs[b.Builder])
		if err != nil {
			return nil, errors.Wrap(err, "visiting buildOp source")
		}
		yamlMapAdd(node, walky.NewStringNode("source"), source)
	}
	inputs := walky.NewMappingNode()
	yamlMapAdd(node, walky.NewStringNode("inputs"), inputs)
	inputNames := maps.Keys(b.Inputs)
	sort.Strings(inputNames)
	for _, name := range inputNames {
		if b.Inputs[name].Input == -1 {
			yamlMapAdd(inputs, walky.NewStringNode(name), g.scratchNode())
		} else {
			input, err := g.visit(op.Inputs[b.Inputs[name].Input])
			if err != nil {
				return nil, errors.Wrapf(err, "visiting buildOp input %s", name)
			}
			yamlMapAdd(inputs, walky.NewStringNode(name), input)
		}
	}

	buildInput, buildGraph, err := g.newGraph(b.Def)
	if err != nil {
		return nil, errors.Wrap(err, "building graph for buildOp")
	}
	build, err := buildGraph.visit(buildInput)
	if err != nil {
		return nil, errors.Wrap(err, "visiting build for buildOp")
	}
	yamlMapAdd(node, walky.NewStringNode("build"), build)
	return node, nil
}

func (g graphState) yamlMergeOp(op *pb.Op, m *pb.MergeOp) (*yaml.Node, error) {
	node := walky.NewMappingNode()
	yamlAddKV(node, "type", "MERGE")
	if len(m.Inputs) > 0 {
		inputs := walky.NewSequenceNode()
		yamlMapAdd(node, walky.NewStringNode("inputs"), inputs)
		for _, inputIx := range m.Inputs {
			if inputIx.Input == -1 {
				walky.AppendNode(inputs, g.scratchNode())
			} else {
				input, err := g.visit(op.Inputs[inputIx.Input])
				if err != nil {
					return nil, errors.Wrapf(err, "visiting merge input %d", inputIx.Input)
				}
				walky.AppendNode(inputs, input)
			}
		}
	}
	return node, nil
}

func (g graphState) yamlDiffOp(op *pb.Op, d *pb.DiffOp) (*yaml.Node, error) {
	node := walky.NewMappingNode()
	yamlAddKV(node, "type", "DIFF")
	if d.Lower.Input < 0 || int(d.Lower.Input) >= len(op.Inputs) {
		return nil, errors.Errorf("invalid diff op, lower index %d of with %d inputs", d.Lower.Input, len(op.Inputs))
	}
	lower, err := g.visit(op.Inputs[d.Lower.Input])
	if err != nil {
		return nil, err
	}
	yamlMapAdd(node, walky.NewStringNode("lower"), lower)

	if d.Upper.Input < 0 || int(d.Upper.Input) >= len(op.Inputs) {
		return nil, errors.Errorf("invalid diff op, upper index %d of with %d inputs", d.Upper.Input, len(op.Inputs))
	}
	upper, err := g.visit(op.Inputs[d.Upper.Input])
	if err != nil {
		return nil, err
	}
	yamlMapAdd(node, walky.NewStringNode("upper"), upper)
	return node, nil
}
