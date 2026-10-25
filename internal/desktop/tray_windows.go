//go:build windows

package desktop

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	trayWindows    sync.Map
	classSequence  atomic.Uint32
	windowCallback = windows.NewCallback(trayWindowProc)
)

type Tray struct {
	options        Options
	window         atomic.Uintptr
	done           chan struct{}
	className      *uint16
	instance       uintptr
	taskbarMessage uint32
	icon           uintptr
	iconSize       int
	registered     bool
	lastTip        string
	menuActive     bool
}

func Start(options Options) (*Tray, error) {
	if options.State == nil || options.OpenWeb == nil || options.Stop == nil {
		return nil, fmt.Errorf("托盘缺少节点生命周期回调")
	}
	t := &Tray{options: options, done: make(chan struct{})}
	ready := make(chan error, 1)
	go t.run(ready)
	if err := <-ready; err != nil {
		<-t.done
		return nil, err
	}
	return t, nil
}

// Close is called only after node cleanup. Keeping the icon until then makes
// slow shutdown visible and prevents an apparently stopped but live node.
func (t *Tray) Close() {
	select {
	case <-t.done:
		return
	default:
	}
	postMessage.Call(t.window.Load(), wmDispose, 0, 0)
	<-t.done
}

func (t *Tray) run(ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(t.done)
	if err := t.initialize(); err != nil {
		t.dispose()
		ready <- err
		return
	}
	defer t.dispose()
	defer t.options.Stop(true)
	ready <- nil
	var msg winMessage
	for {
		result, _, err := getMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) == -1 {
			log.Printf("托盘消息循环失败，停止节点: %v", err)
			t.options.Stop(true)
			return
		}
		if result == 0 {
			return
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

func (t *Tray) initialize() error {
	if setDPIAwareness.Find() == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2; older Windows may refuse it.
		setDPIAwareness.Call(^uintptr(3))
	}
	t.instance, _, _ = moduleHandle.Call(0)
	t.className = textPointer(fmt.Sprintf("Asterhand.Tray.%d.%d", os.Getpid(), classSequence.Add(1)))
	cls := windowClass{Style: 8, Procedure: windowCallback, Instance: t.instance, ClassName: t.className}
	cls.Size = uint32(unsafe.Sizeof(cls))
	if ok, _, err := registerClass.Call(uintptr(unsafe.Pointer(&cls))); ok == 0 {
		return fmt.Errorf("注册托盘窗口失败: %w", err)
	}
	hwnd, _, err := createWindow.Call(0x80, uintptr(unsafe.Pointer(t.className)), uintptr(unsafe.Pointer(textPointer("Asterhand"))), 0, 0, 0, 0, 0, 0, 0, t.instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("创建托盘窗口失败: %w", err)
	}
	t.window.Store(hwnd)
	trayWindows.Store(hwnd, t)
	message, _, err := registerMessage.Call(uintptr(unsafe.Pointer(textPointer("TaskbarCreated"))))
	if message == 0 {
		return fmt.Errorf("注册任务栏恢复消息失败: %w", err)
	}
	t.taskbarMessage = uint32(message)
	if err = t.reloadIcon(); err != nil {
		return err
	}
	if timer, _, err := setTimer.Call(hwnd, 1, 1000, 0); timer == 0 {
		return fmt.Errorf("创建托盘状态定时器失败: %w", err)
	}
	// Explorer may not exist yet during sign-in. Retry without restarting the node.
	t.updateIcon()
	return nil
}

func (t *Tray) dispose() {
	hwnd := t.window.Load()
	if hwnd != 0 {
		data := t.iconData()
		notifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
		killTimer.Call(hwnd, 1)
		destroyWindow.Call(hwnd)
		trayWindows.Delete(hwnd)
		t.window.Store(0)
	}
	if t.icon != 0 {
		destroyIcon.Call(t.icon)
		t.icon = 0
	}
	if t.className != nil {
		unregisterClass.Call(uintptr(unsafe.Pointer(t.className)), t.instance)
	}
}

func trayWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	entry, ok := trayWindows.Load(hwnd)
	if !ok {
		result, _, _ := defaultWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
		return result
	}
	t := entry.(*Tray)
	if message == t.taskbarMessage {
		t.registered = false
		t.refreshIcon()
		return 0
	}
	switch message {
	case wmTray:
		t.handleIconEvent(uint32(lParam & 0xffff))
	case wmTimer:
		t.updateIcon()
	case wmSettingChange, wmDPIChanged:
		t.refreshIcon()
	case wmQueryEndSession:
		return 1
	case wmEndSession:
		if wParam != 0 {
			t.options.Stop(true)
			// The OS can end this session after the handler returns. Allow bounded
			// persistence time, without presenting a shutdown-blocking dialog.
			select {
			case <-t.options.CleanupDone:
			case <-time.After(4 * time.Second):
			}
		}
	case wmClose:
		t.options.Stop(true)
	case wmDispose:
		destroyWindow.Call(hwnd)
	case wmDestroy:
		postQuit.Call(0)
	default:
		result, _, _ := defaultWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
		return result
	}
	return 0
}
