//go:build windows

package quickinput

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// This launches the production GUI executable with its original tool-window
// style and real restricted token, but never replaces the installed node.
func TestIsolatedHostLifecycleAndDraftRecovery(t *testing.T) {
	directory := os.Getenv("ASTERHAND_INPUT_HOST_TEST_DIRECTORY")
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" || directory == "" {
		t.Skip("requires built GUI host and explicit desktop validation")
	}
	home := t.TempDir()
	draft := &Draft{}
	if err := draft.Update("主进程恢复 abc 中文 😀", 1); err != nil {
		t.Fatal(err)
	}
	launch := func() (hostProcess, error) { return launchWindowsHostFrom(directory) }
	failed := make(chan error, 4)
	start := func() *remoteSurface {
		s, err := startRemoteSurface(home, "frost", draft, launch, func(err error) {
			select {
			case failed <- err:
			default:
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		})
		p := s.process.(*windowsHostProcess)
		var token windows.Token
		if err := windows.OpenProcessToken(p.process, windows.TOKEN_QUERY, &token); err != nil {
			t.Fatal(err)
		}
		if err := verifyHostToken(token); err != nil {
			token.Close()
			t.Fatal(err)
		}
		token.Close()
		if err := s.Update("star"); err != nil {
			t.Fatal(err)
		}
		if err := s.Snapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := start()
	p := s.process.(*windowsHostProcess)
	if err := windows.TerminateProcess(p.process, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("parent did not observe host death")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	text, rev := draft.Read()
	if text != "主进程恢复 abc 中文 😀" || rev != 1 {
		t.Fatal("host death lost parent draft")
	}
	s = start()
	if err := s.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	text, rev = draft.Read()
	if text != "主进程恢复 abc 中文 😀" || rev != 1 {
		t.Fatal("new renderer did not restore draft")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "quick-input-profile")); err != nil {
		t.Fatal("production profile not created")
	}
	t.Log("production restricted GUI host initialized, snapshotted, killed, recreated with parent draft, and fully closed")
}
