package quickinput

import (
	"context"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

// remoteSurface owns one host generation. It never respawns itself; Controller
// serializes replacement with settings transactions and registration lifetime.
type remoteSurface struct {
	process                                 hostProcess
	draft                                   *Draft
	session                                 string
	epoch                                   uint64
	out                                     chan hostipc.Message
	done                                    chan struct{}
	ready                                   chan error
	finishOnce                              sync.Once
	closeOnce                               sync.Once
	mu                                      sync.Mutex
	closing, initialized                    bool
	err, closeErr                           error
	runtime                                 string
	request, lastSequence, acceptedRevision uint64
	lastText                                string
	waiters                                 map[uint64]chan hostipc.Message
	onFailure                               func(error)
	chat                                    *chatController
	commands                                CommandHandler
	commandSequence                         uint64
}

func startRemoteSurface(home, theme string, draft *Draft, launch hostLauncher, failed func(error), recovered ...bool) (*remoteSurface, error) {
	process, err := launch()
	if err != nil {
		return nil, err
	}
	text, revision, epoch := draft.state()
	s := &remoteSurface{process: process, draft: draft, session: model.ID(), epoch: epoch,
		out: make(chan hostipc.Message, 32), done: make(chan struct{}), ready: make(chan error, 1),
		waiters: map[uint64]chan hostipc.Message{}, onFailure: failed, lastSequence: revision, lastText: text, acceptedRevision: revision}
	go s.writeLoop()
	go s.readLoop()
	go func() { <-process.Exited(); s.finish(hostFailure("host_exited")) }()
	notice := len(recovered) != 0 && recovered[0]
	err = s.send(hostipc.Message{Kind: hostipc.Initialize, Home: home, Theme: theme, Locale: i18n.Effective(), Text: text, Revision: revision, Epoch: epoch, AppVersion: config.Version, Source: config.SourceDigest, Recovered: notice})
	if err == nil {
		select {
		case err = <-s.ready:
		case <-s.done:
			err = s.failure()
		case <-time.After(hostStartTimeout):
			err = hostFailure("host_start_timeout")
		}
	}
	if err != nil {
		if closeErr := s.Close(); closeErr != nil {
			return s, i18n.Wrap("err.quickinput.combined_close", map[string]string{"first": err.Error(), "second": closeErr.Error()}, closeErr)
		}
		return nil, err
	}
	return s, nil
}

func (s *remoteSurface) send(m hostipc.Message) error {
	m.Version, m.Session = hostipc.Version, s.session
	select {
	case <-s.done:
		return s.failure()
	default:
	}
	select {
	case s.out <- m:
		return nil
	case <-s.done:
		return s.failure()
	default:
		s.finish(hostFailure("host_queue_full"))
		return hostFailure("host_queue_full")
	}
}

func (s *remoteSurface) writeLoop() {
	for {
		select {
		case m := <-s.out:
			if err := hostipc.Write(s.process.Input(), m, false); err != nil {
				s.finish(hostFailure("host_disconnected"))
				return
			}
		case <-s.done:
			return
		}
	}
}

func (s *remoteSurface) readLoop() {
	for {
		m, err := hostipc.Read(s.process.Output(), true)
		if err != nil {
			s.finish(hostFailure("host_disconnected"))
			return
		}
		if m.Session != s.session {
			s.finish(hostFailure("host_protocol_invalid"))
			return
		}
		if err := s.receive(m); err != nil {
			s.finish(err)
			return
		}
	}
}

func (s *remoteSurface) receive(m hostipc.Message) error {
	if m.Kind == hostipc.Ready {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.initialized || m.AppVersion != config.Version || m.Source != config.SourceDigest {
			return hostFailure("host_protocol_invalid")
		}
		s.initialized, s.runtime = true, m.Runtime
		s.ready <- nil
		return nil
	}
	if m.Kind == hostipc.Failure {
		err := hostFailure(m.Code)
		if m.Code == "focus_denied" {
			if s.onFailure != nil {
				s.onFailure(err)
			}
			return nil
		}
		return err
	}
	s.mu.Lock()
	initialized := s.initialized
	s.mu.Unlock()
	if !initialized {
		return hostFailure("host_protocol_invalid")
	}
	switch m.Kind {
	case hostipc.CommandCall:
		return s.commandCall(m)
	case hostipc.Edit:
		return s.acceptEdit(m)
	case hostipc.ChatCall:
		s.mu.Lock()
		valid := !s.closing && s.err == nil && m.Epoch == s.epoch && m.Sequence <= s.lastSequence && m.Revision <= s.acceptedRevision
		chat := s.chat
		s.mu.Unlock()
		if !valid {
			return hostFailure("host_protocol_invalid")
		}
		if chat == nil {
			return s.send(hostipc.Message{Kind: hostipc.ChatResult, ChatID: m.ChatID, ChatReply: &hostipc.ChatReply{}})
		}
		chat.dispatch(s, m)
		return nil
	case hostipc.SnapshotDone, hostipc.State:
		s.mu.Lock()
		waiter := s.waiters[m.Request]
		if m.Kind == hostipc.SnapshotDone && (m.Sequence > s.lastSequence || m.Epoch != s.epoch || m.Revision > s.acceptedRevision) {
			s.mu.Unlock()
			return hostFailure("host_protocol_invalid")
		}
		delete(s.waiters, m.Request)
		s.mu.Unlock()
		if waiter != nil {
			waiter <- m
		}
		return nil
	default:
		return hostFailure("host_protocol_invalid")
	}
}

func (s *remoteSurface) acceptEdit(m hostipc.Message) error {
	s.mu.Lock()
	// A frame may have been read before Close/finish retired this session. The
	// shared draft can already belong to its replacement, even with the same epoch.
	if s.closing || s.err != nil {
		s.mu.Unlock()
		return hostFailure("host_disconnected")
	}
	if m.Epoch != s.epoch || m.Sequence < s.lastSequence || (m.Sequence == s.lastSequence && m.Text != s.lastText) {
		s.mu.Unlock()
		return hostFailure("host_protocol_invalid")
	}
	revision, err := s.draft.accept(m.Text, m.Epoch)
	chat := s.chat
	if err == nil {
		s.lastSequence, s.lastText, s.acceptedRevision = m.Sequence, m.Text, revision
	}
	s.mu.Unlock()
	if err != nil {
		return hostFailure("host_protocol_invalid")
	}
	if chat != nil {
		chat.edited(s.session, m.Sequence, revision, m.Text)
	}
	return s.send(hostipc.Message{Kind: hostipc.Accepted, Sequence: m.Sequence, Epoch: m.Epoch, Revision: revision})
}

func (s *remoteSurface) finish(err error) {
	s.finishOnce.Do(func() {
		s.mu.Lock()
		s.err = err
		notify := s.initialized && !s.closing
		s.mu.Unlock()
		close(s.done)
		_ = s.process.Input().Close()
		_ = s.process.Output().Close()
		if notify && s.onFailure != nil {
			s.onFailure(err)
		}
	})
}

func (s *remoteSurface) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return hostFailure("host_disconnected")
}

