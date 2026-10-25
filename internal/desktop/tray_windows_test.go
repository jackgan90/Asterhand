//go:build windows

package desktop

import (
	"encoding/binary"
	"os"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func TestNativeIconFramesAndWin64Layouts(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) == 8 {
		if unsafe.Sizeof(notifyIconData{}) != 976 || unsafe.Sizeof(windowClass{}) != 80 || unsafe.Sizeof(winMessage{}) != 48 {
			t.Fatal("Win64 structure layout mismatch", unsafe.Sizeof(notifyIconData{}), unsafe.Sizeof(windowClass{}), unsafe.Sizeof(winMessage{}))
		}
	}
	for _, size := range []int{16, 20, 24, 32, 40, 48, 64} {
		frame, err := iconFrame(trayICO, size)
		if err != nil {
			t.Fatal(size, err)
		}
		h, _, err := createIcon.Call(uintptr(unsafe.Pointer(&frame[0])), uintptr(len(frame)), 1, 0x00030000, uintptr(size), uintptr(size), 0)
		if h == 0 {
			t.Fatal("Win32 rejected icon frame", size, err)
		}
		destroyIcon.Call(h)
	}
	bad := append([]byte(nil), trayICO...)
	binary.LittleEndian.PutUint32(bad[6+12:], uint32(len(bad)+1))
	if _, err := iconFrame(bad, 16); err == nil {
		t.Fatal("out of bounds icon accepted")
	}
	if _, err := iconFrame(trayICO[:6], 16); err == nil {
		t.Fatal("truncated directory accepted")
	}
}

// Opt-in because this test registers a visible icon on the current desktop.
// It targets only its own window; it never restarts Explorer or logs the user off.
func TestNativeTrayLifecycleOnInteractiveDesktop(t *testing.T) {
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("set ASTERHAND_DESKTOP_TEST=1 on an interactive Windows desktop")
	}
	explorer, _, _ := findWindow.Call(uintptr(unsafe.Pointer(textPointer("Shell_TrayWnd"))), 0)
	if explorer == 0 {
		t.Skip("Explorer taskbar unavailable")
	}
	var stopping atomic.Bool
	var opened atomic.Int32
	cleanup := make(chan struct{})
	tray, err := Start(Options{
		State:   func() State { return State{Name: "托盘验证", Ready: true, Stopping: stopping.Load()} },
		OpenWeb: func() error { opened.Add(1); return nil },
		Stop: func(bool) bool {
			if stopping.CompareAndSwap(false, true) {
				close(cleanup)
			}
			return true
		},
		CleanupDone: cleanup,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tray.Close()
	hwnd := tray.window.Load()
	assertRegistered(t, hwnd)
	sendNative(t, hwnd, wmTray, 0, uintptr(wmDoubleClick))
	if opened.Load() != 1 {
		t.Fatal("tray double click did not open app")
	}
	if result := sendNative(t, hwnd, wmQueryEndSession, 0, 0); result != 1 {
		t.Fatal("query end session blocked Windows")
	}
	sendNative(t, hwnd, wmEndSession, 0, 0)
	if stopping.Load() {
		t.Fatal("cancelled session ending stopped node")
	}
	data := notifyIconData{Window: hwnd, ID: 1}
	data.Size = uint32(unsafe.Sizeof(data))
	notifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
	sendNative(t, hwnd, tray.taskbarMessage, 0, 0)
	assertRegistered(t, hwnd)
	rect := [4]int32{0, 0, 1, 1}
	sendNative(t, hwnd, wmDPIChanged, 144|(144<<16), uintptr(unsafe.Pointer(&rect)))
	assertRegistered(t, hwnd)
	sendNative(t, hwnd, wmEndSession, 1, 0)
	if !stopping.Load() {
		t.Fatal("session end did not stop node")
	}
	// Stop is requested, but the icon must remain until the owner closes it.
	assertRegistered(t, hwnd)
	tray.Close()
	if iconRegistered(hwnd) {
		t.Fatal("tray icon survived cleanup")
	}
}

func sendNative(t *testing.T, hwnd uintptr, message uint32, wp, lp uintptr) uintptr {
	t.Helper()
	var result uintptr
	ok, _, err := user32.NewProc("SendMessageTimeoutW").Call(hwnd, uintptr(message), wp, lp, 2, 5000, uintptr(unsafe.Pointer(&result)))
	if ok == 0 {
		t.Fatal("native message timed out", message, err)
	}
	return result
}

func iconRegistered(hwnd uintptr) bool {
	type identifier struct {
		Size   uint32
		Window uintptr
		ID     uint32
		GUID   [16]byte
	}
	id := identifier{Window: hwnd, ID: 1}
	id.Size = uint32(unsafe.Sizeof(id))
	var rect [4]int32
	result, _, _ := shell32.NewProc("Shell_NotifyIconGetRect").Call(uintptr(unsafe.Pointer(&id)), uintptr(unsafe.Pointer(&rect)))
	return result == 0
}

func assertRegistered(t *testing.T, hwnd uintptr) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if iconRegistered(hwnd) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("Shell did not register the tray icon")
}
