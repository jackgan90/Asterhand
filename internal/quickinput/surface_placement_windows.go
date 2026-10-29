//go:build windows

package quickinput

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

type surfaceMonitor struct {
	work windowBounds
	dpi  uint32
	name string
}

func monitorDetails(monitor uintptr) (surfaceMonitor, bool) {
	var info struct {
		Size          uint32
		Monitor, Work nativeRect
		Flags         uint32
		Device        [32]uint16
	}
	info.Size = uint32(unsafe.Sizeof(info))
	if ok, _, _ := uiDLL.NewProc("GetMonitorInfoW").Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return surfaceMonitor{}, false
	}
	var x, y uint32
	hr, _, _ := windows.NewLazySystemDLL("shcore.dll").NewProc("GetDpiForMonitor").Call(monitor, 0, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y)))
	if int32(hr) < 0 || x == 0 {
		x = 96
	}
	return surfaceMonitor{info.Work, x, windows.UTF16ToString(info.Device[:])}, true
}

func foregroundMonitor() uintptr {
	foreground, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
	monitor, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(foreground, 0)
	if monitor == 0 {
		var cursor nativePoint
		uiDLL.NewProc("GetCursorPos").Call(uintptr(unsafe.Pointer(&cursor)))
		point := uint64(uint32(cursor.X)) | uint64(uint32(cursor.Y))<<32
		monitor, _, _ = uiDLL.NewProc("MonitorFromPoint").Call(uintptr(point), 2)
	}
	return monitor
}

func (s *nativeSurface) positionForShow() {
	monitor, ok := monitorDetails(foregroundMonitor())
	if !ok {
		return
	}
	var origin *windowPoint
	if saved, exists := s.placements[monitor.name]; exists {
		point := saved.point(monitor.work, monitor.dpi)
		origin = &point
	}
	s.applyBounds(fitWindow(monitor.work, monitor.dpi, s.height, origin))
}

func (s *nativeSurface) positionCurrent(suggested *nativeRect) {
	if s.placing || s.closing {
		return
	}
	if minimized, _, _ := uiDLL.NewProc("IsIconic").Call(s.hwnd.Load()); minimized != 0 {
		return
	}
	var current nativeRect
	if ok, _, _ := uiDLL.NewProc("GetWindowRect").Call(s.hwnd.Load(), uintptr(unsafe.Pointer(&current))); ok == 0 {
		return
	}
	if suggested != nil {
		current = *suggested
	}
	monitorID, _, _ := uiDLL.NewProc("MonitorFromRect").Call(uintptr(unsafe.Pointer(&current)), 2)
	monitor, ok := monitorDetails(monitorID)
	if !ok {
		return
	}
	origin := windowPoint{current.Left, current.Top}
	if saved, exists := s.placements[monitor.name]; exists && suggested == nil {
		origin = saved.point(monitor.work, monitor.dpi)
	}
	s.applyBounds(fitWindow(monitor.work, monitor.dpi, s.height, &origin))
}

func (s *nativeSurface) applyBounds(bounds windowBounds) {
	s.placing = true
	defer func() { s.placing = false }()
	// HWND_NOTOPMOST + SWP_NOACTIVATE: resizing must not change application focus.
	uiDLL.NewProc("SetWindowPos").Call(s.hwnd.Load(), ^uintptr(1), uintptr(bounds.Left), uintptr(bounds.Top),
		uintptr(bounds.Right-bounds.Left), uintptr(bounds.Bottom-bounds.Top), 0x0010)
}

func (s *nativeSurface) rememberPlacement() {
	if s.placing {
		return
	}
	if minimized, _, _ := uiDLL.NewProc("IsIconic").Call(s.hwnd.Load()); minimized != 0 {
		return
	}
	var bounds nativeRect
	if ok, _, _ := uiDLL.NewProc("GetWindowRect").Call(s.hwnd.Load(), uintptr(unsafe.Pointer(&bounds))); ok == 0 {
		return
	}
	monitorID, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(s.hwnd.Load(), 2)
	monitor, ok := monitorDetails(monitorID)
	if !ok {
		return
	}
	if s.placements == nil {
		s.placements = make(map[string]windowPlacement)
	}
	// A session cannot accumulate unbounded records from virtual displays.
	if _, exists := s.placements[monitor.name]; !exists && len(s.placements) >= 16 {
		for key := range s.placements {
			delete(s.placements, key)
			break
		}
	}
	s.placements[monitor.name] = rememberWindow(monitor.work, monitor.dpi, bounds)
}
