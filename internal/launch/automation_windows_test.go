//go:build windows

package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

func TestAutomationStartReconcilesWithoutAnotherLaunch(t *testing.T) {
	path, installation := installationFixture(t)
	if err := os.MkdirAll(installation.Data, 0700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(installation.Data, "config.json"), map[string]string{"node_id": "test-node"})
	id := operations.Identity{Actor: "agent", Node: "test-node", ID: operations.NewIDFor("node_start", time.Now()), Name: "node_start", Digest: operations.Hash(struct{}{}), Target: "test-node"}
	prepared := operations.Receipt{Identity: id, State: "prepared", Created: time.Now().UnixMilli(), Updated: time.Now().UnixMilli()}
	if err := saveStartReceipts(installation.Data, startReceipts{Version: 1, Items: []operations.Receipt{prepared}}); err != nil {
		t.Fatal(err)
	}
	// No healthy endpoint: replay must leave the old launch unknown. The fixture
	// executable is deliberately not a working node, so a new launch would fail.
	r, err := StartOperation(context.Background(), path, installation.Data, id, func() error { return nil })
	if err != nil || r.State != "prepared" {
		t.Fatal(r, err)
	}
	_, status := statusFixture(t, installation.Data, healthy)
	writeJSON(t, filepath.Join(installation.Data, "runtime.json"), status)
	r, err = StartOperation(context.Background(), path, installation.Data, id, func() error { return nil })
	if err != nil || r.State != "completed" || !r.Accepted {
		t.Fatal(r, err)
	}
	if err = os.Remove(filepath.Join(installation.Data, "runtime.json")); err != nil {
		t.Fatal(err)
	}
	r, err = StartOperation(context.Background(), path, installation.Data, id, func() error { return nil })
	if err != nil || r.State != "completed" {
		t.Fatal("replay tried to start again", r, err)
	}
	conflict := id
	conflict.Digest = "changed"
	if _, err = StartOperation(context.Background(), path, installation.Data, conflict, func() error { return nil }); !errors.Is(err, operations.ErrConflict) {
		t.Fatal(err)
	}
}

func TestAutomationStartRejectsWrongIdentityAndRevocation(t *testing.T) {
	path, installation := installationFixture(t)
	if err := os.MkdirAll(installation.Data, 0700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(installation.Data, "config.json"), map[string]string{"node_id": "node"})
	id := operations.Identity{Actor: "agent", Node: "node", ID: operations.NewID(time.Now()), Name: "node_start"}
	check := func() error { return nil }
	if _, err := StartOperation(context.Background(), path, installation.Data, id, check); !errors.Is(err, operations.ErrInvalid) {
		t.Fatal(err)
	}
	id.ID = operations.NewIDFor("node_start", time.Now())
	denied := errors.New("revoked")
	if _, err := StartOperation(context.Background(), path, installation.Data, id, func() error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	if _, err := ReadStartReceipt(installation.Data, id.Actor, id.Node, id.ID); !errors.Is(err, operations.ErrMissing) {
		t.Fatal("unauthorized launch was admitted", err)
	}
	id.Node = "wrong-node"
	if _, err := StartOperation(context.Background(), path, installation.Data, id, check); !errors.Is(err, operations.ErrConflict) {
		t.Fatal(err)
	}
}

func TestAutomationStartRollsBackPreparedReceiptOnPreLaunchCheckFailure(t *testing.T) {
	path, installation := installationFixture(t)
	if err := os.MkdirAll(installation.Data, 0700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(installation.Data, "config.json"), map[string]string{"node_id": "test-node"})
	_, status := statusFixture(t, installation.Data, healthy)
	writeJSON(t, filepath.Join(installation.Data, "runtime.json"), status)

	t.Run("rollback_succeeds_and_same_id_retries", func(t *testing.T) {
		id := operations.Identity{
			Actor:  "harness:antigravity",
			Node:   "test-node",
			ID:     operations.NewIDFor("node_start", time.Now()),
			Name:   "node_start",
			Digest: operations.Hash(struct{}{}),
			Target: "test-node",
		}
		calls := 0
		preLaunchErr := errors.New("pre_launch_disconnect")
		r, err := StartOperation(context.Background(), path, installation.Data, id, func() error {
			calls++
			if calls == 2 {
				return preLaunchErr
			}
			return nil
		})
		if !errors.Is(err, preLaunchErr) || r.ID != "" {
			t.Fatalf("expected original pre-launch error with empty receipt after rollback, got r=%+v err=%v", r, err)
		}
		if _, err := ReadStartReceipt(installation.Data, id.Actor, id.Node, id.ID); !errors.Is(err, operations.ErrMissing) {
			t.Fatalf("expected prepared receipt removed after rollback, got %v", err)
		}

		// Retrying with the exact same ID succeeds once check passes.
		r2, err := StartOperation(context.Background(), path, installation.Data, id, func() error { return nil })
		if err != nil || r2.State != "completed" || !r2.Accepted {
			t.Fatalf("expected same-ID retry after pre-launch rollback to complete, got r=%+v err=%v", r2, err)
		}
	})

	t.Run("rollback_write_failure_reports_distinct_persistence_error", func(t *testing.T) {
		id := operations.Identity{
			Actor:  "harness:antigravity",
			Node:   "test-node",
			ID:     operations.NewIDFor("node_start", time.Now()),
			Name:   "node_start",
			Digest: operations.Hash(struct{}{}),
			Target: "test-node",
		}
		origWrite := writeStartReceipts
		t.Cleanup(func() { writeStartReceipts = origWrite })
		writes := 0
		writeStartReceipts = func(home string, book startReceipts) error {
			writes++
			if writes == 2 {
				return errors.New("disk_write_denied")
			}
			return origWrite(home, book)
		}
		calls := 0
		checkErr := errors.New("pre_launch_revoked")
		r, err := StartOperation(context.Background(), path, installation.Data, id, func() error {
			calls++
			if calls == 2 {
				return checkErr
			}
			return nil
		})
		if !errors.Is(err, ErrStartReceiptRollback) || !errors.Is(err, checkErr) {
			t.Fatalf("expected ErrStartReceiptRollback wrapping check error, got %v", err)
		}
		if r.ID != id.ID || r.State != "prepared" {
			t.Fatalf("expected persisted prepared receipt returned on rollback failure, got %+v", r)
		}
	})
}
