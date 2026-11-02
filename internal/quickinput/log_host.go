package quickinput

import (
	"context"
	"encoding/json"
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
	closed              bool
}
type logConnection struct {
	process  hostProcess
	session  string
	out      chan hostipc.Message
	done     chan struct{}
	ready    chan error
	requests sync.Map
	sequence atomic.Uint64
	once     sync.Once
}

func newLogWindowHost(home string, call CommandHandler, launch func() (hostProcess, error)) LogWindowHost {
	return &logHost{home: home, theme: "star", locale: i18n.Effective(), call: call, launch: launch}
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
	c := &logConnection{process: p, session: model.ID(), out: make(chan hostipc.Message, 32), done: make(chan struct{}), ready: make(chan error, 1)}
	h.connection = c
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
	c, err := h.connect()
	if err != nil {
		return err
	}
	select {
	case err = <-c.ready:
		c.ready <- err
		if err != nil {
			return err
		}
	case <-c.done:
		return i18n.Err("err.quickinput.host_exited")
	case <-ctx.Done():
		return ctx.Err()
	}
	_ = c.process.GrantForeground()
	seq := c.sequence.Add(1)
	reply := make(chan error, 1)
	c.requests.Store(seq, reply)
	defer c.requests.Delete(seq)
	if err = c.send(hostipc.Message{Kind: hostipc.OpenLog, Request: seq, WindowID: id}); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-c.done:
		return i18n.Err("err.quickinput.host_exited")
	case <-ctx.Done():
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
	c := h.connection
	h.mu.Unlock()
	if c != nil {
		c.close()
	}
	return nil
}
func (c *logConnection) close() { c.once.Do(func() { close(c.done); _ = c.process.Stop() }) }
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
			case c.ready <- nil:
			default:
				return
			}
		case hostipc.State:
			if value, ok := c.requests.Load(m.Request); ok {
				var err error
				if m.Code != "" {
					err = i18n.Err("err.localcmd.log_window_failed")
				}
				value.(chan error) <- err
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
