//go:build windows

package quickinput

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

func TestNativeDispatchBeforeWindowCreation(t *testing.T) {
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("requires native desktop message queue")
	}
	runtime.LockOSThread()
	s := newWindowSurface(t.TempDir(), nil, nil, nil, nil)
	defer s.dispose()
	_ = s.Update("frost")
	_ = s.UpdateLocale("en-US")
	completed := make(chan struct{}, 12)
	for range 2 {
		if err := s.dispatch(context.Background(), func() { completed <- struct{}{} }); err != nil {
			t.Fatal(err)
		}
	}
	concurrent := make(chan error, 1)
	go func() {
		for range 10 {
			if err := s.dispatch(context.Background(), func() { completed <- struct{}{} }); err != nil {
				concurrent <- err
				return
			}
		}
		concurrent <- nil
	}()
	if err := s.createWindow(); err != nil {
		t.Fatal(err)
	}
	if err := <-concurrent; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(completed) < 12 && time.Now().Before(deadline) {
		var m nativeMessage
		for {
			has, _, _ := uiDLL.NewProc("PeekMessageW").Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, 1)
			if has == 0 {
				break
			}
			uiDLL.NewProc("TranslateMessage").Call(uintptr(unsafe.Pointer(&m)))
			uiDLL.NewProc("DispatchMessageW").Call(uintptr(unsafe.Pointer(&m)))
		}
		time.Sleep(time.Millisecond)
	}
	if len(completed) != 12 || s.theme != "frost" || s.locale != "en-US" {
		t.Fatalf("stranded creation queue: completed=%d pending=%d theme=%s locale=%s", len(completed), len(s.commands), s.theme, s.locale)
	}
}
