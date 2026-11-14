//go:build windows

package quickinput

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/notifications"
)

func TestNotificationHostIndependentProcess(t *testing.T) {
	directory := os.Getenv("ASTERHAND_INPUT_HOST_TEST_DIRECTORY")
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" || directory == "" {
		t.Skip("requires built notification host and Windows desktop")
	}
	launches := 0
	h := newNotificationHost(t.TempDir(), func() (hostProcess, error) {
		launches++
		return launchWindowsHostKind(directory, "--notification-host")
	})
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	frame := notifications.Snapshot{Revision: 1, Theme: "star", Locale: "en-US"}
	if err := h.Apply(ctx, frame); err != nil {
		t.Fatal(err)
	}
	waitNotificationEvent(t, ctx, h, "environment")
	frame.Cards = []notifications.Card{{ID: 1, Phase: "entering", Completion: notifications.Completion{CommandID: "command1234567890", RunID: "execution1234567890", Name: "host-check", Result: "succeeded", Started: time.Now(), Ended: time.Now()}}}
	frame.Revision++
	if err := h.Apply(ctx, frame); err != nil {
		t.Fatal(err)
	}
	waitNotificationEvent(t, ctx, h, "entered")
	frame.Cards[0].Phase = "visible"
	frame.Revision++
	if err := h.Apply(ctx, frame); err != nil {
		t.Fatal(err)
	}
	// Kill only the isolated test host. Retrying the same ID must not replay entry.
	h.mu.Lock()
	p := h.process
	h.mu.Unlock()
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	waitNotificationEvent(t, ctx, h, "disconnected")
	frame.Revision++
	frame.Theme = "frost"
	if err := h.Apply(ctx, frame); err != nil {
		t.Fatal(err)
	}
	waitNotificationEvent(t, ctx, h, "environment")
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case event := <-h.Events():
		if event.Kind == "entered" || event.Kind == "disconnected" {
			t.Fatal("bad restoration", event)
		}
	case <-timer.C:
	}
	frame.Cards[0].Phase = "exiting"
	frame.Revision++
	if err := h.Apply(ctx, frame); err != nil {
		t.Fatal(err)
	}
	waitNotificationEvent(t, ctx, h, "exited")
	if launches != 2 {
		t.Fatal("unexpected process count", launches)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func waitNotificationEvent(t *testing.T, ctx context.Context, h *notificationHost, kind string) notifications.Event {
	t.Helper()
	for {
		select {
		case event := <-h.Events():
			if event.Kind == kind {
				return event
			}
			if event.Kind == "disconnected" {
				t.Fatal("unexpected host loss while waiting for", kind)
			}
		case <-ctx.Done():
			t.Fatal("missing notification event", kind, ctx.Err())
		}
	}
}
