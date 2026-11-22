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

func TestNotificationStackAvoidsWorkerActionPopup(t *testing.T) {
	work := windowBounds{Left: 0, Top: 0, Right: 1920, Bottom: 1040}
	cards := []notifications.Card{
		{ID: 1, Phase: "visible"},
		{ID: 2, Phase: "visible"},
	}
	heights := map[uint64]int32{1: 100, 2: 110}

	for _, dpi := range []uint32{96, 144} {
		actionHeight := int32(280)
		ab := workerActionBounds(work, dpi, actionHeight)
		baseBelow := actionHeight + workerActionGap
		placed := notificationStackBoundsWithBase(work, dpi, cards, notifications.MaxVisible, heights, baseBelow)
		if len(placed) != 2 {
			t.Fatalf("dpi=%d: expected 2 notification placements, got %d", dpi, len(placed))
		}
		// Bottom-most local command notification (placed[1]) must sit strictly ABOVE
		// the worker action center popup with zero vertical overlap.
		if placed[1].Bottom >= ab.Top {
			t.Fatalf("dpi=%d: notification overlaps worker action popup: notif.Bottom=%d >= action.Top=%d", dpi, placed[1].Bottom, ab.Top)
		}
		// Upper notification (placed[0]) sits strictly above placed[1].
		if placed[0].Bottom >= placed[1].Top {
			t.Fatalf("dpi=%d: stacked notifications overlap: upper.Bottom=%d >= lower.Top=%d", dpi, placed[0].Bottom, placed[1].Top)
		}
	}
}

func TestWorkerActionRevealVerifiesTopmostOrder(t *testing.T) {
	requireNativeDesktop(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var reset, demoting bool
	proc := windows.NewCallback(func(hwnd uintptr, msg uint32, w, l uintptr) uintptr {
		if msg == 0x0047 && !demoting {
			var pos *struct {
				window, after uintptr
				x, y, cx, cy  int32
				flags         uint32
			}
			*(*uintptr)(unsafe.Pointer(&pos)) = l
			if pos.flags&0x0004 == 0 {
				ex, _, _ := uiDLL.NewProc("GetWindowLongPtrW").Call(hwnd, ^uintptr(19))
				visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd)
				if ex&8 == 0 && visible == 0 {
					reset = true
				} else if ex&8 != 0 && !reset {
					demoting = true
					uiDLL.NewProc("SetWindowPos").Call(hwnd, ^uintptr(1), 0, 0, 0, 0, 0x0013)
					demoting = false
				}
			}
		}
		return defaultWindowEvent(hwnd, msg, w, l)
	})
	class, _ := windows.UTF16PtrFromString("Asterhand.WorkerActionOrderTest")
	instance, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	wc := nativeWindowClass{Procedure: proc, Instance: instance, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if ok, _, err := uiDLL.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&wc))); ok == 0 {
		t.Fatal(err)
	}
	defer uiDLL.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(class)), instance)

	// Interactive popup style: TOOLWINDOW | LAYERED (without permanent WS_EX_NOACTIVATE).
	hwnd, _, err := uiDLL.NewProc("CreateWindowExW").Call(0x00080080, uintptr(unsafe.Pointer(class)), 0,
		0x80000000, 0, 0, 420, 280, 0, 0, instance, 0)
	if hwnd == 0 {
		t.Fatal(err)
	}
	defer uiDLL.NewProc("DestroyWindow").Call(hwnd)

	peer := &hostPeer{ctx: context.Background(), out: make(chan hostDelivery, 1), failures: make(chan error, 1)}
	s := &nativeSurface{workerAction: &workerActionSurface{
		desktop: &notificationDesktop{peer: peer},
		items:   []notifications.WorkerInteraction{{RequestID: "wir_1", Kind: "approval", Summary: "Run cmd"}},
		target:  nativeRect{Left: 100, Top: 100, Right: 520, Bottom: 380},
	}}
	s.hwnd.Store(hwnd)
	s.revealWorkerAction()

	ex, _, _ := uiDLL.NewProc("GetWindowLongPtrW").Call(hwnd, ^uintptr(19))
	visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd)
	if !reset || ex&wsExTopmost == 0 || visible == 0 || len(peer.failures) != 0 {
		t.Fatalf("worker action reveal failed z-order verification: reset=%t ex=%#x visible=%d failures=%d", reset, ex, visible, len(peer.failures))
	}
}
