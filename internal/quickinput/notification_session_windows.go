//go:build windows

package quickinput

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var notificationWTS = windows.NewLazySystemDLL("wtsapi32.dll")

// Prefix of WTSINFOEXW. Its level-1 union is aligned to eight bytes by the
// Windows ABI because it contains LARGE_INTEGER fields beyond this prefix.
type notificationSessionInfo struct {
	Level        uint32
	_            uint32
	SessionID    uint32
	State        uint32
	SessionFlags int32
}

func (s notificationSessionInfo) visible() bool {
	return s.Level == 1 && s.State == 0 && s.SessionFlags == 1 // WTSActive, WTS_SESSIONSTATE_UNLOCK
}

// Read the current process's session, including when the host starts after a
// lock/disconnect event. Unknown state stays paused until the next change.
func notificationSessionVisible() bool {
	var data *notificationSessionInfo
	var size uint32
	const currentSession, sessionInfoEx = 0xffffffff, 25
	ok, _, _ := notificationWTS.NewProc("WTSQuerySessionInformationW").Call(0, currentSession, sessionInfoEx,
		uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&size)))
	if ok == 0 {
		return false
	}
	defer notificationWTS.NewProc("WTSFreeMemory").Call(uintptr(unsafe.Pointer(data)))
	return data != nil && size >= uint32(unsafe.Sizeof(*data)) && data.visible()
}
