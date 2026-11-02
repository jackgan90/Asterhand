//go:build windows

package quickinput

import (
	"runtime"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func logAnchorForWindow(hwnd uintptr) *hostipc.WindowAnchor {
	monitor, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(hwnd, 2)
	info, ok := monitorDetails(monitor)
	if !ok {
		return nil
	}
	return &hostipc.WindowAnchor{X: info.work.Left + (info.work.Right-info.work.Left)/2, Y: info.work.Top + (info.work.Bottom-info.work.Top)/2}
}

func defaultLogAnchor() *hostipc.WindowAnchor {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous, _, _ := uiDLL.NewProc("SetThreadDpiAwarenessContext").Call(^uintptr(3))
	if previous != 0 {
		defer uiDLL.NewProc("SetThreadDpiAwarenessContext").Call(previous)
	}
	hwnd, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
	return logAnchorForWindow(hwnd)
}

func logMonitor(anchor *hostipc.WindowAnchor) uintptr {
	if anchor == nil {
		return foregroundMonitor()
	}
	point := uint64(uint32(anchor.X)) | uint64(uint32(anchor.Y))<<32
	monitor, _, _ := uiDLL.NewProc("MonitorFromPoint").Call(uintptr(point), 2)
	return monitor
}

func (s *nativeSurface) positionLogWindow(anchor *hostipc.WindowAnchor) bool {
	if anchor == nil {
		anchor = s.log.anchor
	}
	s.log.anchor = anchor
	target := logMonitor(anchor)
	monitor, ok := monitorDetails(target)
	if !ok {
		return false
	}
	current, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(s.hwnd.Load(), 2)
	maximized, _, _ := uiDLL.NewProc("IsZoomed").Call(s.hwnd.Load())
	minimized, _, _ := uiDLL.NewProc("IsIconic").Call(s.hwnd.Load())
	if s.log.positioned && current == target && maximized != 0 {
		return false
	}
	if maximized != 0 || minimized != 0 {
		// Read the restored normal rectangle, rather than the minimized icon or
		// maximized work area. Keep user size and position on the same monitor.
		uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4)
	}
	var size windowPoint
	var origin *windowPoint
	var bounds nativeRect
	if s.log.positioned {
		uiDLL.NewProc("GetWindowRect").Call(s.hwnd.Load(), uintptr(unsafe.Pointer(&bounds)))
		size = windowPoint{bounds.Right - bounds.Left, bounds.Bottom - bounds.Top}
		if current == target {
			origin = &windowPoint{bounds.Left, bounds.Top}
		} else {
			oldDPI, _, _ := uiDLL.NewProc("GetDpiForWindow").Call(s.hwnd.Load())
			if oldDPI != 0 {
				size.X = int32(int64(size.X) * int64(monitor.dpi) / int64(oldDPI))
				size.Y = int32(int64(size.Y) * int64(monitor.dpi) / int64(oldDPI))
			}
		}
	}
	bounds = fitLogWindow(monitor.work, monitor.dpi, size, origin)
	s.placing = true
	uiDLL.NewProc("SetWindowPos").Call(s.hwnd.Load(), 0, uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right-bounds.Left), uintptr(bounds.Bottom-bounds.Top), 0x14)
	s.placing = false
	s.log.positioned = true
	return maximized != 0
}
