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

func prepareRecoveryRound(t *testing.T, store runStore, command Command, count int) runIndex {
	t.Helper()
	index := runIndex{Version: 1, RoundID: model.ID(), Sequence: uint64(count)}
	for i := range count {
		id := model.ID()
		run := &managedRun{record: RunRecord{Version: 1, ID: id, RoundID: index.RoundID,
			Sequence: uint64(i + 1), Command: command, Phase: "running", LogComplete: true}}
		initializeGraph(run)
		run.record.Nodes[1].Phase, run.record.Nodes[1].RunID = "running", model.ID()
		file, err := store.prepare(run.record)
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
		index.IDs = append(index.IDs, id)
		index.Latest = id
	}
	if err := store.publish(command.ID, index); err != nil {
		t.Fatal(err)
	}
	return index
}

func TestRecoveryErrorsStillReconcileOtherValidInstances(t *testing.T) {
	for _, failure := range []string{"first_record", "middle_record", "index", "missing_log"} {
		t.Run(failure, func(t *testing.T) {
			home := t.TempDir()
			backend := &graphBackend{created: make(chan graphStarted, 8)}
			backend.empty.Store(true)
			command, other := testCommand(t, "damaged"), testCommand(t, "valid")
			command.ID, other.ID = model.ID(), model.ID()
			store := runStore{root: filepath.Join(home, "command-runs")}
			index := prepareRecoveryRound(t, store, command, 3)
			prepareRecoveryRound(t, store, other, 1)
			orphan := store.logPath(command.ID, model.ID())
			if err := os.WriteFile(orphan, []byte("retain evidence"), 0600); err != nil {
				t.Fatal(err)
			}
			badPath, expected := store.recordPath(command.ID, index.IDs[0]), 3
			switch failure {
			case "middle_record":
				badPath = store.recordPath(command.ID, index.IDs[1])
			case "index":
				badPath, expected = store.indexPath(command.ID), 1
			case "missing_log":
				badPath, expected = store.logPath(command.ID, index.IDs[1]), 4
			}
			var err error
			if failure == "missing_log" {
				err = os.Remove(badPath)
			} else {
				err = os.WriteFile(badPath, []byte("invalid"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			manager := NewRunManager(home, backend, nil)
			if manager.loadErr == nil || manager.Available() {
				t.Fatal("recovery error did not disable admission")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err = manager.Close(ctx); err != nil {
				t.Fatal("known recovery scopes were abandoned", err)
			}
			backend.mu.Lock()
			checked := len(backend.checked)
			backend.mu.Unlock()
			if checked != expected || manager.Busy("") {
				t.Fatalf("reconciled %d identities, want %d; busy=%v", checked, expected, manager.Busy(""))
			}
			if text, err := os.ReadFile(orphan); err != nil || string(text) != "retain evidence" {
				t.Fatal("failed group collected evidence", err)
			}
			if failure != "missing_log" {
				if text, err := os.ReadFile(badPath); err != nil || string(text) != "invalid" {
					t.Fatal("recovery overwrote invalid data", err)
				}
			}
		})
	}
}

func TestDiscardedCapturedOutputMarksEachNodeIncomplete(t *testing.T) {
	for _, reason := range []string{"log_limit", "log_io_error"} {
		t.Run(reason, func(t *testing.T) {
			command := graphCommand(t)
			manager := NewRunManager(t.TempDir(), nil, nil)
			run := &managedRun{record: RunRecord{Version: 1, ID: model.ID(), RoundID: model.ID(),
				Sequence: 1, Command: command, Phase: "running", LogComplete: true},
				busy: true, group: &runGroup{}, done: make(chan struct{})}
			initializeGraph(run)
			run.record.Nodes[2].Phase, run.record.Nodes[3].Phase = "running", "running"
			file, err := manager.store.prepare(run.record)
			if err != nil {
				t.Fatal(err)
			}
			run.file = file
			defer file.Close()
			if reason == "log_limit" {
				run.logBytes = MaxRunLogBytes
				run.mu.Lock()
				err = manager.appendRunLog(run, 2, 0, []byte("over limit"))
				run.mu.Unlock()
				if err == nil || run.record.Reason != reason {
					t.Fatal("expected quota failure")
				}
			} else {
				run.record.Reason, run.record.StorageError = reason, true
			}
			// A sibling reader can already hold output when failure stops the
			// graph, and it must also report bytes discarded while draining.
			manager.captureGraph(run, 3, strings.NewReader("captured sibling tail"), 0)
			if run.record.Nodes[3].LogComplete || run.record.LogComplete || run.record.Reason != reason {
				t.Fatal("discarded bytes lost their node-level warning or original reason")
			}
		})
	}
}
