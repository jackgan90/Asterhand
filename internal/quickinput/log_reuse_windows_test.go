//go:build windows

package quickinput

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
	"golang.org/x/sys/windows"
)

type logTestOutput struct {
	writer io.Writer
	lines  chan string
}

func (w logTestOutput) Write(data []byte) (int, error) {
	select {
	case w.lines <- string(data):
	default:
	}
	return w.writer.Write(data)
}

func desktopLogHost(t *testing.T, home string) (*logHost, <-chan struct{}) {
	t.Helper()
	directory := os.Getenv("ASTERHAND_INPUT_HOST_TEST_DIRECTORY")
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" || directory == "" {
		t.Skip("requires isolated host and interactive Windows desktop")
	}
	status := make(chan struct{}, 64)
	h := newLogWindowHost(home, func(_ context.Context, request commandswire.Request) commandswire.Response {
		if request.Action == "status" {
			select {
			case status <- struct{}{}:
			default:
			}
		}
		return commandswire.Response{Run: json.RawMessage(`null`)}
	}, func() (hostProcess, error) { return launchWindowsHostMode(directory, true) }).(*logHost)
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	return h, status
}

func visibleLogWindow(t *testing.T, h *logHost) uintptr {
	t.Helper()
	h.mu.Lock()
	p := h.connection.process.(*windowsHostProcess)
	h.mu.Unlock()
	var result uintptr
	callback := windows.NewCallback(func(hwnd, _ uintptr) uintptr {
		var pid uint32
		uiDLL.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		if pid != p.pid {
			return 1
		}
		var name [256]uint16
		uiDLL.NewProc("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&name[0])), 256)
		visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd)
		if visible != 0 && strings.HasPrefix(windows.UTF16ToString(name[:]), "Asterhand.QuickInput.") {
			result = hwnd
			return 0
		}
		return 1
	})
	uiDLL.NewProc("EnumWindows").Call(callback, 0)
	if result == 0 {
		t.Fatal("Open succeeded without a visible native window")
	}
	return result
}

func openDesktopLog(t *testing.T, h *logHost, id string, anchor *hostipc.WindowAnchor) uintptr {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if anchor != nil {
		ctx = context.WithValue(ctx, logAnchorContextKey{}, *anchor)
	}
	start := time.Now()
	if err := h.Open(ctx, id); err != nil {
		t.Fatal(err)
	}
	t.Logf("native window open: %s", time.Since(start))
	return visibleLogWindow(t, h)
}

func waitLogHidden(t *testing.T, hwnd uintptr) {
	t.Helper()
	uiDLL.NewProc("PostMessageW").Call(hwnd, 0x0010, 0, 0)
	deadline := time.Now().Add(time.Second)
	for {
		visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd)
		if visible == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("close did not promptly hide window")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestManagedLogReusesWindowAndRestoresMinimized(t *testing.T) {
	h, status := desktopLogHost(t, t.TempDir())
	id := model.ID()
	hwnd := openDesktopLog(t, h, id, nil)
	select {
	case <-status:
	case <-time.After(16 * time.Second):
		t.Fatal("visible cold initialization did not complete")
	}
	for range 5 {
		waitLogHidden(t, hwnd)
		start := time.Now()
		if next := openDesktopLog(t, h, id, nil); next != hwnd {
			t.Fatal("reopen created a new window")
		}
		if time.Since(start) > time.Second {
			t.Fatal("warm open exceeded one second")
		}
	}
	anchor := logAnchorForWindow(hwnd)
	monitor, ok := monitorDetails(logMonitor(anchor))
	if !ok {
		t.Fatal("no target monitor")
	}
	want := fitLogWindow(monitor.work, monitor.dpi, windowPoint{1000, 700}, &windowPoint{monitor.work.Left + 80, monitor.work.Top + 100})
	uiDLL.NewProc("SetWindowPos").Call(hwnd, 0, uintptr(want.Left), uintptr(want.Top), uintptr(want.Right-want.Left), uintptr(want.Bottom-want.Top), 0x14)
	uiDLL.NewProc("ShowWindow").Call(hwnd, 6)
	if next := openDesktopLog(t, h, id, anchor); next != hwnd {
		t.Fatal("minimized window replaced")
	}
	if minimized, _, _ := uiDLL.NewProc("IsIconic").Call(hwnd); minimized != 0 {
		t.Fatal("still minimized")
	}
	var restored nativeRect
	uiDLL.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&restored)))
	if restored != want {
		t.Fatalf("lost restored bounds: want=%+v got=%+v", want, restored)
	}
}

