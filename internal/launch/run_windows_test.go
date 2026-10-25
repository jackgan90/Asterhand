//go:build windows

package launch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"golang.org/x/sys/windows"
)

func TestMain(m *testing.M) {
	if os.Getenv("ASTERHAND_LAUNCH_TEST_CHILD") == "1" {
		console, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
		fmt.Printf("child arguments: %q\n", os.Args[1:])
		fmt.Fprintln(os.Stderr, "child diagnostics")
		if console != 0 {
			os.Exit(8)
		}
		time.Sleep(50 * time.Millisecond)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func currentProcess(t *testing.T) processRecord {
	t.Helper()
	process, err := inspectProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return process
}

func TestConcurrentLaunchesStartOneNode(t *testing.T) {
	path, installation := installationFixture(t)
	_, record := statusFixture(t, installation.Data, healthy)
	process := currentProcess(t)
	var starts, opens atomic.Int32
	r := runner{timeout: 2 * time.Second, poll: 5 * time.Millisecond}
	r.open = func(string) error { opens.Add(1); return nil }
	r.start = func(Installation) (processRecord, <-chan error, error) {
		starts.Add(1)
		// The node takes time to become ready, so concurrent entry points must
		// wait for the first launch rather than each creating a new process.
		time.AfterFunc(80*time.Millisecond, func() { writeJSON(t, filepath.Join(installation.Data, "runtime.json"), record) })
		return process, nil, nil
	}
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- r.run(path, false) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if starts.Load() != 1 || opens.Load() != 4 {
		t.Fatalf("starts=%d opens=%d", starts.Load(), opens.Load())
	}
	if _, err := os.Stat(filepath.Join(installation.Data, "launch-state.json")); !os.IsNotExist(err) {
		t.Fatal("successful startup retained pending state")
	}
}

func TestHealthyExistingNodeSkipsStartAndNoBrowser(t *testing.T) {
	path, installation := installationFixture(t)
	_, record := statusFixture(t, installation.Data, healthy)
	writeJSON(t, filepath.Join(installation.Data, "runtime.json"), record)
	r := runner{timeout: time.Second, poll: time.Millisecond}
	r.start = func(Installation) (processRecord, <-chan error, error) {
		t.Fatal("started duplicate node")
		return processRecord{}, nil, nil
	}
	r.open = func(string) error { t.Fatal("opened browser with no-browser"); return nil }
	if err := r.run(path, true); err != nil {
		t.Fatal(err)
	}
}

func TestTimeoutPreservesEarlyChildAndPreventsSecondLaunch(t *testing.T) {
	path, installation := installationFixture(t)
	process := currentProcess(t)
	var starts atomic.Int32
	r := runner{timeout: 75 * time.Millisecond, poll: 5 * time.Millisecond}
	r.start = func(Installation) (processRecord, <-chan error, error) { starts.Add(1); return process, nil, nil }
	for range 2 {
		if err := r.run(path, true); err == nil || !strings.Contains(err.Error(), "超时") {
			t.Fatalf("expected retained-process timeout: %v", err)
		}
	}
	if starts.Load() != 1 {
		t.Fatal("timed out child was replaced")
	}
	if pending, err := pendingProcess(installation.Data); err != nil || !pending {
		t.Fatalf("live startup identity lost: %v", err)
	}
}

func TestBusyServiceLockPreventsLaunchWithoutHealthyHTTP(t *testing.T) {
	path, installation := installationFixture(t)
	if err := prepareHome(installation.Data); err != nil {
		t.Fatal(err)
	}
	unlock, err := config.Lock(installation.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	r := runner{timeout: 50 * time.Millisecond, poll: 5 * time.Millisecond}
	r.start = func(Installation) (processRecord, <-chan error, error) {
		t.Fatal("started despite held service lock")
		return processRecord{}, nil, nil
	}
	if err := r.run(path, true); err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("expected timeout: %v", err)
	}
}

func TestPendingRecordDetectsReusedPIDAndUnknownProcess(t *testing.T) {
	home := t.TempDir()
	process := currentProcess(t)
	process.Created++
	if err := saveProcess(home, process); err != nil {
		t.Fatal(err)
	}
	if pending, err := pendingProcess(home); err != nil || pending {
		t.Fatalf("reused pid retained: %v", err)
	}
	process.Created = 0
	if err := saveProcess(home, process); err != nil {
		t.Fatal(err)
	}
	if _, err := pendingProcess(home); err == nil {
		t.Fatal("unknown creation time was treated as permission to restart")
	}
}

func TestLaunchLockTimesOutAndReleases(t *testing.T) {
	home, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := acquireLaunchLock(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		release, err := acquireLaunchLock(ctx, home)
		if err == nil {
			release()
			result <- errors.New("acquired mutex while another thread owned it")
		} else {
			result <- nil
		}
	}()
	err = <-result
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	unlock, err = acquireLaunchLock(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestNativeChildHasNoConsoleAndAppendsLogs(t *testing.T) {
	_, installation := installationFixture(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(installation.Executable, b, 0700); err != nil {
		t.Fatal(err)
	}
	if err = prepareHome(installation.Data); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"startup.log", "error.log"} {
		if err = os.WriteFile(filepath.Join(installation.Data, name), []byte("existing evidence\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ASTERHAND_LAUNCH_TEST_CHILD", "1")
	process, exited, err := startNode(installation)
	if err != nil || process.PID == 0 || process.Created == 0 {
		t.Fatalf("child startup failed: %+v %v", process, err)
	}
	select {
	case err = <-exited:
		if err != nil {
			t.Fatalf("child had console or failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit")
	}
	for _, name := range []string{"startup.log", "error.log"} {
		b, err := os.ReadFile(filepath.Join(installation.Data, name))
		if err != nil || !strings.HasPrefix(string(b), "existing evidence\nchild") {
			t.Fatalf("child log lost previous evidence: %s %v", name, err)
		}
	}
}
