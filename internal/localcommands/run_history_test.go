package localcommands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

func TestRunHistoryRetainsSummaryAfterDetailPruningAndDelete(t *testing.T) {
	r, m, b, _, command := managedFixture(t)
	var ids []string
	for range 12 {
		record, e := startInstance(t, m, b, command)
		e.complete()
		finishedInstance(t, m, command.ID, record.ID)
		ids = append(ids, record.ID)
	}
	items, next, err := r.RunHistory(command.ID, "", 5)
	if err != nil || len(items) != 5 || next == "" || items[0].ID != ids[11] {
		t.Fatal(items, next, err)
	}
	older, _, err := r.RunHistory(command.ID, next, 20)
	if err != nil || len(older) != 7 || older[0].ID != ids[6] {
		t.Fatal(older, err)
	}
	meta, err := r.RunDetails(command.ID, ids[0])
	if err != nil || meta.Details != "round_limit" {
		t.Fatal(meta, err)
	}
	if _, err = m.Read(command.ID, ids[0], 0); err == nil {
		t.Fatal("expired details read as empty success")
	}
	if err = m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened := testRunManager(t, filepath.Dir(m.store.root), b, nil)
	if reopened.loadErr != nil {
		t.Fatal(reopened.loadErr)
	}
	if got := reopened.Run(command.ID, ids[0]); got == nil || got.Result != "succeeded" {
		t.Fatal(got)
	}
	if err = reopened.Clear(command.ID); err != nil {
		t.Fatal(err)
	}
	meta, err = reopened.history.get(ids[11])
	if err != nil || meta.Details != "command_deleted" || meta.Result != "succeeded" {
		t.Fatal(meta, err)
	}
	if _, err = os.Stat(reopened.store.directory(command.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestRunHistoryCurrentExpiredDetailsRecoverWithoutExecution(t *testing.T) {
	_, m, b, _, command := managedFixture(t)
	record, e := startInstance(t, m, b, command)
	e.complete()
	finishedInstance(t, m, command.ID, record.ID)
	run := m.runs[record.ID]
	run.mu.Lock()
	ended := time.Now().Add(-8 * 24 * time.Hour)
	run.record.Ended = &ended
	if err := m.store.save(run.record); err != nil {
		t.Fatal(err)
	}
	if err := m.history.save(run.record); err != nil {
		t.Fatal(err)
	}
	run.mu.Unlock()
	m.mu.Lock()
	err := m.pruneHistoryLocked()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened := testRunManager(t, filepath.Dir(m.store.root), b, nil)
	if reopened.loadErr != nil || reopened.Current(command.ID) == nil {
		t.Fatal(reopened.loadErr)
	}
	if b.count.Load() != 1 {
		t.Fatal("history restarted work")
	}
	meta, err := reopened.history.get(record.ID)
	if err != nil || meta.Details != "expired" {
		t.Fatal(meta, err)
	}
}

func TestHistoryReservationAndNodeBudgetPrecedeExecution(t *testing.T) {
	_, m, b, _, command := managedFixture(t)
	m.diskBytes.Store(MaxNodeRunBytes)
	if _, err := m.Start(command); !errors.Is(err, operations.ErrCapacity) {
		t.Fatal(err)
	}
	if b.count.Load() != 0 {
		t.Fatal("capacity rejection executed")
	}
	// A private preparation left without an index is removed after restart.
	m.diskBytes.Store(0)
	record := RunRecord{Version: 1, ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RoundID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Sequence: 1, Command: command, Started: time.Now(), Phase: "starting"}
	if err := m.history.reserve(record); err != nil {
		t.Fatal(err)
	}
	f, err := m.store.prepare(record)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened := testRunManager(t, filepath.Dir(m.store.root), b, nil)
	if reopened.loadErr != nil {
		t.Fatal(reopened.loadErr)
	}
	if _, err = reopened.history.get(record.ID); !errors.Is(err, operations.ErrMissing) {
		t.Fatal(err)
	}
	if _, err = os.Stat(reopened.store.recordPath(command.ID, record.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if b.count.Load() != 0 {
		t.Fatal("unpublished preparation executed")
	}
}
