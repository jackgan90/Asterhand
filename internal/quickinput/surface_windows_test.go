//go:build windows

package quickinput

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestNativeSurfacePrivateProfile(t *testing.T) {
	requireNativeDesktop(t)
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("requires an explicitly enabled Windows interactive desktop")
	}
	loaded := make(chan struct{}, 1)
	s, err := startNativeSurface(t.TempDir(), func(raw string) {
		var message struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal([]byte(raw), &message) == nil && message.Kind == "ready" {
			select {
			case loaded <- struct{}{}:
			default:
			}
		}
	}, func(err error) { t.Errorf("renderer: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case <-loaded:
		t.Logf("private surface loaded, runtime=%s", s.version)
	case <-time.After(12 * time.Second):
		t.Fatal("no trusted ready message from private surface")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func requireNativeDesktop(t *testing.T) {
	t.Helper()
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" {
		t.Skip("explicit desktop required")
	}
	if err := checkInteractiveDesktop(); err != nil {
		t.Skip("direct STA test requires a non-elevated test runner; production restricted host tested separately")
	}
}
