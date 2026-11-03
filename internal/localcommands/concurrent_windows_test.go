//go:build windows

package localcommands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func awaitWindowsRunOutput(t *testing.T, manager *RunManager, commandID, runID, marker string) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		page, err := manager.Read(commandID, runID, 0)
		if err != nil {
			t.Fatal(err)
		}
		var output strings.Builder
		for _, chunk := range page.Chunks {
			output.Write(chunk.Data)
		}
		if strings.Contains(output.String(), marker) {
			return output.String()
		}
		if time.Now().After(deadline) {
			t.Fatalf("run output did not arrive: %q, record=%+v", output.String(), page.Run)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWindowsConcurrentRunsStopAndRetainIndependently(t *testing.T) {
	command := testCommand(t, "concurrent_windows")
	command.ID = model.ID()
	command.AllowConcurrent = true
	workspace := filepath.Dir(command.Graph.Nodes[1].Script)
	script := filepath.Join(workspace, "concurrent.ps1")
	body := "$label=[guid]::NewGuid().ToString('N')\r\nWrite-Output \"started:$label\"\r\nwhile (-not (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'release'))) { Start-Sleep -Milliseconds 30 }\r\nWrite-Output \"finished:$label\"\r\n"
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	command.Graph.Nodes[1].Script = script
	home := t.TempDir()
	backend := NewPlatformBackend()
	manager := NewRunManager(home, backend, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	first, err := manager.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Start(command)
	if err != nil {
		t.Fatal(err)
	}
	a := strings.TrimSpace(awaitWindowsRunOutput(t, manager, command.ID, first.ID, "started:"))
	b := strings.TrimSpace(awaitWindowsRunOutput(t, manager, command.ID, second.ID, "started:"))
	if a == b || !strings.HasPrefix(a, "started:") || !strings.HasPrefix(b, "started:") {
		t.Fatalf("independent process identities missing: %q %q", a, b)
	}
	if _, err = manager.Stop(command.ID, first.ID, "user_stop"); err != nil {
		t.Fatal(err)
	}
	stopped := finishedInstance(t, manager, command.ID, first.ID)
	if stopped.Result != "interrupted" || manager.Run(command.ID, second.ID).Phase != "running" {
		t.Fatal("stopping one instance affected its sibling", stopped, manager.Run(command.ID, second.ID))
	}
	if err = os.WriteFile(filepath.Join(workspace, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	completed := finishedInstance(t, manager, command.ID, second.ID)
	if completed.Result != "succeeded" {
		t.Fatal(completed)
	}
	secondOutput := awaitWindowsRunOutput(t, manager, command.ID, second.ID, "finished:")
	if strings.Contains(secondOutput, strings.TrimPrefix(a, "started:")) || !strings.Contains(secondOutput, "finished:"+strings.TrimPrefix(b, "started:")) {
		t.Fatal("cross-instance output contamination", secondOutput)
	}
	for _, record := range []*RunRecord{stopped, completed} {
		if empty, err := backend.Reconcile(record.Nodes[1].RunID); err != nil || !empty {
			t.Fatal("finished instance still owns a live process scope", empty, err)
		}
	}
	if err = manager.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager = NewRunManager(home, backend, nil)
	if manager.loadErr != nil || len(manager.Runs(command.ID).Runs) != 2 || manager.Current(command.ID).ID != second.ID {
		t.Fatal("reopen lost retained round", manager.loadErr, manager.Runs(command.ID))
	}
	for _, id := range []string{first.ID, second.ID} {
		if _, err = manager.Read(command.ID, id, 0); err != nil {
			t.Fatal("retained log unreadable after reopen", err)
		}
	}
	third, err := manager.Start(command)
	if err != nil || third.RoundID == first.RoundID {
		t.Fatal("next round did not start", third, err)
	}
	if _, err = manager.Read(command.ID, first.ID, 0); err == nil {
		t.Fatal("previous round still readable after new admission")
	}
	if result := finishedInstance(t, manager, command.ID, third.ID); result.Result != "succeeded" {
		t.Fatal(result)
	}
}
