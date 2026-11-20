//go:build windows

package quickinput

import (
	"context"
	"runtime"
	"testing"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/notifications"
	"golang.org/x/sys/windows"
)

// The test window accepts placement but suppresses Z-order changes until it
// receives HWND_BOTTOM. This models the observed Win32 result on any desktop.
func TestNotificationRevealVerifiesWindowOrder(t *testing.T) {
	requireNativeDesktop(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var reset, denyAll, demoting bool
	proc := windows.NewCallback(func(hwnd uintptr, msg uint32, w, l uintptr) uintptr {
		if msg == 0x0047 && !demoting { // WM_WINDOWPOSCHANGED, valid only during this call.
			var pos *struct {
				window, after uintptr
				x, y, cx, cy  int32
				flags         uint32
			}
			*(*uintptr)(unsafe.Pointer(&pos)) = l
			if pos.flags&0x0004 == 0 { // Windows resolves HWND_BOTTOM to an actual predecessor.
				ex, _, _ := uiDLL.NewProc("GetWindowLongPtrW").Call(hwnd, ^uintptr(19))
				visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd)
				if ex&8 == 0 && visible == 0 {
					reset = true
				} else if ex&8 != 0 && (!reset || denyAll) {
					demoting = true
					uiDLL.NewProc("SetWindowPos").Call(hwnd, ^uintptr(1), 0, 0, 0, 0, 0x0013)
					demoting = false
				}
			}
		}
		return defaultWindowEvent(hwnd, msg, w, l)
	})
	class, _ := windows.UTF16PtrFromString("Asterhand.NotificationOrderTest")
	instance, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	wc := nativeWindowClass{Procedure: proc, Instance: instance, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if ok, _, err := uiDLL.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&wc))); ok == 0 {
		t.Fatal(err)
	}
	defer uiDLL.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(class)), instance)
	for _, blocked := range []bool{false, true} {
		hwnd, _, err := uiDLL.NewProc("CreateWindowExW").Call(0x08080080, uintptr(unsafe.Pointer(class)), 0,
			0x80000000, 0, 0, 1, 1, 0, 0, instance, 0)
		if hwnd == 0 {
			t.Fatal(err)
		}
		reset, denyAll = false, blocked
		peer := &hostPeer{ctx: context.Background(), out: make(chan hostDelivery, 1), failures: make(chan error, 1)}
		s := &nativeSurface{notification: &notificationSurface{
			desktop: &notificationDesktop{peer: peer}, card: notifications.Card{ID: 1, Phase: "entering"},
			target: nativeRect{Right: 1, Bottom: 1}, reduced: true,
		}}
		s.hwnd.Store(hwnd)
		s.revealNotification()
		ex, _, _ := uiDLL.NewProc("GetWindowLongPtrW").Call(hwnd, ^uintptr(19))
		visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd)
		uiDLL.NewProc("DestroyWindow").Call(hwnd)
		if blocked {
			if len(peer.failures) != 1 || len(peer.out) != 0 || visible != 0 {
				t.Errorf("refused placement: ex=%#x failures=%d events=%d visible=%d", ex, len(peer.failures), len(peer.out), visible)
			}
		} else if !reset || ex&8 == 0 || visible == 0 || len(peer.failures) != 0 || len(peer.out) != 1 {
			t.Errorf("recoverable placement: reset=%t ex=%#x visible=%d failures=%d events=%d", reset, ex, visible, len(peer.failures), len(peer.out))
		} else if event := (<-peer.out).message.Notification; event.Kind != "entered" {
			t.Errorf("unexpected event %+v", event)
		}
	}
}
