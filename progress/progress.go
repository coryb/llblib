package progress

import (
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"braces.dev/errtrace"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/opencontainers/go-digest"
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

// WithOutput is used to direct the buildkit output to an io.Writer or console.
func WithOutput(w io.Writer) Option {
	return progressOptionFunc(func(p *progress) {
		p.writer = w
	})
}

// DisplayMode is used to set the display mode for the progress.
type DisplayMode = progressui.DisplayMode

const (
	// DefaultMode is the default value for the DisplayMode.
	// This is effectively the same as AutoMode.
	DefaultMode = progressui.DefaultMode
	// AutoMode will choose TtyMode or PlainMode depending on if the output is
	// a tty.
	AutoMode DisplayMode = progressui.AutoMode
	// QuietMode discards all output.
	QuietMode DisplayMode = progressui.QuietMode
	// TtyMode enforces the output is a tty and will otherwise cause an error if it isn't.
	TtyMode DisplayMode = progressui.TtyMode
	// PlainMode is the human-readable plain text output. This mode is not meant to be read
	// by machines.
	PlainMode DisplayMode = progressui.PlainMode
	// RawJSONMode is the raw JSON text output. It will marshal the various solve status events
	// to JSON to be read by an external program.
	RawJSONMode DisplayMode = progressui.RawJSONMode
)

// WithMode is used to set the display mode for the progress.
func WithMode(m DisplayMode) Option {
	return progressOptionFunc(func(p *progress) {
		p.mode = m
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
		mode:     progressui.DefaultMode,
	}
	p.childCond.L = &p.mu
	for _, opt := range opts {
		opt.SetProgressOption(p)
	}
	p.start()
	return p
}

// Begin will send a new message to the running progress.  The returned function
// MUST be called to ensure the message is marked as completed.
func Begin(p Progress, message string) (end func()) {
	ch := p.Channel()

	dgst := digest.FromBytes([]byte(identity.NewID()))
	tm := time.Now()

	vtx := client.Vertex{
		Digest:  dgst,
		Name:    message,
		Started: &tm,
	}
	// copy so we can send a new vertex on completion
	vtx2 := vtx

	ch <- &client.SolveStatus{
		Vertexes: []*client.Vertex{&vtx},
	}

	return func() {
		defer close(ch)
		tm2 := time.Now()
		vtx2.Completed = &tm2
		ch <- &client.SolveStatus{
			Vertexes: []*client.Vertex{&vtx2},
		}
	}
}

type seenKey struct {
	Vertex     string
	DataLength int
	Timestamp  int64
}

type progress struct {
	statusCh chan *client.SolveStatus
	writer   io.Writer
	mode     DisplayMode

	children  int
	childCond sync.Cond
	mu        sync.Mutex
	done      chan error

	seenMu sync.Mutex
	seen   map[seenKey]struct{} // TODO this should probably be an LRU
}

func (p *progress) start() {
	go func() {
		d, err := progressui.NewDisplay(p.writer, p.mode)
		if err == nil {
			_, err = d.UpdateFrom(context.Background(), p.statusCh)
		}
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
	return errtrace.Wrap(<-p.done)
}

func (p *progress) Sync() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.sync(); err != nil {
		return errtrace.Wrap(err)
	}
	p.start()
	return nil
}

// sync is a helper function to share sync logic between Sync and Pause.
// The p.mu lock *must* be held before calling this
func (p *progress) sync() error {
	close(p.statusCh)
	if err := <-p.done; err != nil {
		return errtrace.Wrap(err)
	}
	// we are sync'd so lets restart
	p.done = make(chan error)
	p.statusCh = make(chan *client.SolveStatus)
	return nil
}

func (p *progress) Pause() error {
	p.mu.Lock()
	return errtrace.Wrap(p.sync())
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

// if we label an labeledProgress we should just append the new label to the
// existing label and return a new progress.
func (p labeledProgress) Label(l string) Progress {
	if p.label != "" && l != "" {
		l = p.label + " " + l
	}
	return labeledProgress{
		Progress: p.Progress,
		label:    l,
	}
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
