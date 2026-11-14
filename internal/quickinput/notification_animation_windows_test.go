//go:build windows

package quickinput

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/notifications"
)

func TestNotificationDelayedRevealWaitsForCurrentMeasurement(t *testing.T) {
	// A previous frame scheduled timer 72; a newer frame now awaits measurement.
	n := &notificationSurface{card: notifications.Card{ID: 2, Phase: "visible"},
		layout: 10, waiting: true, revealAfter: time.Now().Add(-time.Second)}
	s := &nativeSurface{notification: n}
	s.revealNotification()
	if n.shown {
		t.Fatal("old display timer bypassed the current frame's measurement")
	}
}

func TestNotificationExitingCardKeepsExitDuringReposition(t *testing.T) {
	directory := os.Getenv("ASTERHAND_INPUT_HOST_TEST_DIRECTORY")
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" || directory == "" {
		t.Skip("requires built notification host and Windows desktop")
	}
	h := newNotificationHost(t.TempDir(), func() (hostProcess, error) {
		return launchWindowsHostKind(directory, "--notification-host")
	})
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	frame := notifications.Snapshot{Revision: 1, Theme: "star", Locale: "en-US"}
	apply := func() {
		t.Helper()
		frame.Revision++
		if err := h.Apply(ctx, frame); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	waitNotificationEvent(t, ctx, h, "environment")
	for i, id := range []string{"execution1234567890", "execution1234567891", "execution1234567892"} {
		frame.Cards = append(frame.Cards, notifications.Card{ID: uint64(i + 1), Phase: "entering", Completion: notifications.Completion{
			CommandID: "command1234567890", RunID: id, Name: "overlapping-exit", Result: "succeeded", Started: time.Now(), Ended: time.Now()}})
	}
	apply()
	entered := map[uint64]bool{}
	for len(entered) < 3 {
		entered[waitNotificationEvent(t, ctx, h, "entered").ID] = true
	}
	for i := range frame.Cards {
		frame.Cards[i].Phase = "visible"
	}
	apply()
	frame.Cards[1].Phase = "exiting"
	apply()
	time.Sleep(60 * time.Millisecond)
	frame.Cards[0].Phase = "exiting"
	apply()
	if event := waitNotificationEvent(t, ctx, h, "exited"); event.ID != 2 {
		t.Fatalf("expected middle card first, got %+v", event)
	}
	frame.Cards = []notifications.Card{frame.Cards[0], frame.Cards[2]}
	apply()
	timer := time.NewTimer(1500 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case event := <-h.Events():
			if event.Kind == "exited" && event.ID == 1 {
				return
			}
			if event.Kind == "disconnected" {
				t.Fatal("host disconnected")
			}
		case <-timer.C:
			t.Fatal("reposition lost exit acknowledgement and left the slot occupied")
		}
	}
}
