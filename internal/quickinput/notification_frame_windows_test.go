//go:build windows

package quickinput

import (
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Check both the page's composited pixels and the native window's hit area.
// A rounded foreground alone can hide an opaque rectangular page background.
func checkNotificationCorners(t *testing.T, home, theme string) {
	t.Helper()
	directory := os.Getenv("ASTERHAND_NOTIFICATION_CAPTURE")
	if directory == "" {
		directory = t.TempDir()
	} else if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	var surfaces []*nativeSurface
	uiHosts.Range(func(_, value any) bool {
		s := value.(*nativeSurface)
		if s.notification != nil && s.home == home {
			surfaces = append(surfaces, s)
		}
		return true
	})
	for _, s := range surfaces {
		waitChatScript(t, s, `document.documentElement.dataset.theme==='`+theme+`'`)
		for deadline := time.Now().Add(3 * time.Second); ; {
			settled := false
			surfaceSync(t, s, func() { settled = s.notification.current == s.notification.target && !s.notification.waiting })
			if settled && evaluate(t, s, `Math.abs(innerHeight-Math.ceil(document.querySelector('main').getBoundingClientRect().height))<=1`) == "true" {
				break
			}
			if time.Now().After(deadline) {
				t.Log("page layout", evaluate(t, s, `({width:innerWidth,height:innerHeight,scale:devicePixelRatio,card:document.querySelector('main').getBoundingClientRect().toJSON()})`))
				surfaceSync(t, s, func() {
					t.Logf("native layout: dpi=%d current=%+v target=%+v", s.notification.dpi, s.notification.current, s.notification.target)
				})
				t.Fatal("notification layout did not settle before capture")
			}
			time.Sleep(10 * time.Millisecond)
		}
		filename := filepath.Join(directory, fmt.Sprintf("notification-%s-%d.png", theme, s.notification.slot))
		captureExperience(t, s, filename)
		checkNotificationImageCorners(t, filename)
		surfaceSync(t, s, func() {
			var bounds nativeRect
			hwnd := s.hwnd.Load()
			uiDLL.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&bounds)))
			region, _, _ := notificationGDI.NewProc("CreateRectRgn").Call(0, 0, 0, 0)
			if region == 0 {
				t.Error("could not allocate test region")
				return
			}
			defer notificationGDI.NewProc("DeleteObject").Call(region)
			if ok, _, _ := uiDLL.NewProc("GetWindowRgn").Call(hwnd, region); ok == 0 {
				t.Error("notification has no native clipping region")
				return
			}
			w, h := uintptr(bounds.Right-bounds.Left), uintptr(bounds.Bottom-bounds.Top)
			for _, p := range [][2]uintptr{{0, 0}, {w - 1, 0}, {0, h - 1}, {w - 1, h - 1}} {
				if inside, _, _ := notificationGDI.NewProc("PtInRegion").Call(region, p[0], p[1]); inside != 0 {
					t.Errorf("native corner %v still intercepts the desktop", p)
				}
			}
			if inside, _, _ := notificationGDI.NewProc("PtInRegion").Call(region, w/2, h/2); inside == 0 {
				t.Error("notification content missing from native region")
			}
		})
	}
	if len(surfaces) != 3 {
		t.Fatalf("expected three notification windows, got %d", len(surfaces))
	}
}

func checkNotificationImageCorners(t *testing.T, filename string) {
	t.Helper()
	file, err := os.Open(filename)
	// CapturePreview can retain its exclusive stream briefly after completion.
	for deadline := time.Now().Add(time.Second); errors.Is(err, windows.ERROR_SHARING_VIOLATION) && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
		file, err = os.Open(filename)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	frame, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	w, h := frame.Bounds().Dx(), frame.Bounds().Dy()
	for _, p := range [][2]int{{0, 0}, {w - 1, 0}, {0, h - 1}, {w - 1, h - 1}} {
		if _, _, _, alpha := frame.At(p[0], p[1]).RGBA(); alpha != 0 {
			t.Errorf("%s: page corner %v has opaque backing (alpha=%d)", filepath.Base(filename), p, alpha)
		}
	}
	if _, _, _, alpha := frame.At(w/2, h/2).RGBA(); alpha != 65535 {
		t.Errorf("%s: card content lost its opaque theme background", filepath.Base(filename))
	}
}
