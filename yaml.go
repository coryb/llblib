package llblib

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"braces.dev/errtrace"
	"github.com/coryb/walky"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/opencontainers/go-digest"
	"golang.org/x/exp/maps"
	"gopkg.in/yaml.v3"
)

// ToYAML will serialize the llb.States to a yaml sequence node where each
// node in the sequence represents the corresponding states passed in.
func ToYAML(ctx context.Context, states ...llb.State) (*yaml.Node, error) {
	counter := 0
	g := graphState{
		ops:          map[string]*pb.Op{},
		meta:         map[string]*pb.OpMetadata{},
		cache:        map[string]*yaml.Node{},
		outputs:      map[string]*yaml.Node{},
		aliases:      map[string]string{},
		aliasCounter: &counter,
	}

	nodes := walky.NewSequenceNode()
	for _, st := range states {
		def, err := st.Marshal(ctx)
		if err != nil {
			return nil, errtrace.Errorf("failed to marshal state: %w", err)
		}

		root, g, err := g.newGraph(def.ToPB())
		if err != nil {
			return nil, errtrace.Wrap(err)
		}
		if root == nil {
			// no error, so we must be solving llb.Scratch()
			walky.AppendNode(nodes, g.scratchNode())
			continue
		}

		node, err := g.visit(root)
		if err != nil {
			return nil, errtrace.Wrap(err)
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
	ops          map[string]*pb.Op
	meta         map[string]*pb.OpMetadata
	cache        map[string]*yaml.Node
	outputs      map[string]*yaml.Node
	aliases      map[string]string
	aliasCounter *int
}

func (g graphState) newGraph(def *pb.Definition) (*pb.Input, graphState, error) {
	ops := maps.Clone(g.ops)
	var dgst digest.Digest
	if def != nil {
		for _, dt := range def.Def {
			var pbOp pb.Op
			if err := (&pbOp).Unmarshal(dt); err != nil {
				return nil, graphState{}, errtrace.Wrap(err)
			}
			dgst = digest.FromBytes(dt)
			ops[dgst.String()] = &pbOp
		}
	}
	meta := maps.Clone(g.meta)
	if def != nil {
		for d, m := range def.Metadata {
			meta[d] = m
		}
	}

	if dgst == "" {
		return nil, graphState{
			meta:         meta,
			cache:        g.cache,
			aliases:      g.aliases,
			aliasCounter: g.aliasCounter,
		}, nil
	}
	terminal := ops[dgst.String()]
	return terminal.Inputs[0], graphState{
		ops:          ops,
		meta:         meta,
		cache:        g.cache,
		aliases:      g.aliases,
		aliasCounter: g.aliasCounter,
	}, nil
}

func (g graphState) anchorName(d string) string {
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
		ownerNode := walky.NewMappingNode()
		yamlMapAdd(n, walky.NewStringNode("owner"),
			applyOpts(ownerNode, opts...),
		)
		if owner.User != nil && owner.User.User != nil {
			switch u := owner.User.User.(type) {
			case *pb.UserOpt_ByName:
				yamlAddKV(ownerNode, "user", u.ByName.Name)
			case *pb.UserOpt_ByID:
				yamlAddInt(ownerNode, "user", int64(u.ByID))
			}
		}
		if owner.Group != nil && owner.Group.User != nil {
			switch u := owner.Group.User.(type) {
			case *pb.UserOpt_ByName:
				yamlAddKV(ownerNode, "group", u.ByName.Name)
			case *pb.UserOpt_ByID:
				yamlAddInt(ownerNode, "group", int64(u.ByID))
			}
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
		return nil, errtrace.Errorf("op %s not found", input.Digest)
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
			return nil, errtrace.Wrap(err)
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
		return node, errtrace.Wrap(err)
	case *pb.Op_File:
		return errtrace.Wrap2(g.yamlFileOp(input.Digest, op, v.File))
	case *pb.Op_Build:
		return errtrace.Wrap2(g.yamlBuildOp(op, v.Build))
	case *pb.Op_Merge:
		return errtrace.Wrap2(g.yamlMergeOp(op, v.Merge))
	case *pb.Op_Diff:
		return errtrace.Wrap2(g.yamlDiffOp(op, v.Diff))
	default:
		return nil, errtrace.Errorf("unexpected op type %T", op.Op)
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
			return nil, errtrace.Wrap(err)
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
		return nil, errtrace.Errorf("invalid op, impossible input %d from op with %d inputs", m.Input, len(op.Inputs))
	}

	input, err := g.visit(op.Inputs[m.Input])
	if err != nil {
		return nil, errtrace.Errorf("visiting mount %q: %w", m.Dest, err)
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

func (g graphState) yamlFileOp(dgst string, op *pb.Op, f *pb.FileOp) (*yaml.Node, error) {
	node := walky.NewMappingNode()
	yamlAddKV(node, "type", "FILE")
	actions := walky.NewSequenceNode()
	yamlMapAdd(node, walky.NewStringNode("actions"), actions)
	inputs := []*yaml.Node{}
	for i, act := range f.Actions {
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
			err = errtrace.Errorf("unexpected file action type %T", a)
		}
		if err != nil {
			return nil, errtrace.Wrap(err)
		}
		inputName := "input"
		if act.SecondaryInput >= 0 {
			// for copy we use src-input and dest-input
			inputName = "dest-input"
		}
		switch {
		case act.Input == -1:
			yamlMapAdd(action, walky.NewStringNode(inputName), g.scratchNode())
		case int(act.Input) < len(op.Inputs):
			input, err := g.visit(op.Inputs[act.Input])
			if err != nil {
				return nil, errtrace.Errorf("visiting copy input: %w", err)
			}
			yamlMapAdd(action, walky.NewStringNode(inputName), input)
		default:
			prevAction := inputs[int(act.Input)-len(op.Inputs)]
			input := yamlAliasOf(prevAction, g.anchorName(dgst)+"_"+strconv.Itoa(i))
			yamlMapAdd(action, walky.NewStringNode(inputName), input)
		}
		if act.SecondaryInput >= 0 {
			input, err := g.visit(op.Inputs[act.SecondaryInput])
			if err != nil {
				return nil, errtrace.Errorf("visiting copy source input: %w", err)
			}
			yamlMapAdd(action, walky.NewStringNode("src-input"), input)
		}
		walky.AppendNode(actions, action)
		inputs = append(inputs, action)
	}
	return node, nil
}

func (g graphState) yamlFileCopy(c *pb.FileActionCopy) *yaml.Node {
	cp := walky.NewMappingNode()
	yamlAddKV(cp, "type", "COPY")
	yamlAddKV(cp, "src", c.Src)
	yamlAddKV(cp, "dest", c.Dest)
	yamlAddOwner(cp, c.Owner)
	yamlAddMode(cp, c.Mode)
	yamlAddBool(cp, "follow-symlinks", c.FollowSymlink)
	yamlAddBool(cp, "contents-only", c.DirCopyContents)
	yamlAddBool(cp, "unpack-archive", c.AttemptUnpackDockerCompatibility)
	yamlAddBool(cp, "create-dest-path", c.CreateDestPath)
	yamlAddBool(cp, "allow-wildcard", c.AllowWildcard)
	yamlAddBool(cp, "allow-empty-wildcard", c.AllowEmptyWildcard)
	yamlAddTime(cp, c.Timestamp)
	yamlAddSeq(cp, "include-patterns", c.IncludePatterns)
	yamlAddSeq(cp, "exclude-patterns", c.ExcludePatterns)
	return cp
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

	inputs := walky.NewMappingNode()
	yamlMapAdd(node, walky.NewStringNode("inputs"), inputs)

	// builds have exactly one input named `buildkit.llb.definition`
	defInput, ok := b.Inputs[pb.LLBDefinitionInput]
	if !ok {
		return nil, errtrace.New("buildOp missing " + pb.LLBDefinitionInput + " input")
	}
	input, err := g.visit(op.Inputs[defInput.Input])
	if err != nil {
		return nil, errtrace.Errorf("visiting buildOp input %q: %w", pb.LLBDefinitionInput, err)
	}
	yamlMapAdd(inputs, walky.NewStringNode(pb.LLBDefinitionInput), input)

	// extract the MkFile op from the input so we can unmarshal the def
	// and build the execution graph for the buildOp
	inputOp := g.ops[op.Inputs[defInput.Input].Digest]
	file := inputOp.GetFile()
	if file == nil {
		return nil, errtrace.New("buildOp input is not a FILE")
	}
	if len(file.Actions) == 0 {
		return nil, errtrace.New("buildOp input has no actions")
	}
	action := file.Actions[0]
	mkfile := action.GetMkfile()
	if mkfile == nil {
		return nil, errtrace.New("buildOp input action is not a MKFILE")
	}
	defData := mkfile.GetData()

	var def pb.Definition
	if err := def.Unmarshal(defData); err != nil {
		return nil, errtrace.Errorf("unmarshalling definition: %w", err)
	}

	buildInput, buildGraph, err := g.newGraph(&def)
	if err != nil {
		return nil, errtrace.Errorf("building graph for buildOp: %w", err)
	}
	build, err := buildGraph.visit(buildInput)
	if err != nil {
		return nil, errtrace.Errorf("visiting build for buildOp: %w", err)
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
					return nil, errtrace.Errorf("visiting merge input %d: %w", inputIx.Input, err)
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
		return nil, errtrace.Errorf("invalid diff op, lower index %d of with %d inputs", d.Lower.Input, len(op.Inputs))
	}
	lower, err := g.visit(op.Inputs[d.Lower.Input])
	if err != nil {
		return nil, errtrace.Wrap(err)
	}
	yamlMapAdd(node, walky.NewStringNode("lower"), lower)

	if d.Upper.Input < 0 || int(d.Upper.Input) >= len(op.Inputs) {
		return nil, errtrace.Errorf("invalid diff op, upper index %d of with %d inputs", d.Upper.Input, len(op.Inputs))
	}
	upper, err := g.visit(op.Inputs[d.Upper.Input])
	if err != nil {
		return nil, errtrace.Wrap(err)
	}
	yamlMapAdd(node, walky.NewStringNode("upper"), upper)
	return node, nil
}
