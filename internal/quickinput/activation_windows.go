//go:build windows

package quickinput

import (
	"time"
	"unsafe"
)

type windowsActivation struct {
	window  uintptr
	input   uint32
	created time.Time
}

func lastInputTick() uint32 {
	var info struct{ Size, Time uint32 }
	info.Size = uint32(unsafe.Sizeof(info))
	if ok, _, _ := uiDLL.NewProc("GetLastInputInfo").Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		return 0
	}
	return info.Time
}

func captureActivation() activationIntent {
	window, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
	return windowsActivation{window, lastInputTick(), time.Now()}
}

func (a windowsActivation) valid() bool {
	window, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
	if window != a.window || a.window == 0 || time.Since(a.created) > hostStartTimeout {
		return false
	}
	last := lastInputTick()
	// Modifier/key releases immediately after WM_HOTKEY are part of the same
	// gesture. Later input invalidates a delayed activation even in one window.
	return a.input != 0 && (last == a.input || uint32(last-a.input) <= 250)
}
