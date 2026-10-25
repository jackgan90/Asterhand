//go:build windows

package quickinput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type scriptCallback interface{ comCallback }

func init() { registerCallback[scriptCallback]("{49511172-cc67-4bca-9923-137112f4c4cc}") }

// ExecuteScript is test-only; the product bridge exposes no script execution.
func evaluate(t *testing.T, s *nativeSurface, script string) string {
	t.Helper()
	result := make(chan string, 1)
	err := s.dispatch(context.Background(), func() {
		s.resume()
		h := newCallback[scriptCallback](func(code uintptr, text *comRef) uintptr {
			if int32(code) < 0 {
				result <- fmt.Sprintf("HRESULT %x", code)
			} else {
				result <- windows.UTF16PtrToString((*uint16)(unsafe.Pointer(text)))
			}
			return 0
		})
		// WebView2 retains the callback until completion.
		defer h.Close()
		value, _ := windows.UTF16PtrFromString(script)
		if err := s.core.call(29, uintptr(unsafe.Pointer(value)), h.Ref()); err != nil {
			result <- err.Error()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-result:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("script completion timeout")
		return ""
	}
}

func TestNativeTextThemeSnapshotAndIsolation(t *testing.T) {
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("requires interactive desktop")
	}
	home := t.TempDir()
	draft := &Draft{}
	s, err := startNativeSurface(home, nil, func(err error) { t.Errorf("surface failed: %v", err) }, draft)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	secret := "QIN-PRIVATE-TEST-中文😀\n second line"
	encoded, _ := json.Marshal(secret)
	evaluate(t, s, `document.querySelector('#input').value = `+string(encoded)+`; document.querySelector('#input').dispatchEvent(new Event('input')); true`)
	if err = s.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	if text, _ := draft.Read(); text != secret {
		t.Fatal("draft mismatch")
	}
	evaluate(t, s, `document.querySelector('#input').setSelectionRange(2,5); true`)
	if err = s.Update("frost"); err != nil {
		t.Fatal(err)
	}
	if value := evaluate(t, s, `[document.documentElement.dataset.theme,document.querySelector('#input').selectionStart,document.querySelector('#input').selectionEnd]`); value != `["frost",2,5]` {
		t.Fatal("theme replaced selection", value)
	}
	// A real composing editor must refuse a destructive snapshot until committed.
	evaluate(t, s, `document.querySelector('#input').dispatchEvent(new CompositionEvent('compositionstart')); true`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	if err = s.Snapshot(ctx); err == nil {
		t.Fatal("composition snapshot unexpectedly acknowledged")
	}
	cancel()
	evaluate(t, s, `document.querySelector('#input').dispatchEvent(new CompositionEvent('compositionend')); true`)
	if err = s.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Wrong generation and extra fields are ignored even from the trusted page.
	evaluate(t, s, `window.chrome.webview.postMessage({version:1,kind:'draft_updated',generation:'wrong',text:'attack',revision:999}); true`)
	if text, _ := draft.Read(); text != secret {
		t.Fatal("wrong-generation message changed draft")
	}
	evaluate(t, s, `location.href='https://example.invalid/'; true`)
	if value := evaluate(t, s, `location.href`); value != strconv.Quote(surfaceURL) {
		t.Fatal("external navigation allowed", value)
	}
	evaluate(t, s, `window.__network='pending';fetch('https://example.invalid/').then(()=>window.__network='allowed').catch(()=>window.__network='blocked');true`)
	if value := evaluate(t, s, `window.__network`); value != `"blocked"` {
		t.Fatal("network was not blocked", value)
	}
	if value := evaluate(t, s, `[localStorage.length,sessionStorage.length]`); value != `[0,0]` {
		t.Fatal("persistent web storage used", value)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// Inspect only this isolated profile, after the browser releases its files.
	err = filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("QIN-PRIVATE-TEST")) || bytes.Contains(data, []byte("Q\x00I\x00N\x00-\x00P\x00R\x00I\x00V\x00A\x00T\x00E\x00-\x00T\x00E\x00S\x00T\x00")) {
			return fmt.Errorf("draft persisted in %s", filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBridgeRejectsMalformedMessages(t *testing.T) {
	s := &nativeSurface{draft: &Draft{}, epoch: "expected", documentReady: true, waiters: map[string]chan struct{}{}}
	for _, raw := range []string{`{}`, `{"version":2,"kind":"draft_updated"}`, `{"version":1,"kind":"draft_updated","generation":"wrong","text":"x","revision":1}`, `{"version":1,"kind":"draft_updated","generation":"expected","text":"x","revision":1,"unknown":true}`, `{"version":1} {}`} {
		s.bridgeMessage(raw)
		if text, _ := s.draft.Read(); text != "" {
			t.Fatal("malformed message accepted")
		}
	}
}

func TestNativeCloseHidesBeforeBrowserCleanup(t *testing.T) {
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("requires interactive desktop")
	}
	s, err := startNativeSurface(t.TempDir(), nil, func(err error) { t.Errorf("surface: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	visible := make(chan uintptr, 1)
	err = s.dispatch(context.Background(), func() {
		// Show only outside the work area without activating this test window.
		offscreen := int32(-32000)
		uiDLL.NewProc("SetWindowPos").Call(s.hwnd.Load(), 0, uintptr(offscreen), uintptr(offscreen), 680, 240, 0x14)
		uiDLL.NewProc("ShowWindow").Call(s.hwnd.Load(), 4)
		s.beginClose()
		value, _, _ := uiDLL.NewProc("IsWindowVisible").Call(s.hwnd.Load())
		visible <- value
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-visible:
		if value != 0 {
			t.Fatal("closing surface remained visible while the browser was cleaning up")
		}
	case <-time.After(time.Second):
		t.Fatal("no closing state")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}
