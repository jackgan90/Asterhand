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
		if err := checkInteractiveDesktop(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		generation := c.surfaceGeneration
		c.mu.Unlock()
		s, err := startNativeSurface(c.home, nil, func(err error) { c.failedGeneration(generation, err) }, &c.draft)
		if err == nil {
			c.mu.Lock()
			c.status.RuntimeVersion = s.version
			notice := c.status.RecoveryNotice
			c.mu.Unlock()
			if notice != "" {
				_ = s.dispatch(context.Background(), func() { _ = s.postJSON(map[string]string{"kind": "error", "message": notice}) })
			}
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
		err := s.postJSON(map[string]any{"kind": "init", "generation": s.epoch, "text": text, "revision": revision, "theme": s.theme})
		s.suspend(func() { s.ready <- err })
		return
	}
	if m.Epoch != s.epoch || !s.documentReady {
		return
	}
	switch m.Kind {
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
		if m.Kind == "snapshot" {
			if done, ok := s.waiters[m.ID]; ok {
				delete(s.waiters, m.ID)
				if visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(s.hwnd.Load()); visible == 0 {
					s.suspend(func() { close(done) })
				} else {
					close(done)
				}
			}
		}
	case "hide":
		s.hide(true)
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
	if visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(s.hwnd.Load()); visible == 0 {
		s.suspend(nil)
	}
}
