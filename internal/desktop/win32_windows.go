//go:build windows

package desktop

import "golang.org/x/sys/windows"

const (
	wmDestroy         = 0x0002
	wmClose           = 0x0010
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wmSettingChange   = 0x001A
	wmContextMenu     = 0x007B
	wmTimer           = 0x0113
	wmDoubleClick     = 0x0203
	wmDPIChanged      = 0x02E0
	wmTray            = 0x8001
	wmDispose         = 0x8002
	ninKeySelect      = 0x0401
	nimAdd            = 0
	nimModify         = 1
	nimDelete         = 2
	nimSetVersion     = 4
	menuOpen          = 1
	menuLogs          = 2
	menuExit          = 3
)

type point struct{ X, Y int32 }

type winMessage struct {
	Window  uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Point   point
	Private uint32
}

type windowClass struct {
	Size        uint32
	Style       uint32
	Procedure   uintptr
	ClassExtra  int32
	WindowExtra int32
	Instance    uintptr
	Icon        uintptr
	Cursor      uintptr
	Background  uintptr
	MenuName    *uint16
	ClassName   *uint16
	SmallIcon   uintptr
}

type notifyIconData struct {
	Size        uint32
	Window      uintptr
	ID          uint32
	Flags       uint32
	Callback    uint32
	Icon        uintptr
	Tip         [128]uint16
	State       uint32
	StateMask   uint32
	Info        [256]uint16
	Version     uint32
	InfoTitle   [64]uint16
	InfoFlags   uint32
	GUID        windows.GUID
	BalloonIcon uintptr
}

var (
	registerClass       = user32.NewProc("RegisterClassExW")
	unregisterClass     = user32.NewProc("UnregisterClassW")
	createWindow        = user32.NewProc("CreateWindowExW")
	destroyWindow       = user32.NewProc("DestroyWindow")
	defaultWindowProc   = user32.NewProc("DefWindowProcW")
	getMessage          = user32.NewProc("GetMessageW")
	translateMessage    = user32.NewProc("TranslateMessage")
	dispatchMessage     = user32.NewProc("DispatchMessageW")
	postMessage         = user32.NewProc("PostMessageW")
	postQuit            = user32.NewProc("PostQuitMessage")
	registerMessage     = user32.NewProc("RegisterWindowMessageW")
	setTimer            = user32.NewProc("SetTimer")
	killTimer           = user32.NewProc("KillTimer")
	createPopupMenu     = user32.NewProc("CreatePopupMenu")
	appendMenu          = user32.NewProc("AppendMenuW")
	destroyMenu         = user32.NewProc("DestroyMenu")
	trackPopupMenu      = user32.NewProc("TrackPopupMenu")
	getCursorPosition   = user32.NewProc("GetCursorPos")
	setForegroundWindow = user32.NewProc("SetForegroundWindow")
	findWindow          = user32.NewProc("FindWindowW")
	getDPIForWindow     = user32.NewProc("GetDpiForWindow")
	getSystemMetrics    = user32.NewProc("GetSystemMetrics")
	getMetricsForDPI    = user32.NewProc("GetSystemMetricsForDpi")
	setDPIAwareness     = user32.NewProc("SetProcessDpiAwarenessContext")
	createIcon          = user32.NewProc("CreateIconFromResourceEx")
	destroyIcon         = user32.NewProc("DestroyIcon")
	notifyIcon          = shell32.NewProc("Shell_NotifyIconW")
	moduleHandle        = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
)
