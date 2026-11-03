package localcommands

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func awaitInstance(t *testing.T, m *RunManager, commandID, runID string, predicate func(*RunRecord) bool) *RunRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run := m.Run(commandID, runID)
		if run != nil && predicate(run) {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("instance did not settle: %+v", run)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func finishedInstance(t *testing.T, m *RunManager, commandID, runID string) *RunRecord {
	t.Helper()
	return awaitInstance(t, m, commandID, runID, func(r *RunRecord) bool { return r.Phase == "finished" })
}

func startInstance(t *testing.T, m *RunManager, b *fakeBackend, c Command) (*RunRecord, *fakeExecution) {
	t.Helper()
	run, err := m.Start(c)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case execution := <-b.created:
		return run, execution
	case <-time.After(3 * time.Second):
		t.Fatal("instance did not reach backend")
		return nil, nil
	}
}

func TestConcurrentRunsRetainAcceptanceOrderAndAllLogsUntilNextRound(t *testing.T) {
	_, m, b, _, c := managedFixture(t)
	c.AllowConcurrent = true
	first, a := startInstance(t, m, b, c)
	second, z := startInstance(t, m, b, c)
	io.WriteString(a.outWriter, "older output")
	io.WriteString(z.outWriter, "newer output")
	z.complete()
	finishedInstance(t, m, c.ID, second.ID)
	summary := m.Summary(c.ID)
	if summary.Active != 1 || summary.RunID != second.ID || summary.Result != "succeeded" {
		t.Fatal("latest hides older active run", summary)
	}
	a.complete()
	finishedInstance(t, m, c.ID, first.ID)
	if m.Current(c.ID).ID != second.ID {
		t.Fatal("completion order replaced admission order")
	}
	if first.RoundID != second.RoundID || second.Sequence != first.Sequence+1 {
		t.Fatal("round or sequence changed within live round")
	}
	reopened := NewRunManager(filepath.Dir(m.store.root), b, nil)
	if reopened.loadErr != nil {
		t.Fatal(reopened.loadErr)
	}
	list := reopened.Runs(c.ID)
	if len(list.Runs) != 2 || list.LatestID != second.ID {
		t.Fatal("restart dropped retained instances", list)
	}
	for id, want := range map[string]string{first.ID: "older output", second.ID: "newer output"} {
		page, err := reopened.ReadNode(c.ID, id, c.Graph.Nodes[1].ID, 0, false)
		if err != nil || len(page.Chunks) != 1 || string(page.Chunks[0].Data) != want {
			t.Fatal("cross-instance log contamination", page, err)
		}
	}
	third, e := startInstance(t, reopened, b, c)
	defer reopened.Close(context.Background())
	if third.RoundID == first.RoundID || third.Sequence != second.Sequence+1 {
		t.Fatal("next round did not advance")
	}
	if len(reopened.Runs(c.ID).Runs) != 1 {
		t.Fatal("old round retained in visible list")
	}
	for _, id := range []string{first.ID, second.ID} {
		if _, err := reopened.Read(c.ID, id, 0); err == nil {
			t.Fatal("old read accepted")
		}
		if _, err := os.Stat(reopened.store.logPath(c.ID, id)); !os.IsNotExist(err) {
			t.Fatal("old log was not collected", err)
		}
	}
	e.complete()
	finishedInstance(t, reopened, c.ID, third.ID)
}

func TestConcurrentAdmissionLimitAndStopAllUseCapturedIDs(t *testing.T) {
	r, m, _, _, c := managedFixture(t)
	c.AllowConcurrent = true
	c.Aliases = []string{"并行"}
	c = saveCommand(t, r, c).Catalog.Commands[0]
	var accepted atomic.Int32
	var workers sync.WaitGroup
	for range 24 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if r.Launch(context.Background(), c.ID, c.Revision).Started {
				accepted.Add(1)
			}
		}()
	}
	workers.Wait()
	if accepted.Load() != MaxActiveCommandRuns {
		t.Fatal("admission cap", accepted.Load())
	}
	list := m.Runs(c.ID)
	if len(list.Runs) != MaxActiveCommandRuns || len(m.Summary(c.ID).ActiveRunIDs) != MaxActiveCommandRuns {
		t.Fatal(list)
	}
	if _, err := r.Save(Mutation{Revision: mustSnapshot(t, r).Revision, Command: c}); err == nil {
		t.Fatal("edited active command")
	}
	if _, err := r.Delete(c.ID, mustSnapshot(t, r).Revision); err == nil {
		t.Fatal("deleted active command")
	}
	// Retire one member while the others keep this round alive, then admit a new
	// member after the UI has captured the stop set.
	ids := make([]string, 0, len(list.Runs))
	for _, run := range list.Runs {
		ids = append(ids, run.ID)
	}
	if _, err := m.Stop(c.ID, ids[0], "user_stop"); err != nil {
		t.Fatal(err)
	}
	finishedInstance(t, m, c.ID, ids[0])
	newest, err := m.Start(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.StopAll(c.ID, ids); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if got := finishedInstance(t, m, c.ID, id); got.Result != "interrupted" {
			t.Fatal(got)
		}
	}
	if got := m.Run(c.ID, newest.ID); got.Phase == "finished" || got.Reason != "" {
		t.Fatal("late stop reached new member", got)
	}
	if err = m.StopAll(c.ID, ids); err != nil {
		t.Fatal("repeat stop lost idempotence", err)
	}
	if err = m.StopAll(c.ID, []string{newest.ID, model.ID()}); err == nil {
		t.Fatal("invalid set accepted")
	}
	if got := m.Run(c.ID, newest.ID); got.Reason != "" {
		t.Fatal("invalid set partially stopped", got)
	}
	if _, err = m.Stop(c.ID, newest.ID, "user_stop"); err != nil {
		t.Fatal(err)
	}
	finishedInstance(t, m, c.ID, newest.ID)
}

