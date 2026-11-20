//go:build windows

package quickinput

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"time"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/notifications"
	"golang.org/x/sys/windows"
)

var notificationGDI = windows.NewLazySystemDLL("gdi32.dll")

const (
	wsExTopmost   = 0x00000008
	swpNoActivate = 0x0010
	swpNoZOrder   = 0x0004
)

type notificationSurface struct {
	desktop                         *notificationDesktop
	slot                            int
	card                            notifications.Card
	target, current, from           nativeRect
	alpha, fromAlpha, targetAlpha   float64
	animation                       time.Time
	duration                        time.Duration
	paused, reduced, waiting, shown bool
	revealAfter                     time.Time
	dpi                             uint32
	layout                          uint64
}

func (n *notificationSurface) emit(kind string) {
	if n.card.ID != 0 {
		n.desktop.emit(notifications.Event{Kind: kind, ID: n.card.ID})
	}
}

func (s *nativeSurface) notificationBridgeMessage(raw string) {
	var m struct {
		Version int    `json:"version"`
		Kind    string `json:"kind"`
		Epoch   string `json:"generation"`
		ID      uint64 `json:"id,omitempty"`
		Paused  bool   `json:"paused,omitempty"`
		Layout  uint64 `json:"layout,omitempty"`
		Height  int32  `json:"height,omitempty"`
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
	n := s.notification
	switch m.Kind {
	case "initialized":
		if !s.pageInitialized {
			s.pageInitialized = true
			s.suspend(func() { s.ready <- nil })
		}
	case "rendered":
		if m.ID != n.card.ID || m.Layout != n.layout || n.paused ||
			m.Height < notificationMinHeight || m.Height > notificationMaxHeight {
			return
		}
		if !n.desktop.measured(n.slot, m.ID, m.Height) {
			return
		}
		if n.waiting {
			n.waiting = false
			s.revealNotification()
		}
	case "hover":
		if m.ID == n.card.ID {
			n.desktop.setHover(n.slot, m.Paused)
		}
	case "activate", "dismiss":
		if m.ID == n.card.ID && n.shown && !n.paused {
			if m.Kind == "activate" {
				uiDLL.NewProc("AllowSetForegroundWindow").Call(^uintptr(0))
			}
			n.emit(m.Kind)
		}
	}
}

func (s *nativeSurface) applyNotification(card notifications.Card, bounds nativeRect, paused, reduced bool) {
	n := s.notification
	previous := n.card
	n.card, n.paused, n.reduced = card, paused, reduced
	moved := n.target != bounds
	n.target = bounds
	s.applyFrameColor()
	s.resume()
	n.layout++
	if !n.shown && !paused {
		// Measure using the destination width/DPI while the native window is hidden.
		n.current, n.alpha = bounds, 0
		if !s.positionNotification() {
			return
		}
	}
	_ = s.postJSON(map[string]any{"kind": "notification", "card": card, "layout": n.layout, "theme": s.theme, "locale": s.locale})
	if paused {
		s.hideNotification()
		return
	}
	if previous.ID != card.ID || !n.shown {
		n.waiting = true
		// Resume controller rendering while the native window remains hidden.
		_ = s.controller.call(4, 1)
		return
	}
	if card.Phase == "exiting" {
		if previous.Phase != "exiting" {
			s.startNotificationAnimation(0, 180*time.Millisecond)
		} else if moved {
			// Reposition from the current frame without reversing or extending exit.
			s.startNotificationAnimation(0, max(0, n.duration-time.Since(n.animation)))
		}
	} else if moved {
		s.startNotificationAnimation(255, 200*time.Millisecond)
	}
}

func (s *nativeSurface) revealNotification() {
	n := s.notification
	if n.paused || s.closing {
		return
	}
	if n.waiting {
		// An older delay must not reveal a newer frame before its measurement.
		uiDLL.NewProc("KillTimer").Call(s.hwnd.Load(), 72)
		return
	}
	if time.Now().Before(n.revealAfter) {
		uiDLL.NewProc("SetTimer").Call(s.hwnd.Load(), 72, 15, 0)
		return
	}
	uiDLL.NewProc("KillTimer").Call(s.hwnd.Load(), 72)
	if n.card.Phase == "exiting" {
		s.hideNotification()
		n.emit("exited")
		return
	}
	entering := n.card.Phase == "entering"
	n.current, n.alpha = n.target, 255
	if entering {
		if !n.reduced {
			offset := scaled(24, max(96, n.dpi))
			n.current.Left += offset
			n.current.Right += offset
		}
		n.alpha = 0
	}
	if !s.positionNotification() {
		return
	}
	s.showContent(true)
	if err := showNotificationWindow(s.hwnd.Load()); err != nil {
		s.failNotification(err)
		return
	}
	n.shown = true
	if entering {
		s.startNotificationAnimation(255, 240*time.Millisecond)
	}
}

func (s *nativeSurface) startNotificationAnimation(alpha float64, duration time.Duration) {
	n := s.notification
	n.from, n.fromAlpha, n.targetAlpha = n.current, n.alpha, alpha
	n.animation, n.duration = time.Now(), duration
	if n.reduced {
		n.duration = 0
	}
	uiDLL.NewProc("SetTimer").Call(s.hwnd.Load(), 71, 15, 0)
	s.animateNotification()
}

func (s *nativeSurface) animateNotification() {
	n := s.notification
	if n.paused || s.closing {
		uiDLL.NewProc("KillTimer").Call(s.hwnd.Load(), 71)
		return
	}
	t := 1.0
	if n.duration > 0 {
		t = min(1, float64(time.Since(n.animation))/float64(n.duration))
	}
	ease := 1 - math.Pow(1-t, 3)
	target := n.target
	if n.targetAlpha == 0 && !n.reduced {
		offset := scaled(16, max(96, n.dpi))
		target.Left += offset
		target.Right += offset
	}
	interpolate := func(a, b int32) int32 { return a + int32(math.Round(float64(b-a)*ease)) }
	n.current = nativeRect{Left: interpolate(n.from.Left, target.Left), Top: interpolate(n.from.Top, target.Top), Right: interpolate(n.from.Right, target.Right), Bottom: interpolate(n.from.Bottom, target.Bottom)}
	n.alpha = n.fromAlpha + (n.targetAlpha-n.fromAlpha)*ease
	if !s.positionNotification() || t < 1 {
		return
	}
	uiDLL.NewProc("KillTimer").Call(s.hwnd.Load(), 71)
	if n.targetAlpha == 0 {
		s.hideNotification()
		n.emit("exited")
	} else if n.card.Phase == "entering" {
		if err := checkNotificationWindow(s.hwnd.Load()); err != nil {
			s.failNotification(err)
			return
		}
		n.emit("entered")
	}
}

func (s *nativeSurface) positionNotification() bool {
	n := s.notification
	r := n.current
	s.placing = true
	hwnd := s.hwnd.Load()
	ok, _, err := uiDLL.NewProc("SetWindowPos").Call(hwnd, 0, uintptr(r.Left), uintptr(r.Top), uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), swpNoActivate|swpNoZOrder)
	s.placing = false
	if ok != 0 {
		ok, _, err = uiDLL.NewProc("SetLayeredWindowAttributes").Call(hwnd, 0, uintptr(math.Round(n.alpha)), 2)
	}
	if ok == 0 {
		s.failNotification(err)
		return false
	}
	return true
}

