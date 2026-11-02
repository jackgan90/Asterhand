//go:build windows

package quickinput

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

const logHiddenLimit = 4
const logSurfaceLimit = 16

type logWindowEntry struct {
	surface       *nativeSurface
	shown, failed bool
	used          uint64
	firstRequest  uint64
}
type logPendingOpen struct {
	entry  *logWindowEntry
	anchor *hostipc.WindowAnchor
}
type logRuntimeEvent struct {
	entry   *logWindowEntry
	kind    string
	request uint64
	err     error
}
type logRuntime struct {
	ctx         context.Context
	peer        *hostPeer
	initial     hostipc.Message
	windows     map[string]*logWindowEntry
	retiring    map[*logWindowEntry]bool
	pending     map[uint64]logPendingOpen
	events      chan logRuntimeEvent
	routes      sync.Map
	sequence    uint64
	initTimeout time.Duration
}

func serveLogHost(input io.Reader, output io.Writer, initial hostipc.Message) error {
	if err := checkHostRuntimePolicy(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &logRuntime{ctx: ctx, peer: newHostPeer(ctx, input, output, initial.Session), initial: initial,
		windows: make(map[string]*logWindowEntry), retiring: make(map[*logWindowEntry]bool),
		pending: make(map[uint64]logPendingOpen), events: make(chan logRuntimeEvent, 128), initTimeout: 15 * time.Second}
	defer r.close()
	if initial.Locale != "" {
		i18n.SetDefaultPreference(initial.Locale)
	}
	if err := r.peer.send(hostipc.Message{Kind: hostipc.Ready, AppVersion: config.Version, Source: config.SourceDigest, Runtime: "log-host"}); err != nil {
		return err
	}
	for {
		select {
		case err := <-r.peer.failures:
			return err
		case event := <-r.events:
			r.event(event)
		case m := <-r.peer.in:
			if m.Kind == hostipc.Quit {
				return nil
			}
			if err := r.message(m); err != nil {
				return err
			}
		}
	}
}

func (r *logRuntime) message(m hostipc.Message) error {
	switch m.Kind {
	case hostipc.OpenLog:
		r.open(m)
	case hostipc.CommandResult:
		if value, ok := r.routes.LoadAndDelete(m.CommandID); ok {
			s := value.(*nativeSurface)
			s.log.inFlight.Add(-1)
			_ = s.dispatch(r.ctx, func() {
				if !s.closing && !s.log.failed.Load() && s.core != nil {
					_ = s.postJSON(map[string]any{"kind": "command_result", "id": m.CommandID, "response": m.CommandReply})
				}
			})
		}
	case hostipc.Theme:
		r.initial.Theme = m.Theme
		for _, entry := range r.windows {
			_ = entry.surface.Update(m.Theme)
		}
	case hostipc.Locale:
		r.initial.Locale = m.Locale
		i18n.SetDefaultPreference(m.Locale)
		for _, entry := range r.windows {
			_ = entry.surface.UpdateLocale(m.Locale)
		}
	default:
		return hostipc.ErrProtocol
	}
	return nil
}

func (r *logRuntime) open(m hostipc.Message) {
	if len(r.pending) >= 32 {
		r.reply(m.Request, i18n.Err("err.quickinput.surface_busy_retry"))
		return
	}
	if _, duplicate := r.pending[m.Request]; duplicate {
		r.peer.fail(hostipc.ErrProtocol)
		return
	}
	entry := r.windows[m.WindowID]
	if entry != nil && (entry.failed || entry.surface.log.failed.Load()) {
		r.retire(entry)
		entry = nil
	}
	if entry == nil {
		if len(r.windows)+len(r.retiring) >= logSurfaceLimit {
			r.reply(m.Request, i18n.Err("err.quickinput.surface_busy_retry"))
			return
		}
		entry = &logWindowEntry{firstRequest: m.Request}
		log := &logSurface{id: m.WindowID, session: r.initial.Session, anchor: m.Anchor, shown: make(chan error, 1),
			started: time.Now(), send: r.peer.send, routes: &r.routes}
		log.hidden = func() { r.emit(logRuntimeEvent{entry: entry, kind: "hidden"}) }
		entry.surface = newWindowSurface(r.initial.Home, nil, func(err error) { r.emit(logRuntimeEvent{entry: entry, kind: "failed", err: err}) }, nil, log)
		entry.surface.theme, entry.surface.locale = r.initial.Theme, r.initial.Locale
		r.windows[m.WindowID] = entry
		go entry.surface.run()
		go r.watch(entry)
	}
	r.sequence++
	entry.used = r.sequence
	r.pending[m.Request] = logPendingOpen{entry: entry, anchor: m.Anchor}
	if entry.shown {
		r.activate(entry, m.Request, m.Anchor)
	}
}

func (r *logRuntime) emit(event logRuntimeEvent) {
	select {
	case r.events <- event:
	case <-r.ctx.Done():
	}
}

// One watcher owns initialization deadlines. Native creation and disposal never
// run on the host's IPC loop, so a stuck browser cannot block another Open.
func (r *logRuntime) watch(entry *logWindowEntry) {
	s := entry.surface
	deadline := time.NewTimer(r.initTimeout)
	defer deadline.Stop()
	shown, ready, timeout := s.log.shown, s.ready, deadline.C
	for {
		select {
		case err := <-shown:
			shown = nil
			r.emit(logRuntimeEvent{entry: entry, kind: "shown", err: err})
		case err := <-ready:
			ready, timeout = nil, nil
			deadline.Stop()
			if err != nil {
				r.emit(logRuntimeEvent{entry: entry, kind: "failed", err: err})
			}
		case <-timeout:
			ready, timeout = nil, nil
			r.emit(logRuntimeEvent{entry: entry, kind: "failed", err: i18n.Err("err.quickinput.init_timeout")})
		case <-s.done:
			if s.closeError != nil {
				// A native window disappearing does not confirm its browser exit.
				// Retire the host Job so failed cleanup cannot accumulate browsers.
				r.peer.terminalFailure("cleanup_unconfirmed")
				r.peer.fail(s.closeError)
			}
			r.emit(logRuntimeEvent{entry: entry, kind: "closed"})
			return
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *logRuntime) activate(entry *logWindowEntry, request uint64, anchor *hostipc.WindowAnchor) {
	err := entry.surface.dispatch(r.ctx, func() {
		err := entry.surface.showLogWindow(anchor)
		r.emit(logRuntimeEvent{entry: entry, kind: "activated", request: request, err: err})
	})
	if err != nil {
		r.emit(logRuntimeEvent{entry: entry, kind: "activated", request: request, err: err})
	}
}

func (r *logRuntime) event(event logRuntimeEvent) {
	entry, s := event.entry, event.entry.surface
	if event.kind == "closed" {
		delete(r.retiring, entry)
		if r.windows[s.log.id] == entry {
			delete(r.windows, s.log.id)
			r.failPending(entry)
		}
		r.clearRoutes(s)
		return
	}
	// Late controller callbacks and disposal from an older generation must never
	// affect the replacement for the same command.
	if r.windows[s.log.id] != entry {
		return
	}
	switch event.kind {
	case "shown":
		if event.err != nil {
			r.fail(entry, event.err)
			return
		}
		entry.shown = true
		s.logProgress("window_shown", "")
		if !entry.failed {
			for request, pending := range r.pending {
				if pending.entry == entry {
					if request == entry.firstRequest && s.log.visible.Load() {
						// createWindow already showed and painted this request's
						// native shell. No browser work belongs in its ACK path.
						delete(r.pending, request)
						r.reply(request, nil)
					} else {
						r.activate(entry, request, pending.anchor)
					}
				}
			}
		}
	case "activated":
		if pending, ok := r.pending[event.request]; ok && pending.entry == entry {
			delete(r.pending, event.request)
			if entry.failed {
				event.err = i18n.Err("err.localcmd.log_window_failed")
			}
			r.reply(event.request, event.err)
		}
	case "failed":
		r.fail(entry, event.err)
	case "hidden":
		s.logProgress("hidden", "")
		if entry.failed {
			r.retire(entry)
		} else {
			r.trimHidden()
		}
	}
}

func (r *logRuntime) fail(entry *logWindowEntry, err error) {
	if entry.failed {
		return
	}
	entry.failed = true
	entry.surface.log.failed.Store(true)
	entry.surface.logProgress("initialization_failed", logFailureCode(err, entry.surface.log.id))
	_ = entry.surface.dispatch(r.ctx, entry.surface.showLogFailure)
	r.failPending(entry)
}

func logFailureCode(err error, id string) string {
	if key, args, ok := i18n.ErrorDetails(err); ok {
		code := key
		if value := args["code"]; value != "" {
			code += ":" + value
		}
		if (hostipc.LogWindowEvent{ID: id, Stage: "initialization_failed", Code: code}).Valid() {
			return code
		}
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return fmt.Sprintf("win32:%x", uint32(errno))
	}
	return "initialization_error"
}

func (r *logRuntime) failPending(entry *logWindowEntry) {
	for request, pending := range r.pending {
		if pending.entry == entry {
			delete(r.pending, request)
			r.reply(request, i18n.Err("err.localcmd.log_window_failed"))
		}
	}
}
func (r *logRuntime) reply(request uint64, err error) {
	code := ""
	if err != nil {
		code = "window_failed"
	}
	if sendErr := r.peer.send(hostipc.Message{Kind: hostipc.State, Request: request, Code: code}); sendErr != nil {
		r.peer.fail(sendErr)
	}
}
func (r *logRuntime) clearRoutes(s *nativeSurface) {
	r.routes.Range(func(key, value any) bool {
		if value == s {
			r.routes.Delete(key)
		}
		return true
	})
}
func (r *logRuntime) retire(entry *logWindowEntry) {
	s := entry.surface
	if r.windows[s.log.id] != entry {
		return
	}
	delete(r.windows, s.log.id)
	r.retiring[entry] = true
	s.log.failed.Store(true)
	r.failPending(entry)
	r.clearRoutes(s)
	s.logProgress("retired", "")
	_ = s.dispatch(r.ctx, func() { s.hide(false); s.log.visible.Store(false) })
	go func() { _ = s.Close() }()
}
func (r *logRuntime) trimHidden() {
	for {
		count := 0
		var oldest *logWindowEntry
		for _, entry := range r.windows {
			if !entry.surface.log.visible.Load() {
				count++
				if oldest == nil || entry.used < oldest.used {
					oldest = entry
				}
			}
		}
		if count <= logHiddenLimit {
			return
		}
		r.retire(oldest)
	}
}
func (r *logRuntime) close() {
	var wait sync.WaitGroup
	closeEntry := func(entry *logWindowEntry) { wait.Add(1); go func() { defer wait.Done(); _ = entry.surface.Close() }() }
	for _, entry := range r.windows {
		closeEntry(entry)
	}
	for entry := range r.retiring {
		closeEntry(entry)
	}
	done := make(chan struct{})
	go func() { wait.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(hostStopTimeout):
	}
}
