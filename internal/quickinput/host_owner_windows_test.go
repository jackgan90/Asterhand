//go:build windows

package quickinput

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestIsolatedHostOwnerDeath(t *testing.T) {
	if os.Getenv("ASTERHAND_DESKTOP_TEST") != "1" || os.Getenv("ASTERHAND_INPUT_HOST_TEST_DIRECTORY") == "" {
		t.Skip("requires production GUI host and explicit desktop validation")
	}
	if report := os.Getenv("ASTERHAND_OWNER_DEATH_REPORT"); report != "" {
		runHostOwner(t, report)
		return
	}
	for _, phase := range []string{"starting", "ready"} {
		t.Run(phase, func(t *testing.T) { assertHostOwnerDeath(t, phase) })
	}
}

func assertHostOwnerDeath(t *testing.T, phase string) {
	report := filepath.Join(t.TempDir(), "owned.json")
	command := exec.Command(os.Args[0], "-test.run=^TestIsolatedHostOwnerDeath$", "-test.timeout=1m")
	command.Env = append(os.Environ(), "ASTERHAND_OWNER_DEATH_REPORT="+report, "ASTERHAND_OWNER_DEATH_PHASE="+phase)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Process.Kill()
	var ids []uint32
	minimum := 1
	if phase == "ready" {
		minimum = 2
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(report)
		if err == nil && json.Unmarshal(data, &ids) == nil && len(ids) >= minimum {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(ids) < minimum {
		t.Fatal("owner did not report its host/browser tree")
	}
	var handles []windows.Handle
	for _, id := range ids {
		h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, id)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(h)
		handles = append(handles, h)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	for i, h := range handles {
		status, err := windows.WaitForSingleObject(h, 5000)
		if err != nil || status != windows.WAIT_OBJECT_0 {
			t.Errorf("owned process %d survived broker death: status=%d err=%v", ids[i], status, err)
		}
	}
}

func runHostOwner(t *testing.T, report string) {
	directory := os.Getenv("ASTERHAND_INPUT_HOST_TEST_DIRECTORY")
	if os.Getenv("ASTERHAND_OWNER_DEATH_PHASE") == "starting" {
		p, err := launchWindowsHostFrom(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Stop()
		// The production host is waiting for Initialize. Kill its owner before
		// the UI or browser exists, without adding a production test hook.
		writeOwnedProcesses(t, report, []uint32{p.(*windowsHostProcess).pid})
		select {}
	}
	s, err := startRemoteSurface(filepath.Dir(report), "star", &Draft{}, func() (hostProcess, error) {
		return launchWindowsHostFrom(directory)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var ids []uint32
	for id := range processTreeCost().samples {
		if id != uint32(os.Getpid()) {
			ids = append(ids, id)
		}
	}
	writeOwnedProcesses(t, report, ids)
	select {}
}

func writeOwnedProcesses(t *testing.T, report string, ids []uint32) {
	data, _ := json.Marshal(ids)
	if err := os.WriteFile(report, data, 0600); err != nil {
		t.Fatal(err)
	}
}
