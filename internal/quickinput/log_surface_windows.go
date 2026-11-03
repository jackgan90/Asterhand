//go:build windows

package quickinput

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type logSurface struct {
	id         string
	session    string
	anchor     *hostipc.WindowAnchor
	shown      chan error
	hidden     func()
	visible    atomic.Bool
	failed     atomic.Bool
	positioned bool
	started    time.Time
	send       func(hostipc.Message) error
	routes     *sync.Map
	inFlight   atomic.Int32
}

func NewLogWindowHost(home string, call CommandHandler) LogWindowHost {
	return newLogWindowHost(home, call, func() (hostProcess, error) {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		return launchWindowsHostMode(filepath.Dir(exe), true)
	})
}

func (s *nativeSurface) pageURL() string {
	if s.log != nil {
		return surfaceOrigin + "/quick-input/command-log.html"
	}
	return surfaceURL
}
func (s *nativeSurface) titleKey() string {
	if s.log != nil {
		return "brand.log_window_title"
	}
	return "brand.quickinput_window_title"
}

func (s *nativeSurface) logBridgeMessage(raw string) {
	if s.log.failed.Load() {
		return
	}
	m, err := decodeBridgeMessage(raw)
	if err != nil {
		return
	}
	if m.Kind == "ready" && !s.documentReady {
		s.documentReady = true
		s.logProgress("page_ready", "")
		if err = s.postJSON(map[string]any{"kind": "init", "generation": s.epoch, "command_id": s.log.id, "theme": s.theme, "locale": s.locale}); err != nil {
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
			s.logProgress("page_initialized", "")
			s.ready <- nil
			if s.log.visible.Load() {
				s.showContent(true)
			} else {
				s.suspend(nil)
			}
		}
	case "command":
		if !commandswire.ValidID(m.ID) || m.Command == nil || m.Command.Validate() != nil || m.Command.ID != s.log.id {
			return
		}
		if m.Command.Action != "runs" && m.Command.Action != "stop_all" && m.Command.Action != "status" && m.Command.Action != "read_log" && m.Command.Action != "tail_log" && m.Command.Action != "stop" {
			return
		}
		if s.log.inFlight.Add(1) > 4 {
			s.log.inFlight.Add(-1)
			return
		}
		if _, loaded := s.log.routes.LoadOrStore(m.ID, s); loaded {
			s.log.inFlight.Add(-1)
			return
		}
		if err = s.log.send(hostipc.Message{Kind: hostipc.CommandCall, Epoch: 1, CommandID: m.ID, Command: m.Command}); err != nil {
			s.log.routes.Delete(m.ID)
			s.log.inFlight.Add(-1)
		}
	}
}

func (s *nativeSurface) logActivate() {
	_ = s.showLogWindow(s.log.anchor)
}

func (s *nativeSurface) showLogWindow(anchor *hostipc.WindowAnchor) error {
	if s.closing {
		return i18n.Err("err.quickinput.window_closed")
	}
	maximize := s.positionLogWindow(anchor)
	if s.pageInitialized && !s.log.failed.Load() {
		s.showContent(true)
	}
	mode := uintptr(5)
	if minimized, _, _ := uiDLL.NewProc("IsIconic").Call(s.hwnd.Load()); minimized != 0 {
		mode = 9
	}
	if maximize {
		mode = 3
	}
	uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), mode)
	uiDLL.NewProc("SetForegroundWindow").Call(s.hwnd.Load())
	visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(s.hwnd.Load())
	s.log.visible.Store(visible != 0)
	if visible == 0 {
		return i18n.Err("err.localcmd.log_window_failed")
	}
	s.invalidateLogShell()
	return nil
}

func (s *nativeSurface) logProgress(stage, code string) {
	if s.log == nil || s.log.send == nil {
		return
	}
	elapsed := int64(0)
	if !s.log.started.IsZero() {
		elapsed = min(3600000, max(0, time.Since(s.log.started).Milliseconds()))
	}
	_ = s.log.send(hostipc.Message{Kind: hostipc.LogProgress, LogEvent: &hostipc.LogWindowEvent{ID: s.log.id, Stage: stage, ElapsedMS: elapsed, Code: code}})
}

func (s *nativeSurface) logWindowEvent(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case 0x000f:
		if !s.pageInitialized || s.log.failed.Load() {
			s.paintLogShell(hwnd)
			return 0
		}
		return defaultWindowEvent(hwnd, message, wParam, lParam)
	case 0x0014:
		if !s.pageInitialized || s.log.failed.Load() {
			return 1
		}
		return defaultWindowEvent(hwnd, message, wParam, lParam)
	case 0x0005:
		if s.controller == nil || s.closing || s.log.failed.Load() {
			return 0
		}
		if wParam == 1 {
			s.showContent(false)
			return 0
		}
		var rect nativeRect
		uiDLL.NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&rect)))
		_ = s.controller.call(6, uintptr(unsafe.Pointer(&rect)))
		if visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd); visible != 0 {
			s.showContent(true)
		}
	case 0x0007:
		if s.controller != nil && s.contentVisible && !s.closing {
			_ = s.controller.call(12, 0)
		}
	case 0x02e0:
		if lParam != 0 && !s.placing {
			var rect *nativeRect
			*(*uintptr)(unsafe.Pointer(&rect)) = lParam
			uiDLL.NewProc("SetWindowPos").Call(hwnd, 0, uintptr(rect.Left), uintptr(rect.Top), uintptr(rect.Right-rect.Left), uintptr(rect.Bottom-rect.Top), 0x14)
		}
		s.setWindowIcons()
	default:
		return defaultWindowEvent(hwnd, message, wParam, lParam)
	}
	return 0
}
