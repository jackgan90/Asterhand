//go:build windows

package quickinput

import (
	"bytes"
	"encoding/json"
	"io"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/notifications"
)

const (
	workerActionMinHeight     int32 = 44
	workerActionMaxHeight     int32 = 520
	workerActionInitialHeight int32 = 280
	workerActionWidth         int32 = 420
	workerActionGap           int32 = 12
)

type workerActionSurface struct {
	desktop *notificationDesktop
	items   []notifications.WorkerInteraction
	target  nativeRect
	dpi     uint32
	layout  uint64
	paused  bool
	waiting bool
	shown   bool
}

func (s *nativeSurface) workerActionBridgeMessage(raw string) {
	var m struct {
		Version   int                 `json:"version"`
		Kind      string              `json:"kind"`
		Epoch     string              `json:"generation"`
		Layout    uint64              `json:"layout,omitempty"`
		Height    int32               `json:"height,omitempty"`
		RequestID string              `json:"request_id,omitempty"`
		Decision  string              `json:"decision,omitempty"`
		Text      string              `json:"text,omitempty"`
		Answers   map[string][]string `json:"answers,omitempty"`
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || m.Version != 1 {
		return
	}
	if m.Kind == "ready" && !s.documentReady {
		s.documentReady = true
		if err := s.postJSON(map[string]any{"kind": "init", "generation": s.epoch, "theme": s.theme, "locale": s.locale}); err != nil {
			s.ready <- err
		}
		return
	}
	if m.Epoch != s.epoch || !s.documentReady {
		return
	}
	a := s.workerAction
	switch m.Kind {
	case "initialized":
		if !s.pageInitialized {
			s.pageInitialized = true
			s.suspend(func() { s.ready <- nil })
		}
	case "rendered":
		if a.paused || len(a.items) == 0 {
			return
		}
		h := m.Height
		if h < workerActionMinHeight {
			h = workerActionMinHeight
		}
		if h > workerActionMaxHeight {
			h = workerActionMaxHeight
		}
		if !a.desktop.actionMeasured(h) {
			return
		}
		if a.waiting {
			a.waiting = false
			s.revealWorkerAction()
		} else if a.shown {
			_ = s.postJSON(map[string]any{"kind": "resized", "layout": a.layout, "height": h})
		}
	case "decide":
		if m.RequestID == "" || !a.shown || a.paused {
			return
		}
		a.desktop.emit(notifications.Event{
			Kind: "worker_action",
			WorkerAction: &notifications.WorkerActionEvent{
				RequestID: m.RequestID,
				Decision:  m.Decision,
				Text:      m.Text,
				Answers:   m.Answers,
			},
		})
	}
}

func (s *nativeSurface) applyWorkerActions(items []notifications.WorkerInteraction, bounds nativeRect, paused bool) {
	a := s.workerAction
	a.items = items
	a.paused = paused
	moved := a.target != bounds
	a.target = bounds
	if len(items) == 0 || paused {
		s.hideWorkerAction()
		return
	}
	s.applyFrameColor()
	s.resume()
	a.layout++
	if !a.shown || moved {
		if !s.positionWorkerAction() {
			return
		}
	}
	_ = s.postJSON(map[string]any{
		"kind":   "worker_actions",
		"items":  items,
		"layout": a.layout,
		"theme":  s.theme,
		"locale": s.locale,
	})
	if !a.shown {
		a.waiting = true
		_ = s.controller.call(4, 1)
	}
}

func (s *nativeSurface) revealWorkerAction() {
	a := s.workerAction
	if a.paused || len(a.items) == 0 {
		s.hideWorkerAction()
		return
	}
	if !s.positionWorkerAction() {
		return
	}
	s.showContent(true)
	if err := showNotificationWindow(s.hwnd.Load()); err != nil {
		s.hideWorkerAction()
		a.desktop.peer.fail(err)
		return
	}
	a.shown = true
}

func (s *nativeSurface) positionWorkerAction() bool {
	a := s.workerAction
	r := a.target
	s.placing = true
	hwnd := s.hwnd.Load()
	ok, _, err := uiDLL.NewProc("SetWindowPos").Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpNoActivate|swpNoZOrder)
	s.placing = false
	if ok != 0 {
		ok, _, err = uiDLL.NewProc("SetLayeredWindowAttributes").Call(hwnd, 0, 255, 2)
	}
	if ok == 0 {
		s.hideWorkerAction()
		a.desktop.peer.fail(err)
		return false
	}
	return true
}

func (s *nativeSurface) hideWorkerAction() {
	s.workerAction.shown = false
	s.workerAction.waiting = false
	s.hide(false)
}

func (s *nativeSurface) workerActionWindowEvent(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case 0x0005: // WM_SIZE
		if s.controller != nil && !s.closing {
			var rect nativeRect
			uiDLL.NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&rect)))
			_ = s.controller.call(6, uintptr(unsafe.Pointer(&rect)))
			dpi, _, _ := uiDLL.NewProc("GetDpiForWindow").Call(hwnd)
			radius := max(1, 24*dpi/96)
			region, _, _ := notificationGDI.NewProc("CreateRoundRectRgn").Call(0, 0, uintptr(rect.Right+1), uintptr(rect.Bottom+1), radius, radius)
			if region != 0 {
				if ok, _, _ := uiDLL.NewProc("SetWindowRgn").Call(hwnd, region, 1); ok == 0 {
					notificationGDI.NewProc("DeleteObject").Call(region)
				}
			}
		}
	case 0x0007: // WM_SETFOCUS: allow keyboard & IME input when user clicks the popup.
		if s.controller != nil && s.contentVisible && !s.closing {
			_ = s.controller.call(12, 0)
		}
	case 0x007e, 0x001a, 0x02e0:
		if !s.placing {
			s.workerAction.desktop.refreshEnvironment()
		}
	case 0x02b1:
		if wParam >= 1 && wParam <= 8 {
			s.workerAction.desktop.refreshEnvironment()
		}
	case 0x0218:
		if wParam == 4 {
			s.workerAction.desktop.suspend(true)
		}
		if wParam == 7 || wParam == 18 {
			s.workerAction.desktop.suspend(false)
		}
	default:
		return defaultWindowEvent(hwnd, message, wParam, lParam)
	}
	return 0
}