func notificationTopmost(hwnd uintptr) bool {
	ex, _, _ := uiDLL.NewProc("GetWindowLongPtrW").Call(hwnd, ^uintptr(19)) // GWL_EXSTYLE
	return ex&wsExTopmost != 0
}

func showNotificationWindow(hwnd uintptr) error {
	const keepBounds = 0x0001 | 0x0002 | swpNoActivate // SWP_NOSIZE | SWP_NOMOVE | SWP_NOACTIVATE
	const showWindow = 0x0040
	setPos := uiDLL.NewProc("SetWindowPos")
	if !notificationTopmost(hwnd) {
		// A direct HWND_TOPMOST request can succeed without promoting the HWND.
		// Reset its Z position while hidden, before showing any content.
		if ok, _, err := setPos.Call(hwnd, 1, 0, 0, 0, 0, keepBounds); ok == 0 { // HWND_BOTTOM
			return err
		}
	}
	if ok, _, err := setPos.Call(hwnd, ^uintptr(0), 0, 0, 0, 0, keepBounds|showWindow); ok == 0 {
		return err
	}
	return checkNotificationWindow(hwnd)
}

func checkNotificationWindow(hwnd uintptr) error {
	visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd)
	if visible == 0 || !notificationTopmost(hwnd) {
		return errors.New("notification window did not become visible and topmost")
	}
	return nil
}

func (s *nativeSurface) failNotification(err error) {
	s.hideNotification()
	s.notification.desktop.peer.fail(err)
}

func (s *nativeSurface) hideNotification() {
	s.notification.shown = false
	uiDLL.NewProc("KillTimer").Call(s.hwnd.Load(), 71)
	uiDLL.NewProc("KillTimer").Call(s.hwnd.Load(), 72)
	s.hide(false)
}

func (s *nativeSurface) notificationWindowEvent(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case 0x0021:
		return 3 // MA_NOACTIVATE: clicks never steal focus from the current app.
	case 0x0005:
		if s.controller != nil && !s.closing {
			var rect nativeRect
			uiDLL.NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&rect)))
			_ = s.controller.call(6, uintptr(unsafe.Pointer(&rect)))
			// Real rounded native hit area, including Windows 10; no invisible
			// full-screen overlay or click-catching gaps between card windows.
			dpi, _, _ := uiDLL.NewProc("GetDpiForWindow").Call(hwnd)
			radius := max(1, 24*dpi/96)
			region, _, _ := notificationGDI.NewProc("CreateRoundRectRgn").Call(0, 0, uintptr(rect.Right+1), uintptr(rect.Bottom+1), radius, radius)
			if region != 0 {
				if ok, _, _ := uiDLL.NewProc("SetWindowRgn").Call(hwnd, region, 1); ok == 0 {
					notificationGDI.NewProc("DeleteObject").Call(region)
				}
			}
		}
	case 0x007e, 0x001a, 0x02e0:
		if !s.placing {
			s.notification.desktop.refreshEnvironment()
		}
	case 0x02b1:
		if wParam >= 1 && wParam <= 8 {
			// WTS messages signal a change; query connection and lock together.
			s.notification.desktop.refreshEnvironment()
		}
	case 0x0218:
		if wParam == 4 {
			s.notification.desktop.suspend(true)
		}
		if wParam == 7 || wParam == 18 {
			s.notification.desktop.suspend(false)
		}
	default:
		return defaultWindowEvent(hwnd, message, wParam, lParam)
	}
	return 0
}
