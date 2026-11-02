//go:build windows

package quickinput

import (
	"context"
	"time"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
	"golang.org/x/sys/windows"
)

func NativeFactory(c *Controller) SurfaceFactory {
	return func() (Surface, error) {
		c.mu.Lock()
		generation := c.surfaceGeneration
		c.mu.Unlock()
		s, err := startRemoteSurface(c.home, c.Status().Desired.Theme, &c.draft, launchWindowsHost, func(err error) { c.failedGeneration(generation, err) }, c.Status().RecoveryNotice != "")
		if err == nil {
			s.mu.Lock()
			s.chat = c.chat
			s.commands = c.commands
			s.mu.Unlock()
			c.mu.Lock()
			c.status.RuntimeVersion = s.runtime
			c.mu.Unlock()
		}
		return s, err
	}
}

func (s *nativeSurface) bridgeMessage(raw string) {
	m, err := decodeBridgeMessage(raw)
	if err != nil {
		return
	}
	if m.Kind == "ready" && !s.documentReady {
		s.documentReady = true
		text, revision := s.draft.Read()
		if err := s.postJSON(map[string]any{"kind": "init", "generation": s.epoch, "text": text, "revision": revision, "theme": s.theme, "locale": s.locale}); err != nil {
			s.ready <- err
		}
		return
	}
	if m.Epoch != s.epoch || !s.documentReady {
		return
	}
	switch m.Kind {
	case "initialized":
		if !s.pageInitialized {
			s.pageInitialized = true
			s.suspend(func() { s.ready <- nil })
		}
	case "resize":
		if validSurfaceHeight(m.Height) && len(m.ID) <= 64 {
			s.layoutID = m.ID
			if m.Height != s.height {
				s.height = m.Height
				s.positionCurrent(nil)
			} else {
				s.postLayoutApplied()
			}
		}
	case "draft_updated", "snapshot":
		if err := s.draft.Update(m.Text, m.Revision); err != nil {
			msg := map[string]any{"kind": "error", "message": err.Error()}
			if key, args, ok := i18n.ErrorDetails(err); ok {
				msg["message_key"] = key
				if len(args) > 0 {
					msg["message_args"] = args
				}
			}
			_ = s.postJSON(msg)
			return
		}
		if s.remoteDraft != nil {
			id := ""
			var complete func()
			if m.Kind == "snapshot" {
				if _, ok := s.waiters[m.ID]; !ok {
					return
				}
				id = m.ID
				complete = func() { _ = s.dispatch(context.Background(), func() { s.completeSnapshot(id) }) }
			}
			s.remoteDraft.submit(m.Text, m.Revision, id, complete)
		} else if m.Kind == "snapshot" {
			s.completeSnapshot(m.ID)
		}
	case "hide":
		s.hide(true)
	case "copy":
		if len(m.Text) <= 64*1024 {
			if err := writeChatClipboard(s.hwnd.Load(), m.Text); err != nil {
				_ = s.postJSON(map[string]string{"kind": "error", "message": i18n.T(s.locale, "err.quickinput.copy_failed"), "message_key": "err.quickinput.copy_failed"})
			}
		}
	case "chat":
		s.chatRequest(m)
	case "command":
		s.commandRequest(m)
	}
}

func (s *nativeSurface) completeSnapshot(id string) {
	if done, ok := s.waiters[id]; ok {
		delete(s.waiters, id)
		if visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(s.hwnd.Load()); visible == 0 {
			s.suspend(func() { close(done) })
		} else {
			close(done)
		}
	}
}

func (s *nativeSurface) Update(theme string) error {
	if theme != "star" && theme != "frost" {
		return i18n.Err("err.quickinput.invalid_theme")
	}
	return s.dispatch(context.Background(), func() {
		s.theme = theme
		if !s.wantsSuspended {
			_ = s.postJSON(map[string]string{"kind": "theme", "theme": theme})
		}
	})
}

func (s *nativeSurface) UpdateLocale(locale string) error {
	if !i18n.ValidLocale(locale) {
		return i18n.Err("err.quickinput.prefs_invalid_locale")
	}
	return s.dispatch(context.Background(), func() {
		s.locale = locale
		if hwnd := s.hwnd.Load(); hwnd != 0 {
			if title, err := windows.UTF16PtrFromString(i18n.T(locale, s.titleKey())); err == nil {
				uiDLL.NewProc("SetWindowTextW").Call(hwnd, uintptr(unsafe.Pointer(title)))
			}
		}
		if !s.wantsSuspended && s.documentReady {
			_ = s.postJSON(map[string]string{"kind": "locale", "locale": locale})
		}
	})
}

func (s *nativeSurface) Snapshot(ctx context.Context) error {
	id, done := model.ID(), make(chan struct{})
	if err := s.dispatch(ctx, func() {
		if len(s.waiters) >= 8 {
			return
		}
		s.waiters[id] = done
		s.resume()
		_ = s.postJSON(map[string]string{"kind": "snapshot", "id": id})
	}); err != nil {
		return err
	}
	select {
	case <-done:
		return nil
	case <-s.done:
		return i18n.Err("err.quickinput.surface_closed")
	case <-ctx.Done():
		_ = s.dispatch(context.Background(), func() { s.cancelSnapshot(id) })
		return ctx.Err()
	case <-time.After(2 * time.Second):
		_ = s.dispatch(context.Background(), func() { s.cancelSnapshot(id) })
		return i18n.Err("err.quickinput.snapshot_ime")
	}
}

func (s *nativeSurface) cancelSnapshot(id string) {
	delete(s.waiters, id)
	if s.remoteDraft != nil {
		s.remoteDraft.cancelSnapshot(id)
	}
	if visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(s.hwnd.Load()); visible == 0 {
		s.suspend(nil)
	}
}
