//go:build windows

package quickinput

import "unsafe"

const flashTaskbarUntilForeground = 0x00000002 | 0x0000000c // FLASHW_TRAY | FLASHW_TIMERNOFG

// Field order and natural pointer padding match Win32 FLASHWINFO on amd64/arm64.
type flashWindowInfo struct {
	Size    uint32
	Window  uintptr
	Flags   uint32
	Count   uint32
	Timeout uint32
}

// All attention state and window queries belong to the native UI thread.
func (s *nativeSurface) replyCompleted() {
	if s.closing || s.attention || s.hwnd.Load() == 0 {
		return
	}
	hwnd := s.hwnd.Load()
	visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd)
	foreground, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
	if visible == 0 || foreground == hwnd {
		return
	}
	s.attention = true
	s.flashTaskbar(flashTaskbarUntilForeground)
}

func (s *nativeSurface) stopAttention() {
	if !s.attention {
		return
	}
	s.attention = false
	s.flashTaskbar(0) // FLASHW_STOP
}

func (s *nativeSurface) flashTaskbar(flags uint32) {
	hwnd := s.hwnd.Load()
	if hwnd == 0 {
		return
	}
	info := flashWindowInfo{Window: hwnd, Flags: flags}
	if flags != 0 {
		info.Count = ^uint32(0)
	}
	info.Size = uint32(unsafe.Sizeof(info))
	// Return value is the previous active-caption state, not a success flag.
	uiDLL.NewProc("FlashWindowEx").Call(uintptr(unsafe.Pointer(&info)))
}
