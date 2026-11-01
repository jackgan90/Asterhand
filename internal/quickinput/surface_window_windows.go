//go:build windows

package quickinput

import (
	"context"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"golang.org/x/sys/windows"
)

func checkInteractiveDesktop() error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return i18n.Err("err.quickinput.host_privilege_error")
	}
	return checkInteractiveSession()
}

func checkInteractiveSession() error {
	station, _, _ := uiDLL.NewProc("GetProcessWindowStation").Call()
	var flags struct{ Inherit, Reserved, Flags uint32 }
	var size uint32
	ok, _, _ := uiDLL.NewProc("GetUserObjectInformationW").Call(station, 1, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || flags.Flags&1 == 0 {
		return i18n.Err("err.quickinput.no_interactive_desktop")
	}
	return nil
}

func (s *nativeSurface) Toggle() {
	_ = s.dispatch(context.Background(), func() {
		visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(s.hwnd.Load())
		if visible != 0 {
			s.hide(true)
			return
		}
		if s.closing || !s.pageInitialized {
			return
		}
		s.previous, _, _ = uiDLL.NewProc("GetForegroundWindow").Call()
		if minimized, _, _ := uiDLL.NewProc("IsIconic").Call(s.hwnd.Load()); minimized != 0 {
			uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4) // Restore without activating before placement.
		}
		s.positionForShow()
		s.activate()
	})
}

// Explicit activation is separate from Toggle. Shell activation/restoration
// never invokes the shortcut's hide action.
func (s *nativeSurface) activate() {
	if s.closing || !s.pageInitialized {
		return
	}
	s.showContent(true)
	uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 9) // SW_RESTORE also clears a hidden minimized state.
	ok, _, _ := uiDLL.NewProc("SetForegroundWindow").Call(s.hwnd.Load())
	if ok == 0 {
		if s.onFailure != nil {
			s.onFailure(&RegistrationError{Code: "focus_denied", Detail: i18n.T(i18n.Effective(), "err.quickinput.focus_denied_taskbar"), Key: "err.quickinput.focus_denied_taskbar"})
		}
		return
	}
	_ = s.controller.call(12, 0)
	_ = s.postJSON(map[string]string{"kind": "focus"})
}

func (s *nativeSurface) showContent(visible bool) {
	if s.core == nil || !s.pageInitialized || s.contentVisible == visible {
		return
	}
	s.contentVisible = visible
	if visible {
		s.resume()
		_ = s.controller.call(4, 1)
		_ = s.postJSON(map[string]any{"kind": "visibility", "visible": true})
	} else {
		_ = s.postJSON(map[string]any{"kind": "visibility", "visible": false})
		s.suspend(nil)
	}
}

func (s *nativeSurface) hide(restore bool) {
	s.stopAttention()
	s.showContent(false)
	foreground, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
	uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 0)
	// Snapshot/initialization may have resumed an already hidden WebView.
	s.suspend(nil)
	if restore && foreground == s.hwnd.Load() && s.previous != 0 {
		if valid, _, _ := uiDLL.NewProc("IsWindow").Call(s.previous); valid != 0 {
			uiDLL.NewProc("SetForegroundWindow").Call(s.previous)
		}
	}
}

func (s *nativeSurface) windowSizeChanged(hwnd, kind uintptr) {
	if s.controller == nil || s.closing {
		return
	}
	if kind == 1 { // SIZE_MINIMIZED is not an explicit Hide: keep the taskbar entry.
		s.showContent(false)
		return
	}
	var rect nativeRect
	uiDLL.NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	_ = s.controller.call(6, uintptr(unsafe.Pointer(&rect)))
	if visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd); visible != 0 {
		s.showContent(true)
		if !s.placing {
			s.positionCurrent(nil)
		}
	}
}

func (s *nativeSurface) windowEvent(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case 0x0005: // WM_SIZE
		s.windowSizeChanged(hwnd, wParam)
	case 0x0006: // WM_ACTIVATE: losing focus no longer hides the window.
		if wParam&0xffff != 0 && wParam>>16 == 0 {
			s.stopAttention()
			s.showContent(true)
		}
		return defaultWindowEvent(hwnd, message, wParam, lParam)
	case 0x0007: // WM_SETFOCUS restores WebView keyboard navigation from the taskbar.
		if s.controller != nil && s.contentVisible && !s.closing {
			_ = s.controller.call(12, 0)
		}
	case 0x0232: // WM_EXITSIZEMOVE: only user movement records a new preferred position.
		s.rememberPlacement()
		s.positionCurrent(nil)
	case 0x007e: // WM_DISPLAYCHANGE
		s.positionCurrent(nil)
	case 0x02e0: // WM_DPICHANGED, use the OS-proposed anchor, never the foreground app.
		if lParam != 0 {
			// lParam points to an OS-owned RECT valid for this callback only.
			var suggested *nativeRect
			*(*uintptr)(unsafe.Pointer(&suggested)) = lParam
			s.positionCurrent(suggested)
		}
		s.setWindowIcons()
	case 0x001a: // WM_SETTINGCHANGE, including taskbar/work-area changes.
		if wParam == 0x002f {
			s.positionCurrent(nil)
		} // SPI_SETWORKAREA
		return defaultWindowEvent(hwnd, message, wParam, lParam)
	case 0x0112: // WM_SYSCOMMAND
		if wParam&0xfff0 == 0xf030 || wParam&0xfff0 == 0xf000 {
			return 0
		} // No maximize/free resize.
		return defaultWindowEvent(hwnd, message, wParam, lParam)
	case 0x00a3: // WM_NCLBUTTONDBLCLK: title dragging must not add a maximize mode.
		return 0
	case 0x02b1:
		if wParam == 7 || wParam == 4 || wParam == 2 || wParam == 6 {
			s.hide(false)
		}
	default:
		return defaultWindowEvent(hwnd, message, wParam, lParam)
	}
	return 0
}

func defaultWindowEvent(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	result, _, _ := uiDLL.NewProc("DefWindowProcW").Call(hwnd, uintptr(message), wParam, lParam)
	return result
}