func TestManagedLogInitializationFailureCanRebuild(t *testing.T) {
	lines := make(chan string, 32)
	previous := log.Writer()
	log.SetOutput(logTestOutput{writer: previous, lines: lines})
	defer log.SetOutput(previous)
	home := t.TempDir()
	blocker := filepath.Join(home, "command-log-profiles")
	if err := os.WriteFile(blocker, []byte("profile creation failure fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	h, status := desktopLogHost(t, home)
	id := model.ID()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = h.Open(ctx, id) // The error may race the native-show ACK; both must be prompt.
	cancel()
	hwnd := visibleLogWindow(t, h)
	wait := time.NewTimer(5 * time.Second)
	defer wait.Stop()
failed:
	for {
		select {
		case line := <-lines:
			if strings.Contains(line, "command_id="+id) && strings.Contains(line, "stage=initialization_failed") {
				break failed
			}
		case <-wait.C:
			t.Fatal("profile failure was not reported")
		}
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	// A failed surface is retired on Close; replacement never shares its profile.
	waitLogHidden(t, hwnd)
	next := openDesktopLog(t, h, id, nil)
	if next == hwnd {
		t.Fatal("failed surface reused")
	}
	select {
	case <-status:
	case <-time.After(16 * time.Second):
		t.Fatal("replacement did not initialize")
	}
}

func TestManagedLogFollowsRealMonitors(t *testing.T) {
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("requires interactive desktop")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous, _, _ := uiDLL.NewProc("SetThreadDpiAwarenessContext").Call(^uintptr(3))
	if previous != 0 {
		defer uiDLL.NewProc("SetThreadDpiAwarenessContext").Call(previous)
	}
	var monitors []uintptr
	callback := windows.NewCallback(func(monitor, _, _, _ uintptr) uintptr { monitors = append(monitors, monitor); return 1 })
	uiDLL.NewProc("EnumDisplayMonitors").Call(0, 0, callback, 0)
	if len(monitors) < 2 {
		t.Skip("requires two physical display work areas")
	}
	h, _ := desktopLogHost(t, t.TempDir())
	id := model.ID()
	var hwnd uintptr
	for _, index := range []int{1, 0, 1} {
		info, ok := monitorDetails(monitors[index])
		if !ok {
			t.Fatal("monitor details unavailable")
		}
		anchor := &hostipc.WindowAnchor{X: (info.work.Left + info.work.Right) / 2, Y: (info.work.Top + info.work.Bottom) / 2}
		next := openDesktopLog(t, h, id, anchor)
		if hwnd != 0 && hwnd != next {
			t.Fatal("moving across monitors recreated window")
		}
		hwnd = next
		actual, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(hwnd, 2)
		var rect nativeRect
		uiDLL.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&rect)))
		if actual != monitors[index] || rect.Left < info.work.Left || rect.Top < info.work.Top || rect.Right > info.work.Right || rect.Bottom > info.work.Bottom {
			t.Fatalf("window %+v outside target %+v", rect, info.work)
		}
		if got := logAnchorForWindow(hwnd); got == nil || *got != *anchor {
			t.Fatal("native input monitor capture disagrees", got, anchor)
		}
		t.Logf("target work area=%+v DPI=%d window=%+v", info.work, info.dpi, rect)
		waitLogHidden(t, hwnd)
	}
}