func (s *remoteSurface) Toggle() {
	if err := s.process.GrantForeground(); err != nil {
		if s.onFailure != nil {
			s.onFailure(err)
		}
		return
	}
	s.mu.Lock()
	s.request++
	id := s.request
	s.mu.Unlock()
	_ = s.send(hostipc.Message{Kind: hostipc.Toggle, Request: id})
}

func (s *remoteSurface) call(ctx context.Context, m hostipc.Message, expected hostipc.Kind) error {
	s.mu.Lock()
	if s.closing || len(s.waiters) >= 8 {
		s.mu.Unlock()
		return i18n.Err("err.quickinput.surface_busy")
	}
	s.request++
	m.Request = s.request
	waiter := make(chan hostipc.Message, 1)
	s.waiters[m.Request] = waiter
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.waiters, m.Request); s.mu.Unlock() }()
	if err := s.send(m); err != nil {
		return err
	}
	select {
	case result := <-waiter:
		if result.Kind != expected {
			return hostFailure("host_protocol_invalid")
		}
		if result.Code != "" {
			return hostFailure(result.Code)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return s.failure()
	}
}

func (s *remoteSurface) Update(theme string) error {
	if theme != "star" && theme != "frost" {
		return i18n.Err("err.quickinput.invalid_theme")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.call(ctx, hostipc.Message{Kind: hostipc.Theme, Theme: theme}, hostipc.State)
}

func (s *remoteSurface) UpdateLocale(locale string) error {
	if !i18n.ValidLocale(locale) {
		return i18n.Err("err.quickinput.prefs_invalid_locale")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return s.call(ctx, hostipc.Message{Kind: hostipc.Locale, Locale: locale}, hostipc.State)
}

func (s *remoteSurface) Snapshot(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.call(ctx, hostipc.Message{Kind: hostipc.Snapshot}, hostipc.SnapshotDone)
}

func (s *remoteSurface) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		if s.send(hostipc.Message{Kind: hostipc.Quit}) == nil {
			select {
			case <-s.process.Exited():
			case <-s.done:
			case <-time.After(hostStopTimeout):
			}
		}
		s.finish(hostFailure("host_disconnected"))
		s.closeErr = s.process.Stop()
	})
	return s.closeErr
}

func hostFailure(code string) error {
	keys := map[string]string{
		"focus_denied":                  "err.quickinput.focus_denied_tray",
		"host_exited":                   "err.quickinput.host_exited",
		"host_disconnected":             "err.quickinput.host_disconnected",
		"host_start_timeout":            "err.quickinput.host_start_timeout",
		"host_queue_full":               "err.quickinput.host_queue_full",
		"host_protocol_invalid":         "err.quickinput.host_protocol_invalid",
		"runtime_unavailable":           "err.quickinput.runtime_unavailable",
		"runtime_override":              "err.quickinput.runtime_override",
		"runtime_initialization_failed": "err.quickinput.runtime_init_failed",
		"renderer_failed":               "err.quickinput.renderer_failed",
		"snapshot_failed":               "err.quickinput.snapshot_ime",
	}
	key, ok := keys[code]
	if !ok {
		code, key = "host_protocol_invalid", keys["host_protocol_invalid"]
	}
	return &RegistrationError{Code: code, Detail: i18n.T(i18n.Effective(), key), Key: key}
}
