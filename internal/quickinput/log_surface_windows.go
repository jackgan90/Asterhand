//go:build windows

package quickinput

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type logSurface struct {
	id       string
	send     func(hostipc.Message) error
	routes   *sync.Map
	inFlight atomic.Int32
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
	m, err := decodeBridgeMessage(raw)
	if err != nil {
		return
	}
	if m.Kind == "ready" && !s.documentReady {
		s.documentReady = true
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
			s.ready <- nil
			s.logActivate()
		}
	case "command":
		if !commandswire.ValidID(m.ID) || m.Command == nil || m.Command.Validate() != nil || m.Command.ID != s.log.id {
			return
		}
		if m.Command.Action != "status" && m.Command.Action != "read_log" && m.Command.Action != "tail_log" && m.Command.Action != "stop" {
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
	if s.closing || !s.pageInitialized {
		return
	}
	s.showContent(true)
	mode := uintptr(5)
	if minimized, _, _ := uiDLL.NewProc("IsIconic").Call(s.hwnd.Load()); minimized != 0 {
		mode = 9
	}
	uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), mode)
	uiDLL.NewProc("SetForegroundWindow").Call(s.hwnd.Load())
}

func (s *nativeSurface) logWindowEvent(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case 0x0005:
		if s.controller == nil || s.closing {
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
		if lParam != 0 {
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

func serveLogHost(input io.Reader, output io.Writer, initial hostipc.Message) error {
	if err := checkHostRuntimePolicy(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peer := newHostPeer(ctx, input, output, initial.Session)
	if initial.Locale != "" {
		i18n.SetDefaultPreference(initial.Locale)
	}
	windows := map[string]*nativeSurface{}
	closed := make(chan *nativeSurface, 32)
	var routes sync.Map
	defer func() {
		for _, s := range windows {
			_ = s.Close()
		}
	}()
	if err := peer.send(hostipc.Message{Kind: hostipc.Ready, AppVersion: config.Version, Source: config.SourceDigest, Runtime: "log-host"}); err != nil {
		return err
	}
	for {
		select {
		case err := <-peer.failures:
			return err
		case s := <-closed:
			delete(windows, s.log.id)
			routes.Range(func(key, value any) bool {
				if value == s {
					routes.Delete(key)
				}
				return true
			})
			if len(windows) == 0 {
				return nil
			}
		case m := <-peer.in:
			switch m.Kind {
			case hostipc.Quit:
				return nil
			case hostipc.OpenLog:
				s := windows[m.WindowID]
				var err error
				if s == nil {
					if len(windows) >= 16 {
						err = i18n.Err("err.quickinput.surface_busy_retry")
					} else {
						log := &logSurface{id: m.WindowID, send: peer.send, routes: &routes}
						s, err = startWindowSurface(initial.Home, nil, peer.fail, nil, log)
						if err == nil {
							windows[m.WindowID] = s
							_ = s.Update(initial.Theme)
							_ = s.UpdateLocale(initial.Locale)
							go func(s *nativeSurface) {
								<-s.done
								select {
								case closed <- s:
								case <-ctx.Done():
								}
							}(s)
						}
					}
				} else {
					err = s.dispatch(ctx, s.logActivate)
				}
				code := ""
				if err != nil {
					code = "window_failed"
				}
				_ = peer.send(hostipc.Message{Kind: hostipc.State, Request: m.Request, Code: code})
			case hostipc.CommandResult:
				if value, ok := routes.LoadAndDelete(m.CommandID); ok {
					s := value.(*nativeSurface)
					s.log.inFlight.Add(-1)
					_ = s.dispatch(ctx, func() {
						_ = s.postJSON(map[string]any{"kind": "command_result", "id": m.CommandID, "response": m.CommandReply})
					})
				}
			case hostipc.Theme:
				initial.Theme = m.Theme
				for _, s := range windows {
					_ = s.Update(m.Theme)
				}
			case hostipc.Locale:
				initial.Locale = m.Locale
				i18n.SetDefaultPreference(m.Locale)
				for _, s := range windows {
					_ = s.UpdateLocale(m.Locale)
				}
			default:
				return hostipc.ErrProtocol
			}
		}
	}
}
