package sockproxy

import (
	"context"
	"io"
	"net"
	"os"
	"time"

	"braces.dev/errtrace"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/sshforward"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// AgentConfig holds data necessary for to create a tcp forwarding agent.
type AgentConfig struct {
	// ID should be a digest string that uniquely identifies this config.
	ID string
	// Path is the list of paths to forward.  If empty, SSH_AUTH_SOCK will be
	// used unless SSH is false.
	Paths []string
	// SSH when true will fall back to forward the file located at the
	// SSH_AUTH_SOCK environment variable.
	SSH bool
}

// NewProvider creates a session provider for arbitrary socket forwarding by
// piggy-backing off the SSH forwarding protocol.
func NewProvider(confs []AgentConfig) (session.Attachable, error) {
	m := map[string]source{}
	for _, conf := range confs {
		if !conf.SSH {
			if len(conf.Paths) != 1 {
				return nil, errtrace.Errorf("must provide a path to a socket")
			}
			if _, ok := m[conf.ID]; ok {
				return nil, errtrace.Errorf("invalid duplicate ID %s", conf.ID)
			}
			m[conf.ID] = source{socket: conf.Paths[0]}
			continue
		}

		if len(conf.Paths) == 0 || len(conf.Paths) == 1 && conf.Paths[0] == "" {
			conf.Paths = []string{os.Getenv("SSH_AUTH_SOCK")}
		}

		if conf.Paths[0] == "" {
			return nil, errtrace.Errorf("invalid empty ssh agent socket, make sure SSH_AUTH_SOCK is set")
		}

		src, err := toAgentSource(conf.Paths)
		if err != nil {
			return nil, errtrace.Wrap(err)
		}
		if conf.ID == "" {
			conf.ID = sshforward.DefaultID
		}
		if _, ok := m[conf.ID]; ok {
			return nil, errtrace.Errorf("invalid duplicate ID %s", conf.ID)
		}
		m[conf.ID] = src
	}

	return &socketProvider{m: m}, nil
}

type source struct {
	ssh    bool
	agent  agent.Agent
	socket string
}

type socketProvider struct {
	m map[string]source
}

func (sp *socketProvider) Register(server *grpc.Server) {
	sshforward.RegisterSSHServer(server, sp)
}

func (sp *socketProvider) CheckAgent(ctx context.Context, req *sshforward.CheckAgentRequest) (*sshforward.CheckAgentResponse, error) {
	id := sshforward.DefaultID
	if req.ID != "" {
		id = req.ID
	}
	if _, ok := sp.m[id]; !ok {
		return &sshforward.CheckAgentResponse{}, errtrace.Errorf("unset ssh forward key %s", id)
	}
	return &sshforward.CheckAgentResponse{}, nil
}

func (sp *socketProvider) ForwardAgent(stream sshforward.SSH_ForwardAgentServer) error {
	id := sshforward.DefaultID

	opts, _ := metadata.FromIncomingContext(stream.Context()) // if no metadata continue with empty object

	if v, ok := opts[sshforward.KeySSHID]; ok && len(v) > 0 && v[0] != "" {
		id = v[0]
	}

	src, ok := sp.m[id]
	if !ok {
		return errtrace.Errorf("unset ssh forward key %s", id)
	}

	eg, ctx := errgroup.WithContext(context.TODO())

	var conn net.Conn
	if src.socket != "" {
		var err error
		conn, err = net.DialTimeout("unix", src.socket, time.Second)
		if err != nil {
			return errtrace.Errorf("failed to connect to %s: %w", src.socket, err)
		}
		defer conn.Close()
	}

	if !src.ssh {
		eg.Go(func() error {
			return errtrace.Wrap(sshforward.Copy(ctx, conn, stream, nil))
		})
	} else {
		var a agent.Agent

		if src.socket != "" {
			a = &readOnlyAgent{agent.NewClient(conn)}
		} else {
			a = src.agent
		}

		s1, s2 := sockPair()

		eg.Go(func() error {
			return errtrace.Wrap(agent.ServeAgent(a, s1))
		})

		eg.Go(func() error {
			defer s1.Close()
			return errtrace.Wrap(sshforward.Copy(ctx, s2, stream, nil))
		})
	}

	return errtrace.Wrap(eg.Wait())
}

func toAgentSource(paths []string) (source, error) {
	var keys bool
	var socket string
	a := agent.NewKeyring()
	for _, p := range paths {
		if socket != "" {
			return source{}, errtrace.New("only single socket allowed")
		}
		fi, err := os.Stat(p)
		if err != nil {
			return source{}, errtrace.Wrap(err)
		}
		if fi.Mode()&os.ModeSocket > 0 {
			if keys {
				return source{}, errtrace.Errorf("invalid combination of keys and sockets")
			}
			socket = p
			continue
		}
		keys = true
		f, err := os.Open(p)
		if err != nil {
			return source{}, errtrace.Errorf("failed to open %s: %w", p, err)
		}
		dt, err := io.ReadAll(&io.LimitedReader{R: f, N: 100 * 1024})
		if err != nil {
			return source{}, errtrace.Errorf("failed to read %s: %w", p, err)
		}

		k, err := ssh.ParseRawPrivateKey(dt)
		if err != nil {
			return source{}, errtrace.Errorf("failed to parse %s: %w", p, err) // TODO: prompt passphrase?
		}
		if err := a.Add(agent.AddedKey{PrivateKey: k}); err != nil {
			return source{}, errtrace.Errorf("failed to add %s to agent: %w", p, err)
		}
	}

	if socket != "" {
		return source{ssh: true, socket: socket}, nil
	}

	return source{ssh: true, agent: a}, nil
}

func sockPair() (io.ReadWriteCloser, io.ReadWriteCloser) {
	pr1, pw1 := io.Pipe()
	pr2, pw2 := io.Pipe()
	return &sock{pr1, pw2, pw1}, &sock{pr2, pw1, pw2}
}

type sock struct {
	io.Reader
	io.Writer
	io.Closer
}

type readOnlyAgent struct {
	agent.ExtendedAgent
}

func (a *readOnlyAgent) Add(_ agent.AddedKey) error {
	return errtrace.Errorf("adding new keys not allowed by buildkit")
}

func (a *readOnlyAgent) Remove(_ ssh.PublicKey) error {
	return errtrace.Errorf("removing keys not allowed by buildkit")
}

func (a *readOnlyAgent) RemoveAll() error {
	return errtrace.Errorf("removing keys not allowed by buildkit")
}

func (a *readOnlyAgent) Lock(_ []byte) error {
	return errtrace.Errorf("locking agent not allowed by buildkit")
}

func (a *readOnlyAgent) Extension(_ string, _ []byte) ([]byte, error) {
	return nil, errtrace.Errorf("extensions not allowed by buildkit")
}
