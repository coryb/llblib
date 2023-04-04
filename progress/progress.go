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

// Progress defines functions for a buildkit progress display.
type Progress interface {
	// Channel returns a new buildkit client.SolveStatus channel used for
	// buildkit to send messages to the progress display.
	Channel(opts ...ChannelOption) chan *client.SolveStatus
	// Label returns a new Progress where all messages will have the provided
	// label prepended to displayed messages.  Close MUST be called on the
	// returned Progress to free resources.
	Label(string) Progress
	// Pause will sync the Progress, then prevent further messages from being
	// written to the display channels.  Resume MUST be called after Pause.
	Pause() error
	// Resume will unblock the display channels so messages will start
	// displaying after a Pause.
	Resume()
	// Close MUST be called to release resources used and ensures all in-flight
	// messages have been handled.
	Close() error
	// Sync will ensure all in-flight messages have been handled.
	Sync() error
}

// Option can be used to customize the progress display.
type Option interface {
	SetProgressOption(p *progress)
}

type progressOptionFunc func(p *progress)

func (f progressOptionFunc) SetProgressOption(p *progress) {
	f(p)
}

// WithConsole is used to direct the "tty" buildkit output to the provided
// console. Typically used like:
//
//	progress.NewProgress(progress.WithConsole(console.Current()))
func WithConsole(c console.Console) Option {
	return progressOptionFunc(func(p *progress) {
		p.console = c
	})
}

// WithOutput is used to direct the "plain" buildkit output to an io.Writer.
func WithOutput(w io.Writer) Option {
	return progressOptionFunc(func(p *progress) {
		p.writer = w
	})
}

// NewProgress creates a new Progress display to use with buildkit solves.
// Close MUST be called on the returned progress to clean-up resources.
func NewProgress(opts ...Option) Progress {
	p := &progress{
		statusCh: make(chan *client.SolveStatus),
		done:     make(chan error),
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
	done      chan error

	seenMu sync.Mutex
	seen   map[seenKey]struct{} // TODO this should probably be an LRU
}

func (p *progress) start() {
	go func() {
		_, err := progressui.DisplaySolveStatus(context.Background(), "", p.console, p.writer, p.statusCh)
		p.done <- err
		close(p.done)
	}()
}

func (p *progress) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.children > 0 {
		p.childCond.Wait()
	}
	close(p.statusCh)
	return <-p.done
}

func (p *progress) Sync() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.sync(); err != nil {
		return err
	}
	p.start()
	return nil
}

// sync is a helper function to share sync logic between Sync and Pause.
// The p.mu lock *must* be held before calling this
func (p *progress) sync() error {
	close(p.statusCh)
	if err := <-p.done; err != nil {
		return err
	}
	// we are sync'd so lets restart
	p.done = make(chan error)
	p.statusCh = make(chan *client.SolveStatus)
	return nil
}

func (p *progress) Pause() error {
	p.mu.Lock()
	return p.sync()
}

func (p *progress) Resume() {
	p.start()
	p.mu.Unlock()
}

// ChannelOption can be used to customize how a specific display channel handles
// messages.
type ChannelOption interface {
	SetChannelOption(*channelOption)
}

type channelOptionFunc func(*channelOption)

func (f channelOptionFunc) SetChannelOption(co *channelOption) {
	f(co)
}

// AddLabel will return a specific display channel where all messages will have
// the provided label prepended.
func AddLabel(l string) ChannelOption {
	return channelOptionFunc(func(co *channelOption) {
		if l == "" {
			return
		}
		if co.label == "" {
			co.label = l
			return
		}
		co.label += " " + l
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

func (p *progress) Label(l string) Progress {
	return labeledProgress{
		Progress: p,
		label:    l,
	}
}

type labeledProgress struct {
	Progress
	label string
}

func (p labeledProgress) Channel(opts ...ChannelOption) chan *client.SolveStatus {
	if p.label != "" {
		opts = append([]ChannelOption{AddLabel(p.label)}, opts...)
	}
	return p.Progress.Channel(opts...)
}

func (p labeledProgress) Close() error {
	return nil // no-op, dont release parent progress
}

func addLabel(label, name string) string {
	if strings.HasPrefix(name, "[") {
		return "[" + label + " " + name[1:]
	}
	return "[" + label + "] " + name
}
