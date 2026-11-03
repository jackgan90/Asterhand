package localcommands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedRegressionSmallFramesWireBudget(t *testing.T) {
	_, m, b, _, c := managedFixture(t)
	run, err := m.Start(c)
	if err != nil {
		t.Fatal(err)
	}
	e := <-b.created
	e.complete()
	awaitFinished(t, m, c.ID)
	f, err := os.OpenFile(m.store.logPath(c.ID, run.ID), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	writer := &logFile{File: f}
	for range 3000 {
		if err = appendGraphLog(writer, 1, 0, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	total := 0
	for cursor := int64(0); ; {
		page, err := m.Read(c.ID, run.ID, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, chunk := range page.Chunks {
			total += len(chunk.Data)
		}
		if page.End {
			break
		}
		if page.Next <= cursor {
			t.Fatal("paging did not advance")
		}
		cursor = page.Next
	}
	if total != 3000 {
		t.Fatal("paging lost bytes", total)
	}
	for _, cursor := range []int64{0, -1} {
		page, err := m.Read(c.ID, run.ID, cursor)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(page)
		t.Logf("cursor=%d chunks=%d JSON=%d", cursor, len(page.Chunks), len(data))
		if len(data) > 96*1024 {
			t.Errorf("valid page exceeds log host budget: %d", len(data))
		}
	}
}
func TestManagedRegressionFailedClearCannotResurrect(t *testing.T) {
	r, m, b, _, c := managedFixture(t)
	run, err := m.Start(c)
	if err != nil {
		t.Fatal(err)
	}
	e := <-b.created
	e.complete()
	awaitFinished(t, m, c.ID)
	blocked := filepath.Join(m.store.directory(c.ID), "deleting")
	if err = os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Delete(c.ID, mustSnapshot(t, r).Revision); err == nil {
		t.Fatal("expected marker error")
	}
	disk := Open(filepath.Dir(m.store.root))
	if len(mustSnapshot(t, disk).PendingCleanup) != 1 {
		t.Fatal("cleanup intent not durable")
	}
	if _, err = r.Save(Mutation{Revision: mustSnapshot(t, r).Revision, Command: testCommand(t, "new")}); err == nil {
		t.Fatal("saved while cleanup failed")
	}
	if err = os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	reopened := NewRunManager(filepath.Dir(m.store.root), b, nil)
	rr := Open(filepath.Dir(m.store.root))
	if err = rr.ConfigureRuns(reopened); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Current(c.ID); got != nil {
		t.Fatalf("deleted result resurrected: old=%s got=%+v", run.ID, got)
	}
}

func TestManagedRegressionRunningElapsedIsLive(t *testing.T) {
	_, m, b, clock, c := managedFixture(t)
	if _, err := m.Start(c); err != nil {
		t.Fatal(err)
	}
	<-b.created
	awaitRun(t, m, c.ID, func(r *RunRecord) bool { return r.Phase == "running" })
	clock.now.Store(11_000_000_000)
	got := m.Current(c.ID)
	if got.Elapsed != 11 {
		t.Errorf("running elapsed=%v after clock advanced 11 seconds", got.Elapsed)
	}
}

func TestManagedLogWireBudgetIncludesLargeCommandSnapshot(t *testing.T) {
	store := runStore{root: t.TempDir()}
	c := Command{ID: "command1234567890", Graph: singleScriptGraph(strings.Repeat("&", 2048)), Description: strings.Repeat("&", 512)}
	for range 16 {
		c.Aliases = append(c.Aliases, strings.Repeat("&", 128))
	}
	c.Graph.Nodes[1].Arguments = []string{strings.Repeat("x", 4000), strings.Repeat("x", 4000)}
	record := RunRecord{ID: "execution123456789", Command: c, LogFormat: 2, Nodes: []NodeRecord{{Kind: "script", NodeID: "script1234567890"}}}
	f, err := store.prepare(record)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		if err = appendGraphLog(f, 0, 0, []byte(strings.Repeat("x", logBlockSize))); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	for cursor := int64(0); ; {
		page, err := store.read(record, cursor)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(page)
		if len(encoded) > 96*1024 {
			t.Fatal("oversized page", len(encoded))
		}
		if page.End {
			break
		}
		if page.Next <= cursor {
			t.Fatal("paging stalled")
		}
		cursor = page.Next
	}
}

func TestManagedLogFinalPageMarksEndAtBudget(t *testing.T) {
	store := runStore{root: t.TempDir()}
	record := RunRecord{ID: "execution123456789", Command: Command{ID: "command1234567890"}, LogFormat: 2, Nodes: []NodeRecord{{Kind: "script", NodeID: "script1234567890"}}}
	f, err := store.prepare(record)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = appendGraphLog(f, 0, 0, []byte(strings.Repeat("x", logBlockSize-1)+"\xe4")); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	page, err := store.read(record, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !page.End {
		t.Fatal("final decoder would not flush on the last full page")
	}
}
