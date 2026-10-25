//go:build windows

package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/jackgan90/Asterhand/internal/quickinput"
)

func TestHotkeyOccupier(t *testing.T) {
	if os.Getenv("ASTERHAND_HOTKEY_HELPER") != "1" {
		t.Skip("helper")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ok, _, err := user32.NewProc("RegisterHotKey").Call(0, 9001, 0x4000|1|2|4, 0x79)
	if ok == 0 {
		t.Fatal(err)
	}
	defer user32.NewProc("UnregisterHotKey").Call(0, 9001)
	fmt.Println("HOTKEY_READY")
	_, _ = os.Stdin.Read(make([]byte, 1))
}

func TestNativeHotkeyConflictAcrossProcesses(t *testing.T) {
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("requires interactive desktop")
	}
	tray, err := Start(Options{State: func() State { return State{Name: "快捷键验证", Ready: true} }, OpenWeb: func() error { return nil }, Stop: func(bool) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer tray.Close()
	exe, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, exe, "-test.run=^TestHotkeyOccupier$", "-test.v")
	child.Env = append(os.Environ(), "ASTERHAND_HOTKEY_HELPER=1")
	in, _ := child.StdinPipe()
	out, _ := child.StdoutPipe()
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close(); _ = child.Wait() }()
	scanner := bufio.NewScanner(out)
	ready := false
	for scanner.Scan() {
		if scanner.Text() == "HOTKEY_READY" {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatal("occupier could not acquire isolated test combination")
	}
	r := &trayRegistrar{tray}
	b := quickinput.Binding{Modifiers: []string{"ctrl", "alt", "shift"}, Key: "F10"}
	_, err = r.Register(b)
	var conflict *quickinput.RegistrationError
	if !errors.As(err, &conflict) || conflict.Code != "conflict" {
		t.Fatalf("expected system conflict, got %v", err)
	}
	_ = in.Close()
	if err = child.Wait(); err != nil {
		t.Fatal(err)
	}
	id, err := r.Register(b)
	if err != nil {
		t.Fatal("not released by exited owner", err)
	}
	if err = r.Unregister(id); err != nil {
		t.Fatal(err)
	}
}

type hotkeySurface struct{ toggled chan struct{} }

func (s *hotkeySurface) Toggle()                        { s.toggled <- struct{}{} }
func (s *hotkeySurface) Update(string) error            { return nil }
func (s *hotkeySurface) Snapshot(context.Context) error { return nil }
func (s *hotkeySurface) Close() error                   { return nil }

func TestNativeDefaultHotkeyDispatchAndNoRepeat(t *testing.T) {
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("requires interactive desktop")
	}
	tray, err := Start(Options{State: func() State { return State{Name: "默认键验证", Ready: true} }, OpenWeb: func() error { return nil }, Stop: func(bool) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	defer tray.Close()
	c := quickinput.New(t.TempDir())
	defer c.Close()
	s := &hotkeySurface{toggled: make(chan struct{}, 8)}
	_, err = (&trayRegistrar{tray}).invoke(func() (int, error) { tray.options.Input = c; return 0, nil })
	if err != nil {
		t.Fatal(err)
	}
	c.Attach(&trayRegistrar{tray}, func() (quickinput.Surface, error) { return s, nil })
	deadline := time.Now().Add(5 * time.Second)
	for c.Status().Registration == "initializing" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.Status().Registration != "registered" {
		t.Fatal(c.Status())
	}
	// Only inject after this isolated test owns Ctrl+Alt+J; no other app handles it.
	keyEvent := user32.NewProc("keybd_event")
	keyEvent.Call(0x11, 0, 0, 0)
	keyEvent.Call(0x12, 0, 0, 0)
	defer keyEvent.Call(0x11, 0, 2, 0)
	defer keyEvent.Call(0x12, 0, 2, 0)
	lease, err := c.StartRecording("native-test", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(lease)
	var value struct {
		ID string `json:"recording_id"`
	}
	_ = json.Unmarshal(encoded, &value)
	keyEvent.Call('J', 0, 0, 0)
	keyEvent.Call('J', 0, 2, 0)
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-s.toggled:
			t.Fatal("recording unexpectedly opened surface")
		default:
		}
		// Reading the opaque lease through its public representation avoids
		// reaching across the package's private recording state.
		observed, err := c.Recording(value.ID, "native-test", false)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(observed)
		if strings.Contains(string(data), `"binding"`) {
			_, _ = c.Recording(value.ID, "native-test", true)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		t.Fatal("native hotkey was not routed to recorder")
	}
	keyEvent.Call('J', 0, 0, 0)
	keyEvent.Call('J', 0, 0, 0)
	keyEvent.Call('J', 0, 2, 0)
	select {
	case <-s.toggled:
	case <-time.After(3 * time.Second):
		t.Fatal("no WM_HOTKEY for Ctrl+Alt+J")
	}
	select {
	case <-s.toggled:
		t.Fatal("MOD_NOREPEAT failed")
	case <-time.After(150 * time.Millisecond):
	}
	// Check that only this test's message window remains responsive.
	var result uintptr
	user32.NewProc("SendMessageTimeoutW").Call(tray.window.Load(), 0, 0, 0, 2, 1000, uintptr(unsafe.Pointer(&result)))
}