func TestFailedPublicationPreservesPreviousRoundAndLeavesNoPreparedFiles(t *testing.T) {
	_, m, b, _, c := managedFixture(t)
	first, e := startInstance(t, m, b, c)
	io.WriteString(e.outWriter, "retain me")
	e.complete()
	finishedInstance(t, m, c.ID, first.ID)
	path := m.store.indexPath(c.ID)
	backup := path + ".test-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	defer func() { os.Remove(path); os.Rename(backup, path) }()
	for range 3 {
		if _, err := m.Start(c); err == nil {
			t.Fatal("publication fault accepted")
		}
	}
	if got := m.Current(c.ID); got.ID != first.ID {
		t.Fatal("lost previous run", got)
	}
	page, err := m.Read(c.ID, first.ID, 0)
	if err != nil || string(page.Chunks[0].Data) != "retain me" {
		t.Fatal("lost previous log", err)
	}
	files, err := os.ReadDir(m.store.directory(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("failed attempts leaked private files: %+v", files)
	}
	if b.count.Load() != 1 {
		t.Fatal("unpublished execution started")
	}
}

func TestFinalStorageFailureRetainsOccupationUntilDurable(t *testing.T) {
	r, m, b, _, c := managedFixture(t)
	c.AllowConcurrent = true
	first, e := startInstance(t, m, b, c)
	awaitInstance(t, m, c.ID, first.ID, func(r *RunRecord) bool { return r.Nodes[1].Phase == "running" })
	path := m.store.recordPath(c.ID, first.ID)
	backup := path + ".test-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	repaired := false
	defer func() {
		if !repaired {
			os.Remove(path)
			os.Rename(backup, path)
		}
	}()
	e.complete()
	got := awaitInstance(t, m, c.ID, first.ID, func(r *RunRecord) bool { return r.Phase == "draining" && r.CleanupPending })
	if !got.StorageError || !m.Busy(c.ID) {
		t.Fatal("unpersisted terminal state released occupation", got)
	}
	if _, err := m.Start(c); err == nil {
		t.Fatal("started while terminal storage unresolved")
	}
	if _, err := r.Delete(c.ID, mustSnapshot(t, r).Revision); err == nil {
		t.Fatal("deleted recovery evidence")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	repaired = true
	got = finishedInstance(t, m, c.ID, first.ID)
	if got.Result != "failed" || got.CleanupPending || m.Busy(c.ID) {
		t.Fatal(got)
	}
	disk, err := m.store.loadRun(c.ID, first.ID)
	if err != nil || disk.Phase != "finished" {
		t.Fatal(disk, err)
	}
}

func TestRunAndRoundOutputLimitsFailOnlyTheProducingInstance(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "instance", true: "round"}[shared], func(t *testing.T) {
			_, m, b, _, c := managedFixture(t)
			c.AllowConcurrent = true
			first, a := startInstance(t, m, b, c)
			second, z := startInstance(t, m, b, c)
			internal := m.runs[first.ID]
			internal.mu.Lock()
			if shared {
				internal.group.bytes.Store(MaxRoundLogBytes)
			} else {
				internal.logBytes = MaxRunLogBytes
			}
			internal.mu.Unlock()
			io.WriteString(a.outWriter, "over budget")
			failed := finishedInstance(t, m, c.ID, first.ID)
			if failed.Result != "failed" || failed.Reason != "log_limit" || failed.LogComplete || failed.Nodes[1].Reason != "log_limit" {
				t.Fatal(failed)
			}
			if got := m.Run(c.ID, second.ID); got.Reason != "" || got.Phase == "finished" {
				t.Fatal("sibling stopped without output", got)
			}
			if shared {
				if _, err := m.Start(c); err == nil {
					t.Fatal("accepted into exhausted round")
				}
				io.WriteString(z.outWriter, "also over budget")
			} else {
				io.WriteString(z.outWriter, "still alive")
				z.complete()
			}
			finished := finishedInstance(t, m, c.ID, second.ID)
			if shared && finished.Reason != "log_limit" || !shared && finished.Result != "succeeded" {
				t.Fatal(finished)
			}
		})
	}
}

