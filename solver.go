package llblib

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/coryb/llblib/progress"
	"github.com/coryb/llblib/sockproxy"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	bksess "github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/session/secrets/secretsprovider"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"golang.org/x/exp/maps"
	"golang.org/x/sync/errgroup"
)

type Solver interface {
	Local(name string, opts ...llb.LocalOption) llb.State
	Forward(src, dest string, opts ...ForwardOption) llb.RunOption
	AddSecretFile(src, dest string, opts ...llb.SecretOption) llb.RunOption

	Build(st llb.State, opts ...RequestOption) Request
	Container(root llb.State, opts ...ContainerOption) Request

	Resolver() llb.ImageMetaResolver
	NewSession(ctx context.Context, cln *client.Client, p progress.Progress) (Session, error)
}

type SolverOption interface {
	SetSolverOption(*solver)
}

type solverOptionFunc func(*solver)

func (f solverOptionFunc) SetSolverOption(s *solver) {
	f(s)
}

func WithCwd(cwd string) SolverOption {
	return solverOptionFunc(func(s *solver) {
		s.cwd = cwd
	})
}

func NewSolver(opts ...SolverOption) Solver {
	cwd, _ := os.Getwd()
	s := solver{
		cwd:          cwd,
		localDirs:    map[string]string{},
		secrets:      map[string]secretsprovider.Source{},
		agentConfigs: map[string]sockproxy.AgentConfig{},
	}
	for _, o := range opts {
		o.SetSolverOption(&s)
	}
	return &s
}

type Request struct {
	Label     string
	state     llb.State
	exports   []client.ExportEntry
	download  bksess.Attachable
	evaluate  bool
	buildFunc func(context.Context, gateway.Client) (*gateway.Result, error)
	onError   func(context.Context, gateway.Client, error) error
}

type solver struct {
	cwd          string
	err          error
	mu           sync.Mutex
	localDirs    map[string]string
	secrets      map[string]secretsprovider.Source
	downloads    []bksess.Attachable
	helpers      []func(ctx context.Context) (release func() error, err error)
	agentConfigs map[string]sockproxy.AgentConfig
	resolver     *resolver
}

var _ Solver = (*solver)(nil)

func (s *solver) Local(name string, opts ...llb.LocalOption) llb.State {
	if s.err != nil {
		return llb.Scratch()
	}

	absPath := name
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(s.cwd, name)
	}

	fi, err := os.Stat(absPath)
	if err != nil {
		s.err = errors.Wrapf(err, "error reading %q", absPath)
		return llb.Scratch()
	}

	localDir := absPath
	if !fi.IsDir() {
		filename := filepath.Base(absPath)
		localDir = filepath.Dir(absPath)

		// When localPath is a filename instead of a directory, include and exclude
		// patterns should be ignored.
		opts = append(opts,
			llb.IncludePatterns([]string{filename}),
			llb.ExcludePatterns([]string{}),
		)
	}

	id, err := localID(absPath, opts...)
	if err != nil {
		s.err = errors.Wrap(err, "error calculating id for local")
		return llb.Scratch()
	}

	opts = append(opts,
		llb.SharedKeyHint(id),
		llb.LocalUniqueID(id),
	)

	s.mu.Lock()
	s.localDirs[name] = localDir
	s.mu.Unlock()

	// Copy the local to scratch for better caching via buildkit
	return llb.Scratch().File(
		llb.Copy(llb.Local(name, opts...), "/", "/"),
		llb.WithCustomName(fmt.Sprintf("caching local://%s", name)),
	)
}

func localID(absPath string, opts ...llb.LocalOption) (string, error) {
	uniqID := localUniqueID(absPath)
	opts = append(opts, llb.LocalUniqueID(uniqID))
	st := llb.Local("", opts...)

	def, err := st.Marshal(context.Background())
	if err != nil {
		return "", errors.Wrap(err, "failed to marshal local state for ID")
	}

	// The terminal op of the graph def.Def[len(def.Def)-1] is an empty vertex with
	// an input to the last vertex's digest. Since that vertex also has its digests
	// of its inputs and so on, the digest of the terminal op is a merkle hash for
	// the graph.
	return digest.FromBytes(def.Def[len(def.Def)-1]).String(), nil
}

func localUniqueID(absPath string) string {
	mac := firstUpInterface()
	return fmt.Sprintf("cwd:%s,mac:%s", absPath, mac)
}

