//go:build windows

package quickinput

import (
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/notifications"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func TestNativeNotificationRendering(t *testing.T) {
	requireNativeDesktop(t)
	commands, input := io.Pipe()
	output, events := io.Pipe()
	p := &pipeHost{input, output, make(chan struct{}), sync.Once{}}
	home := t.TempDir()
	stopped := make(chan error, 1)
	go func() {
		defer commands.Close()
		defer events.Close()
		initial, err := hostipc.Read(commands, false)
		if err == nil {
			err = serveNotificationHost(commands, events, initial)
		}
		stopped <- err
	}()
	h := newNotificationHost(home, func() (hostProcess, error) { return p, nil })
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	before, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
	frame := notifications.Snapshot{Revision: 1, Theme: "star", Locale: "zh-CN"}
	if err := h.Apply(ctx, frame); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case e := <-h.Events():
			if e.Kind == "environment" {
				goto ready
			}
		case <-ctx.Done():
			t.Fatal("environment", ctx.Err())
		}
	}
ready:
	for i := range 3 {
		frame.Cards = append(frame.Cards, notifications.Card{ID: uint64(i + 1), Phase: "entering", Completion: notifications.Completion{
			CommandID: "command1234567890", RunID: []string{"execution1234567890", "execution1234567891", "execution1234567892"}[i],
			Name: []string{"build-project", "long-command-name-for-a-multiline-completion-notification-test", "generate-report"}[i], Result: "succeeded", Elapsed: float64(i+1) * 1.25, Started: time.Now(), Ended: time.Now()}})
	}
	frame.Cards[2].ErrorKey = "err.localcmd.run_changed"
	frame.Revision++
	if err := h.Apply(ctx, frame); err != nil {
		t.Fatal(err)
	}
	entered := map[uint64]bool{}
	for len(entered) < 3 {
		select {
		case e := <-h.Events():
			if e.Kind == "entered" {
				entered[e.ID] = true
			}
			if e.Kind == "disconnected" {
				t.Fatal("host disconnected")
			}
		case <-ctx.Done():
			t.Fatal("cards never entered", ctx.Err(), entered)
		}
	}
	after, _, _ := uiDLL.NewProc("GetForegroundWindow").Call()
	if before != after {
		t.Fatal("notification stole foreground", before, after)
	}
	heights := nativeNotificationHeights(t, ctx, home)
	if heights[1] >= heights[2] || heights[1] >= heights[3] {
		t.Fatal("card heights did not follow title/error content", heights)
	}
	for i := range frame.Cards {
		frame.Cards[i].Phase = "visible"
	}
	frame.Revision++
	frame.Theme = "frost"
	frame.Locale = "en-US"
	if err := h.Apply(ctx, frame); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("ASTERHAND_NOTIFICATION_PREVIEW") == "1" {
		// Preview tooling lists app windows only; this opt-in changes discovery,
		// after the production NOACTIVATE foreground assertion above has passed.
		uiHosts.Range(func(_, value any) bool {
			s := value.(*nativeSurface)
			if s.notification != nil {
				_ = s.dispatch(ctx, func() {
					uiDLL.NewProc("SetWindowLongPtrW").Call(s.hwnd.Load(), ^uintptr(19), 0x08000000|0x00040000|0x00080000)
				})
			}
			return true
		})
		time.Sleep(25 * time.Second)
		frame.Revision++
		frame.Theme = "star"
		frame.Locale = "zh-CN"
		if err := h.Apply(ctx, frame); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Second)
	}
	for i := range frame.Cards {
		frame.Cards[i].Phase = "exiting"
	}
	frame.Revision++
	if err := h.Apply(ctx, frame); err != nil {
		t.Fatal(err)
	}
	exited := map[uint64]bool{}
	for len(exited) < 3 {
		select {
		case e := <-h.Events():
			if e.Kind == "exited" {
				exited[e.ID] = true
			}
			if e.Kind == "disconnected" {
				t.Fatal("host disconnected")
			}
		case <-ctx.Done():
			t.Fatal("cards never exited", ctx.Err(), exited)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second): // Native close allows up to 12 s for WebView2 profile release.
		t.Fatal("host did not stop")
	}
}

func TestNotificationPlacementFitsWorkArea(t *testing.T) {
	for _, dpi := range []uint32{96, 144, 192} {
		work := windowBounds{Left: -2560, Top: -200, Right: 0, Bottom: 1240}
		cards := []notifications.Card{{ID: 1}, {ID: 2}, {ID: 3}}
		bounds := notificationStackBounds(work, dpi, cards, 3, map[uint64]int32{1: 101, 2: 120, 3: 135})
		for i, r := range bounds {
			if r.Left < work.Left || r.Top < work.Top || r.Right > work.Right || r.Bottom > work.Bottom {
				t.Fatal(dpi, i, r)
			}
			if i > 0 {
				gap := r.Top - bounds[i-1].Bottom
				if gap < int32(10*dpi/96)-1 || gap > int32(10*dpi/96)+1 {
					t.Fatal("stack gap does not follow measured heights", dpi, bounds)
				}
			}
		}
	}
}

func nativeNotificationHeights(t *testing.T, ctx context.Context, home string) map[uint64]int32 {
	t.Helper()
	type measurement struct {
		id     uint64
		height int32
	}
	results := make(chan measurement, 3)
	uiHosts.Range(func(_, value any) bool {
		s := value.(*nativeSurface)
		if s.notification != nil && s.home == home {
			if err := s.dispatch(ctx, func() {
				n := s.notification
				results <- measurement{n.card.ID, n.target.Bottom - n.target.Top}
			}); err != nil {
				t.Fatal(err)
			}
		}
		return true
	})
	heights := map[uint64]int32{}
	for len(heights) < 3 {
		select {
		case m := <-results:
			heights[m.id] = m.height
		case <-ctx.Done():
			t.Fatal("missing native height measurement", ctx.Err())
		}
	}
	return heights
}
