package llblib

import (
	"context"
	"io/ioutil"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/coryb/llblib/sockproxy"
	"github.com/moby/buildkit/client/llb"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

type ForwardOption = llb.SSHOption

type Chmod os.FileMode

var (
	_ ForwardOption    = (*Chmod)(nil)
	_ llb.CopyOption   = (*Chmod)(nil)
	_ llb.SecretOption = (*Chmod)(nil)
)

func WithChmod(mode os.FileMode) Chmod {
	return Chmod(mode)
}

func (c Chmod) SetCopyOption(ci *llb.CopyInfo) {
	ci.Mode = (*os.FileMode)(&c)
}

func (c Chmod) SetSSHOption(si *llb.SSHInfo) {
	si.Mode = (int)(c)
}

func (c Chmod) SetSecretOption(si *llb.SecretInfo) {
	si.Mode = (int)(c)
}

func (s *Session) Forward(src, dest string, opts ...ForwardOption) llb.RunOption {
	noop := RunOptions{}
	if s.err != nil {
		return noop
	}

	srcURL, err := url.Parse(src)
	if err != nil {
		s.err = errors.Wrapf(err, "unable to parse source for forward: %s", src)
		return noop
	}

	var (
		id        string
		localPath string
	)

	if srcURL.Scheme == "unix" {
		localPath = srcURL.Path
		if !filepath.IsAbs(localPath) {
			localPath = filepath.Join(s.cwd, srcURL.Path)
		}
		_, err = os.Stat(filepath.Dir(localPath))
		if err != nil {
			s.err = errors.Wrapf(err, "error reading directory for forward: %s", localPath)
			return noop
		}
		id = digest.FromString(localPath).String()
	} else if srcURL.Scheme == "tcp" {
		id = digest.FromString(src).String()
		dir, err := ioutil.TempDir("", "forward")
		if err != nil {
			s.err = errors.Wrap(err, "failed to create tmp dir for forwarding sock")
			return noop
		}

		localPath = filepath.Join(dir, "proxy.sock")
		helper := func(ctx context.Context) (release func() error, err error) {
			dialerFunc := func() (net.Conn, error) {
				var dialer net.Dialer
				conn, err := dialer.DialContext(ctx, srcURL.Scheme, srcURL.Host)
				if err != nil {
					return nil, errors.Wrapf(err, "cannot dial %s", src)
				}
				return conn, err
			}

			var lc net.ListenConfig
			l, err := lc.Listen(ctx, "unix", localPath)
			if err != nil {
				return nil, errors.Wrap(err, "failed to listen on forwarding sock")
			}

			var g errgroup.Group

			release = func() error {
				defer os.RemoveAll(dir)

				err := l.Close()
				if err != nil && !isClosedNetworkError(err) {
					return errors.Wrap(err, "failed to close listener")
				}

				return g.Wait()
			}

			g.Go(func() error {
				err := sockproxy.Run(l, dialerFunc)
				if err != nil && !isClosedNetworkError(err) {
					return err
				}
				return nil
			})
			return release, nil
		}
		s.helpers = append(s.helpers, helper)
	} else {
		s.err = errors.Errorf("unsupported forward scheme %q in %q", srcURL.Scheme, src)
		return noop
	}

	opts = append([]ForwardOption{
		llb.SSHID(id),
		WithChmod(0o600),
		llb.SSHSocketTarget(dest),
	}, opts...)

	s.agentConfigs[id] = sockproxy.AgentConfig{
		ID:    id,
		SSH:   false,
		Paths: []string{localPath},
	}

	return llb.AddSSHSocket(opts...)
}

func isClosedNetworkError(err error) bool {
	// ErrNetClosing is hidden in an internal golang package so we can't use
	// errors.Is: https://golang.org/src/internal/poll/fd.go
	return strings.Contains(err.Error(), "use of closed network connection")
}