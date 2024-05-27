package llblib

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"

	"braces.dev/errtrace"
	"github.com/coryb/llblib/sockproxy"
	"github.com/moby/buildkit/client/llb"
	"github.com/opencontainers/go-digest"
	"golang.org/x/sync/errgroup"
)

// ForwardOption is the same as the llb.SSHOption, but renamed since it can
// be used without SSH for generic forwarding of tcp and unix sockets.
type ForwardOption = llb.SSHOption

// Chmod is a wrapper over os.FileMode to implement various llb options.
type Chmod os.FileMode

var (
	_ ForwardOption    = (*Chmod)(nil)
	_ llb.CopyOption   = (*Chmod)(nil)
	_ llb.SecretOption = (*Chmod)(nil)
)

// WithChmod creates a Chmod option with the provided os.FileMode.
func WithChmod(mode os.FileMode) Chmod {
	return Chmod(mode)
}

// SetCopyOption implements the llb.CopyOption interface.
func (c Chmod) SetCopyOption(ci *llb.CopyInfo) {
	ci.Mode = (*os.FileMode)(&c)
}

// SetSSHOption implements the llb.SSHOption and ForwardOption interfaces.
func (c Chmod) SetSSHOption(si *llb.SSHInfo) {
	si.Mode = (int)(c)
}

// SetSecretOption implements the llb.SecretOption interface.
func (c Chmod) SetSecretOption(si *llb.SecretInfo) {
	si.Mode = (int)(c)
}

func (s *solver) Forward(src, dest string, opts ...ForwardOption) llb.RunOption {
	noop := RunOptions{}
	if s.err != nil {
		return noop
	}

	si := llb.SSHInfo{}
	for _, opt := range opts {
		opt.SetSSHOption(&si)
	}

	srcURL, err := url.Parse(src)
	if err != nil {
		s.err = errtrace.Errorf("unable to parse source for forward: %s: %w", src, err)
		return noop
	}

	var (
		id        = si.ID
		localPath string
	)

	switch srcURL.Scheme {
	case "unix":
		localPath = srcURL.Path
		if !filepath.IsAbs(localPath) {
			localPath = filepath.Join(s.cwd, srcURL.Path)
		}
		_, err = os.Stat(filepath.Dir(localPath))
		if err != nil {
			s.err = errtrace.Errorf("error reading directory for forward: %s: %w", localPath, err)
			return noop
		}
		if id == "" {
			id = digest.FromString(localPath).String()
		}
		s.mu.Lock()
		s.agentConfigs[id] = sockproxy.AgentConfig{
			ID:    id,
			SSH:   false,
			Paths: []string{localPath},
		}
		s.mu.Unlock()
	case "tcp":
		if id == "" {
			id = digest.FromString(src).String()
		}
		helper := func(ctx context.Context) (release func() error, err error) {
			dir, err := os.MkdirTemp("", "forward")
			if err != nil {
				return nil, errtrace.Errorf("failed to create tmp dir for forwarding sock: %w", err)
			}

			localPath = filepath.Join(dir, "proxy.sock")
			s.mu.Lock()
			s.agentConfigs[id] = sockproxy.AgentConfig{
				ID:    id,
				SSH:   false,
				Paths: []string{localPath},
			}
			s.mu.Unlock()

			dialerFunc := func() (net.Conn, error) {
				var dialer net.Dialer
				conn, err := dialer.DialContext(ctx, srcURL.Scheme, srcURL.Host)
				if err != nil {
					return nil, errtrace.Errorf("cannot dial %s: %w", src, err)
				}
				return conn, errtrace.Wrap(err)
			}

			var lc net.ListenConfig
			l, err := lc.Listen(ctx, "unix", localPath)
			if err != nil {
				return nil, errtrace.Errorf("failed to listen on forwarding sock: %w", err)
			}

			var g errgroup.Group

			release = func() error {
				s.mu.Lock()
				delete(s.agentConfigs, id)
				s.mu.Unlock()
				defer os.RemoveAll(dir)

				err := l.Close()
				if err != nil && !errors.Is(err, net.ErrClosed) {
					return errtrace.Errorf("failed to close listener: %w", err)
				}

				return errtrace.Wrap(g.Wait())
			}

			g.Go(func() error {
				err := sockproxy.Run(l, dialerFunc)
				if err != nil && !errors.Is(err, net.ErrClosed) {
					return errtrace.Wrap(err)
				}
				return nil
			})
			return release, nil
		}
		s.mu.Lock()
		s.helpers = append(s.helpers, helper)
		s.mu.Unlock()
	default:
		s.err = errtrace.Errorf("unsupported forward scheme %q in %q", srcURL.Scheme, src)
		return noop
	}

	opts = append([]ForwardOption{
		llb.SSHID(id),
		WithChmod(0o600),
		llb.SSHSocketTarget(dest),
	}, opts...)

	return llb.AddSSHSocket(opts...)
}
