//go:build windows

package quickinput

import (
	"os"
	"path/filepath"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

func (s *nativeSurface) logProfileDirectory() string {
	session := s.log.session
	if session == "" {
		session = s.epoch
	}
	return filepath.Join(s.home, "command-log-profiles", session, s.log.id, s.epoch)
}

// Run after native/COM disposal, before done closes. This is the retiring UI
// thread, never the host IPC loop. Keep profiles whose browser exit is unknown;
// the parent can remove the session after confirming the entire Job has exited.
func (s *nativeSurface) cleanupLogProfile() {
	if s.log == nil {
		return
	}
	if s.closeError == nil {
		if s.creationPending {
			s.closeError = i18n.Err("err.quickinput.create_unfinished")
		} else if s.environment != nil && !s.browserExited {
			s.closeError = i18n.Err("err.quickinput.browser_cleaning")
		}
	}
	err := s.closeError
	if err == nil {
		err = os.RemoveAll(s.logProfileDirectory())
	}
	if err != nil {
		s.logProgress("profile_cleanup_failed", logFailureCode(err, s.log.id))
	}
}
