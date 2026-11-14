//go:build windows

package quickinput

import (
	"context"
	"testing"

	"github.com/jackgan90/Asterhand/internal/notifications"
)

func TestNotificationSessionRequiresActiveUnlockedState(t *testing.T) {
	for _, test := range []struct {
		name string
		info notificationSessionInfo
		want bool
	}{
		{"unlocked", notificationSessionInfo{Level: 1, State: 0, SessionFlags: 1}, true},
		{"locked", notificationSessionInfo{Level: 1, State: 0, SessionFlags: 0}, false},
		{"disconnected", notificationSessionInfo{Level: 1, State: 4, SessionFlags: 1}, false},
		{"unknown lock", notificationSessionInfo{Level: 1, State: 0, SessionFlags: -1}, false},
		{"unknown structure", notificationSessionInfo{Level: 0, State: 0, SessionFlags: 1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.info.visible(); got != test.want {
				t.Fatalf("visible=%v, want %v", got, test.want)
			}
		})
	}
}

func TestNotificationStartupAndPowerResumeRespectLockedSession(t *testing.T) {
	visible := false
	peer := &hostPeer{ctx: context.Background(), out: make(chan hostDelivery, 32), failures: make(chan error, 1)}
	d := &notificationDesktop{peer: peer, sessionVisible: func() bool { return visible }}
	check := func(want bool) {
		t.Helper()
		var environment *notifications.Event
		for len(peer.out) > 0 {
			event := (<-peer.out).message.Notification
			if event.Kind == "environment" {
				environment = event
			}
		}
		if environment == nil || environment.Paused != want || d.paused != want {
			t.Fatalf("environment=%+v paused=%v, want %v", environment, d.paused, want)
		}
	}
	d.refreshEnvironment()
	check(true) // A host first started while locked must not begin countdown.
	d.suspend(true)
	check(true)
	d.suspend(false)
	check(true) // Power resume is not session unlock.
	visible = true
	d.refreshEnvironment()
	check(false)
	d.suspend(true)
	check(true)
	d.refreshEnvironment()
	check(true) // A session change cannot override suspended power state.
	d.suspend(false)
	check(false)
}

func TestNativeNotificationSessionQuery(t *testing.T) {
	requireNativeDesktop(t)
	if !notificationSessionVisible() {
		t.Fatal("test requires a connected, unlocked interactive session")
	}
}
