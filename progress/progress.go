package progress

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/containerd/console"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress/progressui"
)

type Progress interface {
	Release()
	Sync()
	Pause()
	Resume()
	Channel(opts ...ChannelOption) chan *client.SolveStatus
}

type ProgressOption interface {
	SetProgressOption(p *progress)
}

type progressOptionFunc func(p *progress)

func (f progressOptionFunc) SetProgressOption(p *progress) {
	f(p)
}

func WithConsole(c console.Console) ProgressOption {
	return progressOptionFunc(func(p *progress) {
		p.console = c
	})
}

func WithOutput(w io.Writer) ProgressOption {
	return progressOptionFunc(func(p *progress) {
		p.writer = w
	})
}

func NewProgress(opts ...ProgressOption) Progress {
	p := &progress{
		statusCh: make(chan *client.SolveStatus),
		done:     make(chan struct{}),
		writer:   os.Stdout,
		seen:     map[seenKey]struct{}{},
	}
	p.childCond.L = &p.mu
	for _, opt := range opts {
		opt.SetProgressOption(p)
	}
	p.start()
	return p
}

type seenKey struct {
	Vertex     string
	DataLength int
	Timestamp  int64
}

type progress struct {
	statusCh chan *client.SolveStatus
	console  console.Console
	writer   io.Writer

	children  int
	childCond sync.Cond
	mu        sync.Mutex
	done      chan struct{}

	seenMu sync.Mutex
	seen   map[seenKey]struct{} // TODO this should probably be an LRU
}

func (p *progress) start() {
	go func() {
		defer close(p.done)
		progressui.DisplaySolveStatus(context.Background(), "", p.console, p.writer, p.statusCh)
	}()
}

func (p *progress) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.children > 0 {
		p.childCond.Wait()
	}
	close(p.statusCh)
	<-p.done
}

func (p *progress) Sync() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sync()
	p.start()
}

// sync is a helper function to share sync logic between Sync and Pause.
// The p.mu lock *must* be held before calling this
func (p *progress) sync() {
	close(p.statusCh)
	<-p.done
	// we are sync'd so lets restart
	p.done = make(chan struct{})
	p.statusCh = make(chan *client.SolveStatus)
}

func (p *progress) Pause() {
	p.mu.Lock()
	p.sync()
}

func (p *progress) Resume() {
	p.start()
	p.mu.Unlock()
}

type ChannelOption interface {
	SetChannelOption(*channelOption)
}

type channelOptionFunc func(*channelOption)

func (f channelOptionFunc) SetChannelOption(co *channelOption) {
	f(co)
}

func Label(name string) ChannelOption {
	return channelOptionFunc(func(co *channelOption) {
		co.label = name
	})
}

type channelOption struct {
	label string
}

// Channel returns a new status channel.  This channel must be closed
// (usually by client.Solve/client.Build) before Release is called.
func (p *progress) Channel(opts ...ChannelOption) chan *client.SolveStatus {
	co := channelOption{}
	for _, opt := range opts {
		opt.SetChannelOption(&co)
	}
	ch := make(chan *client.SolveStatus)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.children++
	go func() {
		for msg := range ch {
			// first de-dup any incoming log vertices
			keepLogs := []*client.VertexLog{}
			p.seenMu.Lock()
			for _, l := range msg.Logs {
				key := seenKey{
					Vertex:     l.Vertex.String(),
					DataLength: len(l.Data),
					Timestamp:  l.Timestamp.UnixNano(),
				}
				if _, ok := p.seen[key]; ok {
					continue
				}
				p.seen[key] = struct{}{}
				keepLogs = append(keepLogs, l)
			}
			p.seenMu.Unlock()
			msg.Logs = keepLogs
			if co.label != "" {
				for _, v := range msg.Vertexes {
					v.Name = addLabel(co.label, v.Name)
				}
			}

			p.mu.Lock()
			p.statusCh <- msg
			p.mu.Unlock()
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.children--
		p.childCond.Signal()
	}()
	return ch
}

func addLabel(label, name string) string {
	if strings.HasPrefix(name, "[") {
		return "[" + label + " " + name[1:]
	}
	return "[" + label + "] " + name
}
