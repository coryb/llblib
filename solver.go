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
	"github.com/moby/buildkit/util/entitlements"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"golang.org/x/exp/maps"
	"golang.org/x/sync/errgroup"
)

// Solver provides functions used to create and execute buildkit solve requests.
type Solver interface {
	// AddSecretFile will add a secret file to the solve request and session.
	AddSecretFile(src, dest string, opts ...llb.SecretOption) llb.RunOption
	// Forward will add a forwarding proxy to the solve request and the session.
	// The `src` must be either formatted like `tcp://[host](:[port])` or
	// `unix://[path]`.  `dest` is the file location for the forwarded unix
	// domain socket in the build container.
	Forward(src, dest string, opts ...ForwardOption) llb.RunOption
	// Local will add a local directory to the solve request and session.
	Local(name string, opts ...llb.LocalOption) llb.State

	// Build creates a solve request that can be executed by the session
	// returned from `NewSession``.  Note that any Requests created after
	// `NewSession` will possibly be invalid, all requests should be generated
	// with `Build` BEFORE calling `NewSession`.
	Build(st llb.State, opts ...RequestOption) Request
	// Container will create a solve requests to run an ad-hoc container on the
	// buildkit service.
	Container(root llb.State, opts ...ContainerOption) Request

	// NewSession will return a session to used to send the solve requests to
	// buildkit.  Note that `Release` MUST be called on the returned `Session`
	// to free resources.
	NewSession(ctx context.Context, cln *client.Client, p progress.Progress) (Session, error)
}

// SolverOption can be used to modify how solve requests are generated.
type SolverOption interface {
	SetSolverOption(*solver)
}

type solverOptionFunc func(*solver)

func (f solverOptionFunc) SetSolverOption(s *solver) {
	f(s)
}

// WithCwd sets the working directory used when relative paths are provided
// to the `Solver`.
func WithCwd(cwd string) SolverOption {
	return solverOptionFunc(func(s *solver) {
		s.cwd = cwd
	})
}

// NewSolver returns a new Solver to create buildkit requests.
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

// Request defines a buildkit request.
type Request struct {
	// Label can be set to add a prefix to the progress display for this
	// request.
	Label string

	state        llb.State
	cwd          string
	exports      []client.ExportEntry
	download     bksess.Attachable
	evaluate     bool
	buildFunc    func(context.Context, gateway.Client) (*gateway.Result, error)
	onError      func(context.Context, gateway.Client, error) error
	entitlements []entitlements.Entitlement
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
	if !filepath.IsAbs(src) {
		src = filepath.Join(s.cwd, src)
	}
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

// RequestOption can be used to modify the requests.
type RequestOption interface {
	SetRequestOption(*Request)
}

type requestOptionFunc func(*Request)

func (f requestOptionFunc) SetRequestOption(r *Request) {
	f(r)
}

// WithLabel can be used to set the label used for the progress display of
// the request.
func WithLabel(l string) RequestOption {
	return requestOptionFunc(func(r *Request) {
		r.Label = l
	})
}

// WithInsecure will modify the request to ensure the solve request has
// the `Insecure` entitlement provided.
func WithInsecure() RequestOption {
	return requestOptionFunc(func(r *Request) {
		r.entitlements = append(r.entitlements, entitlements.EntitlementSecurityInsecure)
	})
}

// Download will trigger the buildkit exporter to export the solved state to the
// directory provided.  Only one `Download` option can be provided per request.
func Download(localDir string) RequestOption {
	return requestOptionFunc(func(r *Request) {
		if !filepath.IsAbs(localDir) {
			localDir = filepath.Join(r.cwd, localDir)
		}
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
		cwd:   s.cwd,
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
	if len(s.localDirs) > 0 {
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
	}

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

	resolver := newResolver(ctx, cln, p)
	releasers = append(releasers, resolver.stop)

	eg, ctx := errgroup.WithContext(ctx)
	releasers = append(releasers, eg.Wait)
	waiters := []chan struct{}{}
	for _, bkSess := range allSessions {
		bkSess := bkSess
		done := make(chan struct{})
		waiters = append(waiters, done)
		eg.Go(func() error {
			return bkSess.Run(ctx, func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
				defer close(done)
				return cln.Dialer()(ctx, proto, meta)
			})
		})
	}

	// ensure all sessions are running
	for _, waiter := range waiters {
		<-waiter
	}

	localDirs := maps.Clone(s.localDirs)
	return &session{
		allSessions: allSessions,
		attachables: attachables,
		releasers:   releasers,
		client:      cln,
		localDirs:   localDirs,
		resolver:    resolver,
		progress:    p,
	}, nil
}
