//go:build windows

package quickinput

import (
	"os"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestNativeChatShortcutActivation(t *testing.T) {
	if os.Getenv("ASTERHAND_CHAT_GATE") != "1" {
		t.Skip("explicit desktop gate required")
	}
	if os.Getenv("ASTERHAND_CHAT_GATE_CHILD") != "1" {
		runChatGateAtMedium(t)
		return
	}
	s := backgroundExperienceSurface(t)
	activateShortcutFixture(t, s)
	waitChatScript(t, s, `document.querySelector('#input') && !document.querySelector('#input').disabled`)
	evaluate(t, s, `const input=document.querySelector('#input');input.value='shortcut draft';input.dispatchEvent(new Event('input'));true`)
	requireShortcutForeground(t, s, s.hwnd.Load())
	cover := shortcutCoverWindow(t, s)
	requireShortcutForeground(t, s, cover)
	if !experienceState(t, s).visible {
		t.Fatal("focus loss hid the chat window")
	}

	// IsWindowVisible remains true when another window covers the chat.
	s.Toggle()
	if state := experienceState(t, s); !state.visible || state.minimized {
		t.Fatalf("shortcut hid the covered window instead of recalling it: %+v", state)
	}
	requireShortcutForeground(t, s, s.hwnd.Load())
	waitChatScript(t, s, `document.hasFocus() && document.activeElement.id==='input' && document.querySelector('#input').value==='shortcut draft'`)

	s.Toggle()
	if experienceState(t, s).visible {
		t.Fatal("shortcut did not hide the foreground chat")
	}
	requireShortcutForeground(t, s, cover)
	s.Toggle()
	requireShortcutForeground(t, s, s.hwnd.Load())
	if !experienceState(t, s).visible {
		t.Fatal("shortcut did not show the hidden chat")
	}

	surfaceSync(t, s, func() {
		uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 6) // SW_MINIMIZE retains the taskbar entry.
		uiDLL.NewProc("SetForegroundWindow").Call(cover)
	})
	if state := experienceState(t, s); !state.visible || !state.minimized {
		t.Fatalf("minimize did not retain the window: %+v", state)
	}
	requireShortcutForeground(t, s, cover)
	s.Toggle()
	if state := experienceState(t, s); !state.visible || state.minimized {
		t.Fatalf("shortcut did not restore the minimized chat: %+v", state)
	}
	requireShortcutForeground(t, s, s.hwnd.Load())
	waitChatScript(t, s, `document.hasFocus() && document.activeElement.id==='input' && document.querySelector('#input').value==='shortcut draft'`)

	evaluate(t, s, `document.querySelector('#input').dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));true`)
	for deadline := time.Now().Add(time.Second); experienceState(t, s).visible; {
		if time.Now().After(deadline) {
			t.Fatal("Escape did not hide the foreground chat")
		}
		time.Sleep(10 * time.Millisecond)
	}
	requireShortcutForeground(t, s, cover)
	s.Toggle()
	waitChatScript(t, s, `document.hasFocus() && document.activeElement.id==='input' && document.querySelector('#input').value==='shortcut draft'`)
	t.Log("Covered/hidden/minimized chat recall, foreground shortcut/Escape hiding, previous-window focus and editor draft passed.")
}

func requireShortcutForeground(t *testing.T, s *nativeSurface, want uintptr) {
	t.Helper()
	var actual uintptr
	surfaceSync(t, s, func() { actual, _, _ = uiDLL.NewProc("GetForegroundWindow").Call() })
	if actual != want {
		t.Fatalf("foreground window = %x, want %x", actual, want)
	}
}

func activateShortcutFixture(t *testing.T, s *nativeSurface) {
	t.Helper()
	surfaceSync(t, s, func() { uiDLL.NewProc("SetForegroundWindow").Call(s.hwnd.Load()) })
	// A desktop runner may need a click on this isolated window to receive
	// foreground permission. Do not inject a key into the user's current app.
	t.Log("Waiting for the isolated chat window to become foreground; activate it if Windows blocks the test runner.")
	for deadline := time.Now().Add(30 * time.Second); ; {
		var foreground uintptr
		surfaceSync(t, s, func() { foreground, _, _ = uiDLL.NewProc("GetForegroundWindow").Call() })
		if foreground == s.hwnd.Load() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("test fixture needs foreground activation on the interactive desktop")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A second ordinary top-level window on the same UI thread provides real
// focus loss and overlap without depending on any application already open.
func shortcutCoverWindow(t *testing.T, s *nativeSurface) uintptr {
	t.Helper()
	var hwnd uintptr
	surfaceSync(t, s, func() {
		var bounds nativeRect
		uiDLL.NewProc("GetWindowRect").Call(s.hwnd.Load(), uintptr(unsafe.Pointer(&bounds)))
		class, _ := windows.UTF16PtrFromString("STATIC")
		title, _ := windows.UTF16PtrFromString("Asterhand shortcut test")
		var err error
		hwnd, _, err = uiDLL.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)),
			0x00cf0000, uintptr(bounds.Left), uintptr(bounds.Top), uintptr(bounds.Right-bounds.Left), uintptr(bounds.Bottom-bounds.Top), 0, 0, s.instance, 0)
		if hwnd == 0 {
			t.Errorf("create covering window: %v", err)
			return
		}
		uiDLL.NewProc("ShowWindow").Call(hwnd, 9)
		uiDLL.NewProc("SetForegroundWindow").Call(hwnd)
	})
	if hwnd == 0 {
		t.Fatal("covering window unavailable")
	}
	t.Cleanup(func() {
		surfaceSync(t, s, func() { uiDLL.NewProc("DestroyWindow").Call(hwnd) })
	})
	return hwnd
}
