//go:build windows

package quickinput

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var dwmDLL = windows.NewLazySystemDLL("dwmapi.dll")

const (
	dwmWindowCornerPreference = 33 // DWMWA_WINDOW_CORNER_PREFERENCE (Windows 11)
	dwmBorderColor            = 34 // DWMWA_BORDER_COLOR (Windows 11)
	dwmCornerRound            = 2  // DWMWCP_ROUND
)

// Theme line colors as COLORREF (0x00BBGGRR), matching --line in input.css.
var frameColors = map[string]uint32{"star": 0x005e4735, "frost": 0x00e3ccb5}

func setWindowAttribute(hwnd uintptr, attribute uintptr, value uint32) bool {
	proc := dwmDLL.NewProc("DwmSetWindowAttribute")
	if proc.Find() != nil {
		return false
	}
	result, _, _ := proc.Call(hwnd, attribute, uintptr(unsafe.Pointer(&value)), unsafe.Sizeof(value))
	return result == 0
}

// The page cannot clip the native window, so the quick-input panel asks the
// system for rounded corners. Systems without the attribute keep square
// corners, and the page then draws its own square border.
func (s *nativeSurface) applyWindowFrame() {
	if s.log != nil {
		return
	}
	s.nativeCorners = setWindowAttribute(s.hwnd.Load(), dwmWindowCornerPreference, dwmCornerRound)
	s.applyFrameColor()
}

func (s *nativeSurface) applyFrameColor() {
	if s.log != nil || !s.nativeCorners {
		return
	}
	color, ok := frameColors[s.theme]
	if !ok {
		color = frameColors["star"]
	}
	setWindowAttribute(s.hwnd.Load(), dwmBorderColor, color)
}
