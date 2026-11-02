//go:build windows

package quickinput

import (
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"golang.org/x/sys/windows"
)

var logGDI = windows.NewLazySystemDLL("gdi32.dll")

type logPaint struct {
	DC              uintptr
	Erase           int32
	Rect            nativeRect
	Restore, Update int32
	Reserved        [32]byte
}

func (s *nativeSurface) invalidateLogShell() {
	if s.log != nil {
		uiDLL.NewProc("InvalidateRect").Call(s.hwnd.Load(), 0, 1)
	}
}

func (s *nativeSurface) paintLogShell(hwnd uintptr) {
	var paint logPaint
	dc, _, _ := uiDLL.NewProc("BeginPaint").Call(hwnd, uintptr(unsafe.Pointer(&paint)))
	defer uiDLL.NewProc("EndPaint").Call(hwnd, uintptr(unsafe.Pointer(&paint)))
	var rect nativeRect
	uiDLL.NewProc("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	background, foreground := uintptr(0x20130c), uintptr(0xf0e8dc)
	if s.theme == "frost" {
		background, foreground = 0xfaf4ee, 0x442d19
	}
	brush, _, _ := logGDI.NewProc("CreateSolidBrush").Call(background)
	defer logGDI.NewProc("DeleteObject").Call(brush)
	uiDLL.NewProc("FillRect").Call(dc, uintptr(unsafe.Pointer(&rect)), brush)
	dpi, _, _ := uiDLL.NewProc("GetDpiForWindow").Call(hwnd)
	if dpi == 0 {
		dpi = 96
	}
	face, _ := windows.UTF16PtrFromString("Segoe UI")
	font, _, _ := logGDI.NewProc("CreateFontW").Call(uintptr(-int64(scaled(16, uint32(dpi)))), 0, 0, 0, 400, 0, 0, 0, 1, 0, 0, 5, 0, uintptr(unsafe.Pointer(face)))
	old, _, _ := logGDI.NewProc("SelectObject").Call(dc, font)
	defer func() {
		logGDI.NewProc("SelectObject").Call(dc, old)
		logGDI.NewProc("DeleteObject").Call(font)
	}()
	logGDI.NewProc("SetBkMode").Call(dc, 1)
	logGDI.NewProc("SetTextColor").Call(dc, foreground)
	key := "quickinput.log.loading"
	if s.log.failed.Load() {
		key = "quickinput.log.failed_retry"
	}
	text, _ := windows.UTF16PtrFromString(i18n.T(s.locale, key))
	padding := scaled(24, uint32(dpi))
	textRect := rect
	textRect.Left += padding
	textRect.Right = max(textRect.Left+1, textRect.Right-padding)
	uiDLL.NewProc("DrawTextW").Call(dc, uintptr(unsafe.Pointer(text)), ^uintptr(0), uintptr(unsafe.Pointer(&textRect)), 0x411)
	height := textRect.Bottom - textRect.Top
	textRect.Top = max(0, (rect.Bottom-height)/2)
	textRect.Bottom = rect.Bottom
	uiDLL.NewProc("DrawTextW").Call(dc, uintptr(unsafe.Pointer(text)), ^uintptr(0), uintptr(unsafe.Pointer(&textRect)), 0x11)
}

func (s *nativeSurface) showLogFailure() {
	if s.closing {
		return
	}
	s.log.failed.Store(true)
	s.contentVisible = false
	if s.controller != nil {
		_ = s.controller.call(4, 0)
	}
	s.invalidateLogShell()
}
