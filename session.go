package llblib

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/filesync"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
	"golang.org/x/sync/errgroup"
)

func NewSession(cwd string) *Session {
	return &Session{
		cwd:       cwd,
		localDirs: map[string]string{},
	}
}

type solveStates struct {
	state   llb.State
	exports []client.ExportEntry
}

type Session struct {
	cwd         string
	err         error
	localDirs   map[string]string
	attachables []session.Attachable

	solves []solveStates
}

func (l *Session) Local(name string, opts ...llb.LocalOption) llb.State {
	if l.err != nil {
		return llb.Scratch()
	}

	absPath := name
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(l.cwd, name)
	}

	fi, err := os.Stat(absPath)
	if err != nil {
		l.err = errors.Wrapf(err, "error reading %q", absPath)
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
		l.err = errors.Wrap(err, "error calculating id for local")
		return llb.Scratch()
	}

	opts = append(opts,
		llb.SharedKeyHint(id),
		llb.LocalUniqueID(id),
	)

	l.localDirs[name] = localDir

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

func (l *Session) Download(st llb.State, localDir string) {
	l.attachables = append(l.attachables, filesync.NewFSSyncTargetDir(localDir))
	l.solves = append(l.solves, solveStates{
		state: st,
		exports: []client.ExportEntry{{
			Type:      client.ExporterLocal,
			OutputDir: localDir,
		}},
	})
}

func (l *Session) Build(st llb.State) {
	l.solves = append(l.solves, solveStates{
		state: st,
	})
}

func (l *Session) Execute(ctx context.Context, cln *client.Client) error {
	if len(l.solves) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if l.err != nil {
		return errors.Wrap(l.err, "error occurred in llb state")
	}

	attachables := make([]session.Attachable, len(l.attachables))
	copy(attachables, l.attachables)

	dirSource := filesync.StaticDirSource{}
	for name, localDir := range l.localDirs {
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

	s, err := session.NewSession(ctx, "llblib", "")
	if err != nil {
		return err
	}
	for _, a := range attachables {
		s.Allow(a)
	}

	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		return s.Run(ctx, cln.Dialer())
	})

	var busy int32 = int32(len(l.solves))

	for _, solve := range l.solves {
		solve := solve
		def, err := solve.state.Marshal(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to marshal state")
		}

		ch := make(chan *client.SolveStatus)
		eg.Go(func() error {
			_, err := progressui.DisplaySolveStatus(context.TODO(), "", nil, os.Stdout, ch)
			return err
		})

		eg.Go(func() error {
			defer func() {
				if atomic.AddInt32(&busy, -1) == 0 {
					s.Close()
				}
			}()

			solveOpt := client.SolveOpt{
				SharedSession:         s,
				SessionPreInitialized: true,
				LocalDirs:             l.localDirs,
				Session:               attachables,
				Exports:               solve.exports,
			}

			if _, err = cln.Solve(ctx, def, solveOpt, ch); err != nil {
				return errors.Wrap(err, "solve failed")
			}
			return nil
		})

	}
	return eg.Wait()
}
