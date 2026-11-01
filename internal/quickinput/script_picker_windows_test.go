//go:build windows

package quickinput

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestNativeScriptPicker(t *testing.T) {
	if os.Getenv("ASTERHAND_SCRIPT_PICKER_TEST") != "1" {
		t.Skip("explicit native script picker gate required")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	for _, ext := range []string{".bat", ".cmd", ".ps1"} {
		if err := os.WriteFile(filepath.Join(directory, "中文 script"+ext), []byte("echo acceptance"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ASTERHAND_PICKER_PRIVATE", "must-not-inherit")
	for _, selection := range []string{".bat", ".cmd", ".ps1", "cancel", "hold"} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if selection == "hold" {
			time.AfterFunc(time.Second, cancel)
		}
		path, err := pickScriptProcess(ctx, executable, "-test.run=^TestScriptPickerProcessHelper$", "-test.bench=script-picker-helper-"+selection, "-test.outputdir="+directory)
		cancel()
		if selection == "hold" {
			if err == nil {
				t.Fatal("cancelled picker returned success")
			}
			continue
		}
		want := ""
		if selection != "cancel" {
			want = filepath.Join(directory, "中文 script"+selection)
		}
		if err != nil || path != want {
			trace, _ := os.ReadFile(filepath.Join(directory, "picker-trace.txt"))
			t.Log(string(trace))
			t.Fatalf("%s: path %q, err %v", selection, path, err)
		}
		t.Log(selection, "native selection/cancellation passed")
	}
}

func TestScriptPickerProcessHelper(t *testing.T) {
	mode := flag.Lookup("test.bench").Value.String()
	if !strings.HasPrefix(mode, "script-picker-helper-") {
		t.Skip("picker subprocess only")
	}
	if err := verifyHostToken(windows.GetCurrentProcessToken()); err != nil || os.Getenv("ASTERHAND_PICKER_PRIVATE") != "" {
		fmt.Fprintln(os.Stderr, "invalid picker identity/environment", err)
		os.Exit(2)
	}
	selection := strings.TrimPrefix(mode, "script-picker-helper-")
	trace := func(stage string) {
		file, err := os.OpenFile(filepath.Join(flag.Lookup("test.outputdir").Value.String(), "picker-trace.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err == nil {
			fmt.Fprintln(file, time.Now().Format(time.RFC3339Nano), stage)
			file.Close()
		}
	}
	trace("helper started")
	uiDLL.NewProc("SetProcessDpiAwarenessContext").Call(^uintptr(3))
	targetMonitor := foregroundMonitor()
	path, err := showScriptPicker(func(hwnd uintptr) {
		trace(fmt.Sprintf("dialog ready %d", hwnd))
		go func() {
			// INITDONE precedes showing the dialog. Wait for the actual UI, not
			// a fixed sleep, before entering the filename and confirming it.
			deadline := time.Now().Add(5 * time.Second)
			for {
				visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(hwnd)
				if visible != 0 {
					break
				}
				if time.Now().After(deadline) {
					trace("dialog did not become visible")
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := checkScriptPickerPosition(hwnd, targetMonitor); err != nil {
				trace(err.Error())
				fmt.Fprintln(os.Stderr, err)
				os.Exit(4)
			}
			if selection == "hold" {
				return
			}
			if selection == "cancel" {
				uiDLL.NewProc("PostMessageW").Call(hwnd, 0x111, 2, 0)
				return
			}
			name, _ := windows.UTF16PtrFromString(filepath.Join(flag.Lookup("test.outputdir").Value.String(), "中文 script"+selection))
			uiDLL.NewProc("SendMessageW").Call(hwnd, 0x468, 0x480, uintptr(unsafe.Pointer(name))) // CDM_SETCONTROLTEXT, edt1
			trace("filename set")
			uiDLL.NewProc("PostMessageW").Call(hwnd, 0x111, 1, 0) // IDOK
		}()
	})
	trace(fmt.Sprintf("dialog returned %q %v", path, err))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Path string `json:"path"`
	}{path})
	os.Exit(0)
}

func checkScriptPickerPosition(hwnd, targetMonitor uintptr) error {
	actualMonitor, _, _ := uiDLL.NewProc("MonitorFromWindow").Call(hwnd, 2)
	monitor, ok := monitorDetails(targetMonitor)
	var bounds nativeRect
	measured, _, _ := uiDLL.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&bounds)))
	if !ok || measured == 0 || actualMonitor != targetMonitor {
		return fmt.Errorf("picker monitor mismatch: got %d, want %d", actualMonitor, targetMonitor)
	}
	// Compare opposing margins of the visible native window, including the
	// taskbar's excluded area. Integer coordinates may differ by one pixel.
	horizontal := (bounds.Left - monitor.work.Left) - (monitor.work.Right - bounds.Right)
	vertical := (bounds.Top - monitor.work.Top) - (monitor.work.Bottom - bounds.Bottom)
	if horizontal < -1 || horizontal > 1 || vertical < -1 || vertical > 1 {
		return fmt.Errorf("picker is not centered: window %+v, work area %+v", bounds, monitor.work)
	}
	return nil
}
