//go:build windows

package quickinput

import (
	"context"
	"fmt"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func NativeFactory(c *Controller) SurfaceFactory {
	return func() (Surface, error) {
		c.mu.Lock()
		generation := c.surfaceGeneration
		c.mu.Unlock()
		s, err := startRemoteSurface(c.home, c.Status().Desired.Theme, &c.draft, launchWindowsHost, func(err error) { c.failedGeneration(generation, err) }, c.Status().RecoveryNotice != "")
		if err == nil {
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
		if err := s.postJSON(map[string]any{"kind": "init", "generation": s.epoch, "text": text, "revision": revision, "theme": s.theme}); err != nil {
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
		if m.Height >= 240 && m.Height <= 480 {
			s.height = m.Height
			s.position()
		}
	case "draft_updated", "snapshot":
		if err := s.draft.Update(m.Text, m.Revision); err != nil {
			_ = s.postJSON(map[string]string{"kind": "error", "message": err.Error()})
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
		return fmt.Errorf("主题无效")
	}
	return s.dispatch(context.Background(), func() {
		s.theme = theme
		if !s.wantsSuspended {
			_ = s.postJSON(map[string]string{"kind": "theme", "theme": theme})
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
		return fmt.Errorf("输入界面已关闭")
	case <-ctx.Done():
		_ = s.dispatch(context.Background(), func() { s.cancelSnapshot(id) })
		return ctx.Err()
	case <-time.After(2 * time.Second):
		_ = s.dispatch(context.Background(), func() { s.cancelSnapshot(id) })
		return fmt.Errorf("请先完成输入法候选，再重试")
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
