//go:build windows

package quickinput

import (
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"unsafe"
)

func checkInteractiveDesktop() error {
	if windows.GetCurrentProcessToken().IsElevated() {
		return fmt.Errorf("请使用标准用户权限启动快捷输入；当前节点其他功能继续可用")
	}
	station, _, _ := uiDLL.NewProc("GetProcessWindowStation").Call()
	var flags struct{ Inherit, Reserved, Flags uint32 }
	var size uint32
	ok, _, _ := uiDLL.NewProc("GetUserObjectInformationW").Call(station, 1, uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || flags.Flags&1 == 0 {
		return fmt.Errorf("当前没有可用的 Windows 交互桌面")
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
		if s.closing || !s.documentReady {
			return
		}
		s.previous, _, _ = uiDLL.NewProc("GetForegroundWindow").Call()
		s.position()
		s.resume()
		_ = s.controller.call(4, 1)
		uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 5)
		ok, _, _ := uiDLL.NewProc("SetForegroundWindow").Call(s.hwnd.Load())
		if ok == 0 {
			if s.onFailure != nil {
				s.onFailure(&RegistrationError{"focus_denied", "系统拒绝激活输入框，请从托盘重试"})
			}
			return
		}
		_ = s.controller.call(12, 0)
		_ = s.postJSON(map[string]string{"kind": "focus"})
	})
}

func (s *nativeSurface) hide(restore bool) {
	foreground, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
	uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 0)
	s.suspend(nil)
	if restore && foreground == s.hwnd.Load() && s.previous != 0 {
		if valid, _, _ := uiDLL.NewProc("IsWindow").Call(s.previous); valid != 0 {
			uiDLL.NewProc("SetForegroundWindow").Call(s.previous)
		}
	}
}

func (s *nativeSurface) position() {
	foreground, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
	monitor, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(foreground, 0)
	if monitor == 0 {
		var cursor nativePoint
		uiDLL.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&cursor)))
		pointValue := uint64(uint32(cursor.X)) | uint64(uint32(cursor.Y))<<32
		monitor, _, _ = uiDLL.NewProc("MonitorFromPoint").Call(uintptr(pointValue), 2)
	}
	var info struct {
		Size          uint32
		Monitor, Work nativeRect
		Flags         uint32
	}
	info.Size = uint32(unsafe.Sizeof(info))
	if ok, _, _ := uiDLL.NewProc("GetMonitorInfoW").Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return
	}
	dpi, _, _ := uiDLL.NewProc("GetDpiForWindow").Call(foreground)
	if dpi == 0 {
		dpi = 96
	}
	width, height := int32(680*dpi/96), int32(max(240, s.height)*int(dpi)/96)
	width = min(width, info.Work.Right-info.Work.Left-16)
	height = min(height, info.Work.Bottom-info.Work.Top-16)
	x := info.Work.Left + (info.Work.Right-info.Work.Left-width)/2
	y := info.Work.Top + (info.Work.Bottom-info.Work.Top-height)/3
	uiDLL.NewProc("SetWindowPos").Call(s.hwnd.Load(), ^uintptr(0), uintptr(x), uintptr(y), uintptr(width), uintptr(height), 0x10)
}

func (s *nativeSurface) windowEvent(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case 0x0005:
		if s.controller != nil {
			var rect nativeRect
			uiDLL.NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&rect)))
			_ = s.controller.call(6, uintptr(unsafe.Pointer(&rect)))
		}
	case 0x0006:
		if wParam&0xffff == 0 {
			owner, _, _ := uiDLL.NewProc("GetAncestor").Call(lParam, 3)
			if owner != hwnd {
				s.hide(false)
			}
		}
	case 0x007e, 0x02e0:
		s.position()
	case 0x02b1:
		if wParam == 7 || wParam == 4 || wParam == 2 || wParam == 6 {
			s.hide(false)
		}
	default:
		r, _, _ := uiDLL.NewProc("DefWindowProcW").Call(hwnd, uintptr(message), wParam, lParam)
		return r
	}
	return 0
}
