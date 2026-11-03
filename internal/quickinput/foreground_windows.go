//go:build windows

package quickinput

const allowAnyForeground = ^uintptr(0) // ASFW_ANY

// Only the foreground process can grant activation rights. The quick-input
// host is foreground when the user clicks a log button; the node service that
// relays the open request is not, so its own grant to the log host is refused.
func allowForegroundTransfer() {
	uiDLL.NewProc("AllowSetForegroundWindow").Call(allowAnyForeground)
}

// activateForeground asks Windows to activate an already shown window. It
// succeeds when the clicking process granted activation (quick panel). When
// Windows refuses (for example a request relayed from the browser), the window
// stays shown and its taskbar entry flashes until the user switches to it. It
// deliberately does not attach to another thread's input queue: a stalled
// foreground application would then block this window's UI thread.
func (s *nativeSurface) activateForeground() bool {
	hwnd := s.hwnd.Load()
	uiDLL.NewProc("SetForegroundWindow").Call(hwnd)
	if foreground, _, _ := uiDLL.NewProc("GetForegroundWindow").Call(); foreground == hwnd {
		return true
	}
	s.flashTaskbar(flashTaskbarUntilForeground)
	return false
}
