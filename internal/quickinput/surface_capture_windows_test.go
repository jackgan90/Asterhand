//go:build windows

package quickinput

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/pkg/combridge"
	"golang.org/x/sys/windows"
)

type captureCallback interface{ comCallback }

func init() {
	combridge.RegisterVTable[combridge.IUnknown, captureCallback]("{697e05e9-3d8f-45fa-96f4-8ffe1ededaf5}", func(this, code uintptr) uintptr { return combridge.Resolve[captureCallback](this).Invoke(code, nil) })
}

func TestNativeSurfaceVisualCapture(t *testing.T) {
	directory := os.Getenv("ASTERHAND_SURFACE_CAPTURE")
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" || directory == "" {
		t.Skip("explicit visual output required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := startNativeSurface(t.TempDir(), nil, func(err error) { t.Errorf("surface: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// A visible WebView is required for a real compositor capture. This window
	// belongs to this isolated test; it never loads another application's page.
	s.Toggle()
	for _, theme := range []string{"star", "frost"} {
		if err = s.Update(theme); err != nil {
			t.Fatal(err)
		}
		evaluate(t, s, `document.querySelector('#input').value='在灵感出现的瞬间，记下来。\n下一步，从这里开始。';document.querySelector('#input').dispatchEvent(new Event('input'));true`)
		// Wait on two animation frames in the real compositor, not a fixed sleep.
		evaluate(t, s, `window.__painted=false;requestAnimationFrame(()=>requestAnimationFrame(()=>window.__painted=true));true`)
		deadline := time.Now().Add(3 * time.Second)
		for evaluate(t, s, `window.__painted`) != `true` {
			if time.Now().After(deadline) {
				t.Fatal("no compositor frame")
			}
		}
		completed := make(chan error, 1)
		path, _ := windows.UTF16PtrFromString(filepath.Join(directory, "quick-input-"+theme+".png"))
		err = s.dispatch(context.Background(), func() {
			var stream *comRef
			hr, _, _ := windows.NewLazySystemDLL("shlwapi.dll").NewProc("SHCreateStreamOnFileEx").Call(uintptr(unsafe.Pointer(path)), 0x1012, 0, 1, 0, uintptr(unsafe.Pointer(&stream)))
			if int32(hr) < 0 {
				completed <- fmt.Errorf("capture stream HRESULT %x", hr)
				return
			}
			h := newCallback[captureCallback](func(code uintptr, _ *comRef) uintptr {
				stream.release()
				if int32(code) < 0 {
					completed <- fmt.Errorf("capture HRESULT %x", code)
				} else {
					completed <- nil
				}
				return 0
			})
			defer h.Close()
			if err := s.core.call(30, 0, uintptr(unsafe.Pointer(stream)), h.Ref()); err != nil {
				stream.release()
				completed <- err
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		select {
		case err = <-completed:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("capture timed out")
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}
