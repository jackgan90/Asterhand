//go:build windows

package quickinput

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A visible, opt-in test broker for real keyboard/IME acceptance. The input UI
// is always the separately built production binary, with no injected scripts.
type interactiveBroker struct {
	c        *Controller
	hwnd     uintptr
	commands chan func()
	next     int
	label    uintptr
	exiting  bool
}

var interactiveBrokers sync.Map
var interactiveProcedure = windows.NewCallback(func(hwnd uintptr, msg uint32, w, l uintptr) uintptr {
	value, ok := interactiveBrokers.Load(hwnd)
	if !ok {
		v, _, _ := uiDLL.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), w, l)
		return v
	}
	b := value.(*interactiveBroker)
	switch msg {
	case 0x8040:
		select {
		case command := <-b.commands:
			command()
		default:
		}
		return 0
	case 0x312:
		b.c.OnHotkey(int(w), uint32(l&0xffff), uint32(l>>16))
		return 0
	case 0x113:
		text, revision := b.c.draft.Read()
		status := b.c.Status()
		label, _ := windows.UTF16PtrFromString(fmt.Sprintf("%s | 草稿版本 %d | %s", status.MenuLabel(), revision, text))
		uiDLL.NewProc("SetWindowTextW").Call(b.label, uintptr(unsafe.Pointer(label)))
		return 0
	case 0x111:
		switch w & 0xffff {
		case 10:
			b.c.mu.Lock()
			surface := b.c.surface
			b.c.mu.Unlock()
			if s, ok := surface.(*remoteSurface); ok {
				p := s.process.(*windowsHostProcess)
				_ = windows.TerminateProcess(p.process, 1)
			}
		case 11:
			go func() { _ = b.c.Open() }()
		case 12:
			uiDLL.NewProc("PostMessageW").Call(hwnd, 0x10, 0, 0)
		}
		return 0
	case 0x10:
		if !b.exiting {
			b.exiting = true
			b.c.BeginStop()
			go func() { b.c.Close(); uiDLL.NewProc("PostMessageW").Call(hwnd, 0x8041, 0, 0) }()
		}
		return 0
	case 0x8041:
		uiDLL.NewProc("DestroyWindow").Call(hwnd)
		return 0
	case 2:
		uiDLL.NewProc("PostQuitMessage").Call(0)
		return 0
	}
	v, _, _ := uiDLL.NewProc("DefWindowProcW").Call(hwnd, uintptr(msg), w, l)
	return v
})

func (b *interactiveBroker) call(fn func() error) error {
	done := make(chan error, 1)
	b.commands <- func() { done <- fn() }
	uiDLL.NewProc("PostMessageW").Call(b.hwnd, 0x8040, 0, 0)
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		return fmt.Errorf("broker thread timed out")
	}
}
func (b *interactiveBroker) Register(binding Binding) (int, error) {
	var id int
	err := b.call(func() error {
		b.next++
		id = b.next
		mods, key, err := binding.WindowsKeys()
		if err != nil {
			return err
		}
		ok, _, err := uiDLL.NewProc("RegisterHotKey").Call(b.hwnd, uintptr(id), uintptr(mods), uintptr(key))
		if ok == 0 {
			return err
		}
		return nil
	})
	return id, err
}
func (b *interactiveBroker) Unregister(id int) error {
	return b.call(func() error {
		ok, _, err := uiDLL.NewProc("UnregisterHotKey").Call(b.hwnd, uintptr(id))
		if ok == 0 {
			return err
		}
		return nil
	})
}

func TestIsolatedInteractiveBroker(t *testing.T) {
	if os.Getenv("ASTERHAND_INPUT_INTERACTIVE") != "1" {
		t.Skip("explicit visible keyboard acceptance broker only")
	}
	directory := os.Getenv("ASTERHAND_INPUT_HOST_TEST_DIRECTORY")
	if directory == "" {
		t.Fatal("built production host required")
	}
	runtime.LockOSThread()
	c := New(t.TempDir())
	b := &interactiveBroker{c: c, commands: make(chan func(), 32), next: 9000}
	instance, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW").Call(0)
	class, _ := windows.UTF16PtrFromString(fmt.Sprintf("Asterhand.IsolationValidation.%d", os.Getpid()))
	wc := nativeWindowClass{Procedure: interactiveProcedure, Instance: instance, ClassName: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if ok, _, err := uiDLL.NewProc("RegisterClassExW").Call(uintptr(unsafe.Pointer(&wc))); ok == 0 {
		t.Fatal(err)
	}
	defer uiDLL.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(class)), instance)
	title, _ := windows.UTF16PtrFromString("Asterhand Isolation Validation")
	b.hwnd, _, _ = uiDLL.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0x00cf0000, 80, 80, 900, 230, 0, 0, instance, 0)
	if b.hwnd == 0 {
		t.Fatal("test broker window failed")
	}
	interactiveBrokers.Store(b.hwnd, b)
	defer interactiveBrokers.Delete(b.hwnd)
	static, _ := windows.UTF16PtrFromString("STATIC")
	label, _ := windows.UTF16PtrFromString("等待独立宿主就绪，使用 Ctrl+Alt+J 验证真实快捷键")
	b.label, _, _ = uiDLL.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(static)), uintptr(unsafe.Pointer(label)), 0x50000000, 20, 20, 850, 70, b.hwnd, 1, instance, 0)
	button, _ := windows.UTF16PtrFromString("BUTTON")
	for i, value := range []string{"结束 UI 验证恢复", "打开快捷输入", "结束测试"} {
		text, _ := windows.UTF16PtrFromString(value)
		uiDLL.NewProc("CreateWindowExW").Call(0, uintptr(unsafe.Pointer(button)), uintptr(unsafe.Pointer(text)), 0x50010000, uintptr(20+i*250), 110, 230, 35, b.hwnd, uintptr(10+i), instance, 0)
	}
	c.Attach(b, func() (Surface, error) {
		c.mu.Lock()
		generation := c.surfaceGeneration
		c.mu.Unlock()
		return startRemoteSurface(c.home, c.Status().Desired.Theme, &c.draft, func() (hostProcess, error) { return launchWindowsHostFrom(directory) }, func(err error) { c.failedGeneration(generation, err) }, c.Status().RecoveryNotice != "")
	})
	uiDLL.NewProc("SetTimer").Call(b.hwnd, 1, 250, 0)
	uiDLL.NewProc("ShowWindow").Call(b.hwnd, 4)
	timer := time.AfterFunc(15*time.Minute, func() { uiDLL.NewProc("PostMessageW").Call(b.hwnd, 0x10, 0, 0) })
	defer timer.Stop()
	var msg nativeMessage
	for {
		r, _, _ := uiDLL.NewProc("GetMessageW").Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		uiDLL.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&msg)))
		uiDLL.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&msg)))
	}
}