func TestRetainedRunLimitAndCompactSummaryBudget(t *testing.T) {
	_, m, b, _, c := managedFixture(t)
	c.AllowConcurrent = true
	anchor, a := startInstance(t, m, b, c)
	for range MaxRoundRuns - 1 {
		run, e := startInstance(t, m, b, c)
		e.complete()
		finishedInstance(t, m, c.ID, run.ID)
	}
	if _, err := m.Start(c); err == nil {
		t.Fatal("retained run cap not enforced")
	}
	list := m.Runs(c.ID)
	encoded, err := json.Marshal(list)
	if err != nil || len(encoded) > 96*1024 || len(list.Runs) != MaxRoundRuns {
		t.Fatal(len(encoded), err)
	}
	for _, run := range list.Runs {
		if len(run.Nodes) != 0 || run.Command.Graph != nil {
			t.Fatal("summary contains full graph")
		}
	}
	a.complete()
	finishedInstance(t, m, c.ID, anchor.ID)
	newest, e := startInstance(t, m, b, c)
	e.complete()
	finishedInstance(t, m, c.ID, newest.ID)
	if len(m.Runs(c.ID).Runs) != 1 {
		t.Fatal("retention cap did not reset with new round")
	}
}

func TestRecoveryRetainsAllPublishedInstancesAndBlocksNewNodes(t *testing.T) {
	home := t.TempDir()
	b := &fakeBackend{created: make(chan *fakeExecution, 8)}
	store := runStore{root: filepath.Join(home, "command-runs")}
	c := testCommand(t, "recover_many")
	c.ID = model.ID()
	c.AllowConcurrent = true
	ids := []string{model.ID(), model.ID()}
	for i, id := range ids {
		run := &managedRun{record: RunRecord{Version: 1, ID: id, RoundID: ids[0], Sequence: uint64(i + 1), Command: c, Phase: "running", LogComplete: true}}
		initializeGraph(run)
		run.record.Nodes[1].Phase = "running"
		run.record.Nodes[1].RunID = model.ID()
		f, err := store.prepare(run.record)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if err := store.publish(c.ID, runIndex{Version: 1, RoundID: ids[0], Latest: ids[1], Sequence: 2, IDs: ids}); err != nil {
		t.Fatal(err)
	}
	// Preparation without publication is not an accepted execution.
	orphan := RunRecord{Version: 1, ID: model.ID(), Command: c}
	f, err := store.prepare(orphan)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	m := NewRunManager(home, b, nil)
	t.Cleanup(func() {
		b.empty.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		m.Close(ctx)
	})
	if m.loadErr != nil || m.Summary(c.ID).Active != 2 || m.Current(c.ID).ID != ids[1] {
		t.Fatal(m.loadErr, m.Runs(c.ID))
	}
	if _, err := os.Stat(store.recordPath(c.ID, orphan.ID)); !os.IsNotExist(err) {
		t.Fatal("unpublished preparation not collected", err)
	}
	if _, err = m.Start(c); err == nil {
		t.Fatal("same command accepted while recovery uncertain")
	}
	other := testCommand(t, "waiting")
	other.ID = model.ID()
	accepted, err := m.Start(other)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if b.count.Load() != 0 {
		t.Fatal("new node started before old scopes were reconciled")
	}
	b.empty.Store(true)
	for _, id := range ids {
		if got := finishedInstance(t, m, c.ID, id); got.Reason != "unexpected_shutdown" || got.Result != "interrupted" {
			t.Fatal(got)
		}
	}
	select {
	case e := <-b.created:
		e.complete()
	case <-time.After(3 * time.Second):
		t.Fatal("new node did not resume")
	}
	finishedInstance(t, m, other.ID, accepted.ID)
	if len(m.Runs(c.ID).Runs) != 2 {
		t.Fatal("recovery discarded older retained member")
	}
}

func TestConcurrentFailureDoesNotAbortSiblingGraph(t *testing.T) {
	_, m, b, _, c := managedFixture(t)
	c.AllowConcurrent = true
	older, a := startInstance(t, m, b, c)
	latest, z := startInstance(t, m, b, c)
	a.code = 17
	a.complete()
	failed := finishedInstance(t, m, c.ID, older.ID)
	if failed.Result != "failed" || m.Run(c.ID, latest.ID).Reason != "" {
		t.Fatal("failure escaped instance", failed)
	}
	io.WriteString(z.outWriter, strings.Repeat("success", 20))
	z.complete()
	finishedInstance(t, m, c.ID, latest.ID)
	summary := m.Summary(c.ID)
	if summary.Result != "succeeded" || summary.Failed != 1 || summary.Active != 0 {
		t.Fatal("latest success hid earlier failure", summary)
	}
}