// firstUpInterface returns the mac address for the first "UP" network
// interface.
func firstUpInterface() string {
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // not up
		}
		if iface.HardwareAddr.String() == "" {
			continue // no mac
		}
		return iface.HardwareAddr.String()

	}
	return "no-valid-interface"
}

func (s *solver) AddSecretFile(src, dest string, opts ...llb.SecretOption) llb.RunOption {
	id := digest.FromString(src).String()
	s.mu.Lock()
	s.secrets[id] = secretsprovider.Source{
		ID:       id,
		FilePath: src,
	}
	s.mu.Unlock()
	opts = append(opts, llb.SecretID(id))
	return llb.AddSecret(dest, opts...)
}

type RequestOption interface {
	SetRequestOption(*Request)
}

type requestOptionFunc func(*Request)

func (f requestOptionFunc) SetRequestOption(r *Request) {
	f(r)
}

func WithLabel(l string) RequestOption {
	return requestOptionFunc(func(r *Request) {
		r.Label = l
	})
}

func Download(localDir string) RequestOption {
	return requestOptionFunc(func(r *Request) {
		r.exports = []client.ExportEntry{{
			Type:      client.ExporterLocal,
			OutputDir: localDir,
		}}
		r.download = filesync.NewFSSyncTargetDir(localDir)
	})
}

func (s *solver) Build(st llb.State, opts ...RequestOption) Request {
	r := Request{
		state: st,
	}
	for _, opt := range opts {
		opt.SetRequestOption(&r)
	}
	if r.download != nil {
		s.mu.Lock()
		s.downloads = append(s.downloads, r.download)
		s.mu.Unlock()
	}
	return r
}

func (s *solver) NewSession(ctx context.Context, cln *client.Client, p progress.Progress) (Session, error) {
	if s.err != nil {
		return nil, errors.Wrap(s.err, "solver in error state, cannot proceed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	releasers := []func() error{}
	for _, helper := range s.helpers {
		release, err := helper(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "solver helper failed")
		}
		releasers = append(releasers, release)
	}
	attachables := []bksess.Attachable{}

	dirSource := filesync.StaticDirSource{}
	for name, localDir := range s.localDirs {
		dirSource[name] = filesync.SyncedDir{
			Dir: localDir,
			Map: func(_ string, st *fstypes.Stat) fsutil.MapResult {
				st.Uid = 0
				st.Gid = 0
				return fsutil.MapResultKeep
			},
		}
	}
	attachables = append(attachables, filesync.NewFSSyncProvider(dirSource))

	// Attach secret providers to the session.
	if len(s.secrets) > 0 {
		fileStore, err := secretsprovider.NewStore(maps.Values(s.secrets))
		if err != nil {
			return nil, err
		}
		attachables = append(attachables, secretsprovider.NewSecretProvider(fileStore))
	}

	// Attach ssh forwarding providers to the session.
	if len(s.agentConfigs) > 0 {
		sp, err := sockproxy.NewProvider(maps.Values(s.agentConfigs))
		if err != nil {
			return nil, errors.Wrap(err, "failed to create provider for forward")
		}
		attachables = append(attachables, sp)
	}

	// for each download we need a uniq session.  This is a hack, there has been
	// some discussion for buildkit to have a session manager available to the
	// client to manage this eventually.
	allSessions := map[bksess.Attachable]*bksess.Session{}
	for _, attach := range append([]bksess.Attachable{nil}, s.downloads...) {
		bkSess, err := bksess.NewSession(ctx, "llblib", "")
		if err != nil {
			return nil, errors.Wrap(err, "failed to create buildkit session")
		}
		for _, a := range attachables {
			bkSess.Allow(a)
		}
		if attach != nil {
			bkSess.Allow(attach)
		}
		allSessions[attach] = bkSess
	}

	if s.resolver != nil {
		if err := s.resolver.start(ctx, cln, p); err != nil {
			return nil, errors.Wrap(err, "failed to start resolver")
		}
		releasers = append(releasers, s.resolver.stop)
	}

	eg, ctx := errgroup.WithContext(ctx)
	releasers = append(releasers, eg.Wait)
	for _, bkSess := range allSessions {
		bkSess := bkSess
		eg.Go(func() error {
			return bkSess.Run(ctx, cln.Dialer())
		})
	}

	// TODO wrap cln.Dialer to close a channel so we can synchronize
	// the sessions are running before continuing.

	localDirs := maps.Clone(s.localDirs)
	return &session{
		allSessions: allSessions,
		attachables: attachables,
		releasers:   releasers,
		client:      cln,
		localDirs:   localDirs,
		resolver:    s.resolver,
		progress:    p,
	}, nil
}
