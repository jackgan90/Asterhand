package quickinput

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

// LogWindowHost owns viewing surfaces only. The command service remains the
// authority for execution, logs and stopping across window/host reconstruction.
type LogWindowHost interface {
	Open(context.Context, string) error
	Update(string, string)
	Close() error
}

type logHost struct {
	mu                  sync.Mutex
	home, theme, locale string
	call                CommandHandler
	launch              func() (hostProcess, error)
	connection          *logConnection
	connections         map[*logConnection]struct{}
	closed              bool
}
type logConnection struct {
	process  hostProcess
	session  string
	out      chan hostipc.Message
	done     chan struct{}
	ready    chan struct{}
	stopped  chan struct{}
	reaped   chan struct{}
	stopErr  error
	requests sync.Map
	sequence atomic.Uint64
	once     sync.Once
}

func newLogWindowHost(home string, call CommandHandler, launch func() (hostProcess, error)) LogWindowHost {
	return &logHost{home: home, theme: "star", locale: i18n.Effective(), call: call, launch: launch, connections: make(map[*logConnection]struct{})}
}

func (h *logHost) connect() (*logConnection, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, i18n.Err("err.quickinput.window_closed")
	}
	if c := h.connection; c != nil {
		select {
		case <-c.done:
			h.connection = nil
		default:
			return c, nil
		}
	}
	p, err := h.launch()
	if err != nil {
		return nil, err
	}
	c := &logConnection{process: p, session: model.ID(), out: make(chan hostipc.Message, 32), done: make(chan struct{}), ready: make(chan struct{}), stopped: make(chan struct{}), reaped: make(chan struct{})}
	h.connection = c
	h.connections[c] = struct{}{}
	go h.reap(c)
	go c.writeLoop()
	go h.readLoop(c)
	go func() { <-p.Exited(); c.close() }()
	if err = c.send(hostipc.Message{Kind: hostipc.Initialize, Home: h.home, Theme: h.theme, Locale: h.locale, Epoch: 1, AppVersion: config.Version, Source: config.SourceDigest}); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (h *logHost) Open(ctx context.Context, id string) error {
	if !commandswire.ValidID(id) {
		return hostipc.ErrProtocol
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	anchor := logOpenAnchor(ctx)
	c, err := h.connect()
	if err != nil {
		return err
	}
	select {
	case <-c.ready:
	case <-c.done:
		return i18n.Err("err.quickinput.host_exited")
	case <-ctx.Done():
		c.close()
		return ctx.Err()
	}
	_ = c.process.GrantForeground()
	seq := c.sequence.Add(1)
	reply := make(chan error, 1)
	c.requests.Store(seq, reply)
	defer c.requests.Delete(seq)
	if err = c.send(hostipc.Message{Kind: hostipc.OpenLog, Request: seq, WindowID: id, Anchor: anchor}); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-c.done:
		return i18n.Err("err.quickinput.host_exited")
	case <-ctx.Done():
		c.close()
		return ctx.Err()
	}
}

func (h *logHost) Update(theme, locale string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.theme, h.locale = theme, locale
	if c := h.connection; c != nil {
		_ = c.send(hostipc.Message{Kind: hostipc.Theme, Theme: theme, Request: c.sequence.Add(1)})
		_ = c.send(hostipc.Message{Kind: hostipc.Locale, Locale: locale, Request: c.sequence.Add(1)})
	}
}
func (h *logHost) Close() error {
	h.mu.Lock()
	h.closed = true
	connections := make([]*logConnection, 0, len(h.connections))
	for c := range h.connections {
		connections = append(connections, c)
	}
	h.mu.Unlock()
	for _, c := range connections {
		c.close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostStopTimeout)
	defer cancel()
	var result error
	for _, c := range connections {
		select {
		case <-c.reaped:
			result = errors.Join(result, c.stopErr)
		case <-ctx.Done():
			return errors.Join(result, i18n.Err("err.quickinput.host_exit_unconfirmed"))
		}
	}
	return result
}

func (h *logHost) reap(c *logConnection) {
	defer close(c.reaped)
	<-c.stopped
	if c.stopErr == nil {
		// The generated session is the only profile subtree this connection owns.
		if err := os.RemoveAll(filepath.Join(h.home, "command-log-profiles", c.session)); err != nil {
			log.Printf("command_log_window stage=profile_cleanup_failed session=%s", c.session)
		}
	}
	h.mu.Lock()
	// Failed retirement remains owned so a later node Close reports the
	// unconfirmed process exit instead of silently treating it as success.
	if c.stopErr == nil {
		delete(h.connections, c)
	}
	h.mu.Unlock()
}

func (c *logConnection) close() {
	c.once.Do(func() {
		close(c.done)
		go func() {
			c.stopErr = c.process.Stop()
			close(c.stopped)
		}()
	})
}
func (c *logConnection) send(m hostipc.Message) error {
	m.Version, m.Session = hostipc.Version, c.session
	select {
	case <-c.done:
		return i18n.Err("err.quickinput.host_exited")
	default:
	}
	select {
	case c.out <- m:
		return nil
	default:
		return i18n.Err("err.quickinput.surface_busy_retry")
	}
}
func (c *logConnection) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case m := <-c.out:
			if hostipc.Write(c.process.Input(), m, false) != nil {
				c.close()
				return
			}
		}
	}
}

func (h *logHost) readLoop(c *logConnection) {
	defer c.close()
	// Bound service work even if a renderer floods valid requests.
	workers := make(chan struct{}, 4)
	stoppers := make(chan struct{}, 2)
	for {
		m, err := hostipc.Read(c.process.Output(), true)
		if err != nil || m.Session != c.session {
			return
		}
		switch m.Kind {
		case hostipc.Ready:
			if m.AppVersion != config.Version || m.Source != config.SourceDigest {
				return
			}
			select {
			case <-c.ready:
				return
			default:
				close(c.ready)
			}
		case hostipc.LogProgress:
			e := m.LogEvent
			log.Printf("command_log_window stage=%s command_id=%s session=%s elapsed_ms=%d code=%s", e.Stage, e.ID, c.session, e.ElapsedMS, e.Code)
		case hostipc.State:
			if value, ok := c.requests.Load(m.Request); ok {
				var err error
				if m.Code != "" {
					err = i18n.Err("err.localcmd.log_window_failed")
				}
				select {
				case value.(chan error) <- err:
				default:
				}
			}
		case hostipc.CommandCall:
			// The log host cannot start commands, browse or open arbitrary UI.
			if m.Command.Action != "status" && m.Command.Action != "read_log" && m.Command.Action != "tail_log" && m.Command.Action != "stop" {
				return
			}
			pool := workers
			if m.Command.Action == "stop" {
				pool = stoppers
			}
			select {
			case pool <- struct{}{}:
			default:
				reply := commandswire.Response{Error: commandswire.FailKey("busy", "err.quickinput.surface_busy_retry")}
				_ = c.send(hostipc.Message{Kind: hostipc.CommandResult, CommandID: m.CommandID, CommandReply: &reply})
				continue
			}
			go func(m hostipc.Message) {
				defer func() { <-pool }()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				reply := h.call(ctx, *m.Command)
				encoded, err := json.Marshal(reply)
				if err != nil || len(encoded) > 96*1024 {
					reply = commandswire.Response{Error: commandswire.FailKey("unavailable", "err.localcmd.log_window_failed")}
				}
				_ = c.send(hostipc.Message{Kind: hostipc.CommandResult, CommandID: m.CommandID, CommandReply: &reply})
			}(m)
		default:
			return
		}
	}
}
