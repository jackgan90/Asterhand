package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

func TestOperationRetryAfterRestartAndExpiry(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	i := operations.Identity{Actor: "actor", Node: "node", ID: operations.NewID(now), Name: "commands.run", Digest: "digest", Target: "command"}
	r, fresh, err := s.Begin(i, now)
	if err != nil || !fresh {
		t.Fatal(r, fresh, err)
	}
	r.State, r.Accepted, r.Result = "completed", true, json.RawMessage(`{"run_id":"original"}`)
	if err = s.Finish(r, now); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, fresh, err = s.Begin(i, now.Add(24*time.Hour))
	if err != nil || fresh || !r.Accepted || string(r.Result) != `{"run_id":"original"}` {
		t.Fatal(r, fresh, err)
	}
	i.Digest = "different"
	if _, _, err = s.Begin(i, now); !errors.Is(err, operations.ErrConflict) {
		t.Fatal(err)
	}
	i.ID = operations.NewID(now.Add(-24 * time.Hour))
	if _, _, err = s.Begin(i, now); !errors.Is(err, operations.ErrExpired) {
		t.Fatal(err)
	}
	i.ID = operations.NewID(now.Add(time.Minute))
	if _, _, err = s.Begin(i, now); !errors.Is(err, operations.ErrExpired) {
		t.Fatal(err)
	}
}

func TestOperationCapacityProtectsUnknownAndReservesCompletion(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	i := operations.Identity{Actor: "a", Node: "n", ID: operations.NewID(now), Name: "write", Digest: "d"}
	r, _, err := s.Begin(i, now)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the real capacity query without allocating 64 MiB of fixture data.
	if _, err = s.db.Exec("UPDATE operations SET reserved=?", operations.ReceiptBytes); err != nil {
		t.Fatal(err)
	}
	i.ID = operations.NewID(now)
	if _, _, err = s.Begin(i, now); !errors.Is(err, operations.ErrCapacity) {
		t.Fatal(err)
	}
	r.State = "unknown"
	if err = s.Finish(r, now); err != nil {
		t.Fatal(err)
	}
	i.ID = operations.NewID(now.Add(31 * 24 * time.Hour))
	if _, _, err = s.Begin(i, now.Add(31*24*time.Hour)); !errors.Is(err, operations.ErrCapacity) {
		t.Fatal("unknown was evicted", err)
	}
	r.State, r.Result = "completed", json.RawMessage(`{"id":"accepted"}`)
	if err = s.Finish(r, now); err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := s.Begin(i, now.Add(31*24*time.Hour)); err != nil || !fresh {
		t.Fatal("expired terminal was not collected", fresh, err)
	}
	if _, _, err = s.Begin(r.Identity, now.Add(31*24*time.Hour)); !errors.Is(err, operations.ErrExpired) {
		t.Fatal("expired identity executed anew", err)
	}
}
