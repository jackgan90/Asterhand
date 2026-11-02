//go:build windows

package quickinput

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func TestLogProfileCleanupIsScopedAndRequiresConfirmedExit(t *testing.T) {
	home, session, id := t.TempDir(), model.ID(), model.ID()
	for _, state := range []string{"released", "creation_pending", "exit_unobserved"} {
		s := newWindowSurface(home, nil, nil, nil, &logSurface{id: id, session: session})
		profile := s.logProfileDirectory()
		if err := os.MkdirAll(profile, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profile, "cache"), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		sibling := filepath.Join(filepath.Dir(profile), model.ID())
		if err := os.MkdirAll(sibling, 0700); err != nil {
			t.Fatal(err)
		}
		progress := make(chan hostipc.Message, 1)
		s.log.send = func(m hostipc.Message) error { progress <- m; return nil }
		if state == "creation_pending" {
			s.creationPending = true
		}
		if state != "creation_pending" {
			// EnvironmentCompleted retains this owner before query/subscribe can
			// fail. Absence of the exit subscription is not exit confirmation.
			s.environment = &comRef{}
			s.browserExited = state == "released"
		}
		s.cleanupLogProfile()
		_, err := os.Stat(profile)
		if state != "released" {
			if err != nil || s.closeError == nil {
				t.Fatal("unconfirmed browser profile removed", err)
			}
			select {
			case m := <-progress:
				if m.LogEvent.Stage != "profile_cleanup_failed" || !m.LogEvent.Valid() {
					t.Fatal(m)
				}
			default:
				t.Fatal("no cleanup diagnostic")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal("released profile retained", err)
		}
		if _, err := os.Stat(sibling); err != nil {
			t.Fatal("sibling generation removed", err)
		}
	}
}
