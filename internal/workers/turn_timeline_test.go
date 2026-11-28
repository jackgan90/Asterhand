package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

// steppingClock returns strictly increasing times so start-time ordering in
// fast tests never depends on wall-clock resolution.
func steppingClock(base time.Time, step time.Duration) func() time.Time {
	var n atomic.Int64
	return func() time.Time { return base.Add(time.Duration(n.Add(1)) * step) }
}

func sendAndWait(t *testing.T, svc *Service, wsID, workerID, opID, prompt string) Turn {
	t.Helper()
	tr, err := svc.Send(context.Background(), SendInput{OperationID: opID, WorkerID: workerID, Prompt: prompt})
	if err != nil {
		t.Fatalf("send %s: %v", opID, err)
	}
	done, _, _, err := svc.Wait(context.Background(), tr.ID, wsID, 5*time.Second)
	if err != nil || done.State != TurnCompleted {
		t.Fatalf("turn %s did not complete: %+v err=%v", opID, done, err)
	}
	return done
}

func TestTurnTimelineSequencePagingPromptAndSummary(t *testing.T) {
	svc, _, ws := setupTestService(t, &fakeAdapter{})
	svc.now = steppingClock(time.Now().UTC().Add(-time.Hour), 10*time.Millisecond)
	w, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "timeline"})
	if err != nil {
		t.Fatal(err)
	}

	longPrompt := "第一行：审查  协作\n\n" + strings.Repeat("中文任务说明与上下文。", 3500) // ~113 KiB, multi-line
	var sent []Turn
	for i := 1; i <= 25; i++ {
		prompt := "task " + strconv.Itoa(i)
		if i == 3 {
			prompt = longPrompt
		}
		sent = append(sent, sendAndWait(t, svc, ws.ID, w.ID, "op_tl_"+strconv.Itoa(i), prompt))
	}
	for i, tr := range sent {
		if tr.TurnSeq != i+1 {
			t.Fatalf("turn %d has seq %d", i+1, tr.TurnSeq)
		}
	}

	long := sent[2]
	if long.PromptBytes != len(longPrompt) || strings.Contains(long.PromptPreview, "\n") ||
		len(long.PromptPreview) > promptPreviewMaxBytes || !strings.HasSuffix(long.PromptPreview, "…") ||
		!strings.HasPrefix(long.PromptPreview, "第一行：审查 协作 中文") || !utf8.ValidString(long.PromptPreview) {
		t.Fatalf("unexpected prompt preview %q bytes=%d", long.PromptPreview, long.PromptBytes)
	}

	// An older turn updated late (e.g. reconciled) must keep its timeline position.
	late, err := svc.store.GetTurn(sent[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	late.UpdatedAt = time.Now().UTC().Add(time.Hour)
	if err := svc.store.SaveTurn(late); err != nil {
		t.Fatal(err)
	}

	_, recent, err := svc.Get(w.ID, ws.ID)
	if err != nil || len(recent) != 10 || recent[0].TurnSeq != 25 || recent[9].TurnSeq != 16 {
		t.Fatalf("recent turns not ordered by start: len=%d err=%v", len(recent), err)
	}

	var seqs []int
	after := ""
	for page := 0; page < 5; page++ {
		items, hasMore, next, err := svc.ListTurns(w.ID, ws.ID, after, 10, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			seqs = append(seqs, it.TurnSeq)
		}
		if !hasMore {
			if next != "" {
				t.Fatalf("last page must not return a cursor, got %q", next)
			}
			break
		}
		after = next
	}
	if len(seqs) != 25 {
		t.Fatalf("expected 25 turns across pages, got %v", seqs)
	}
	for i, s := range seqs {
		if s != 25-i {
			t.Fatalf("timeline order broken at %d: %v", i, seqs)
		}
	}

	// Full prompt pages back exactly with UTF-8 aligned cursors.
	var b strings.Builder
	offset := 0
	for {
		page, err := svc.ReadPrompt(long.ID, ws.ID, offset, 10000)
		if err != nil {
			t.Fatal(err)
		}
		if page.Part != "prompt" || page.TotalBytes != len(longPrompt) {
			t.Fatalf("unexpected prompt page %+v", page)
		}
		b.WriteString(page.Text)
		if page.Complete {
			break
		}
		offset, _ = strconv.Atoi(page.NextCursor)
	}
	if b.String() != longPrompt {
		t.Fatalf("reassembled prompt mismatch: got %d bytes want %d", b.Len(), len(longPrompt))
	}
	if _, err := svc.ReadPrompt(long.ID, ws.ID, len("第")-1, 100); err == nil {
		t.Fatal("expected invalid_cursor for a mid-rune prompt offset")
	}

	sum, err := svc.TurnSummary(w)
	if err != nil {
		t.Fatal(err)
	}
	if sum.TurnCount != 25 || sum.StateCounts[TurnCompleted] != 25 || sum.AttentionCount != 0 || sum.Partial ||
		sum.FirstStartedAt == nil || sum.LastFinishedAt == nil || sum.WallSeconds < 0 {
		t.Fatalf("unexpected summary %+v", sum)
	}

	if items, _, _, err := svc.ListTurns(w.ID, ws.ID, "", 10, TurnFilterAttention); err != nil || len(items) != 0 {
		t.Fatalf("attention filter should be empty, got %d err=%v", len(items), err)
	}
	if _, _, _, err := svc.ListTurns(w.ID, ws.ID, "", 10, "bogus"); err == nil {
		t.Fatal("expected invalid filter error")
	}
	if _, _, _, err := svc.ListTurns(w.ID, ws.ID, "garbage", 10, ""); err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Fatalf("expected invalid cursor error, got %v", err)
	}
}

func TestTurnTimelineSummaryDurationsAndAttention(t *testing.T) {
	svc, _, ws := setupTestService(t, &fakeAdapter{})
	w, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "summary"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-2 * time.Hour)
	fin1 := base.Add(90 * time.Second)
	fin2 := base.Add(10*time.Minute + 30*time.Second)
	turns := []Turn{
		{ID: "turn_s1", WorkerID: w.ID, WorkspaceID: w.WorkspaceID, TurnSeq: 1, State: TurnCompleted, StartedAt: base, UpdatedAt: fin1, FinishedAt: &fin1},
		{ID: "turn_s2", WorkerID: w.ID, WorkspaceID: w.WorkspaceID, TurnSeq: 2, State: TurnFailed, StartedAt: base.Add(10 * time.Minute), UpdatedAt: fin2, FinishedAt: &fin2},
		{ID: "turn_s3", WorkerID: w.ID, WorkspaceID: w.WorkspaceID, TurnSeq: 4, State: TurnReconcile, StartedAt: base.Add(20 * time.Minute), UpdatedAt: base.Add(21 * time.Minute)},
	}
	for _, tr := range turns {
		if err := svc.store.SaveTurn(tr); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := svc.TurnSummary(w)
	if err != nil {
		t.Fatal(err)
	}
	// 90s + 30s + 60s (unfinished reconciliation counts to last update).
	if sum.TurnCount != 3 || sum.TurnsSeconds != 180 || sum.AttentionCount != 2 || !sum.Partial {
		t.Fatalf("unexpected summary %+v", sum)
	}
	items, _, _, err := svc.ListTurns(w.ID, ws.ID, "", 10, TurnFilterAttention)
	if err != nil || len(items) != 2 || items[0].TurnID != "turn_s3" || items[1].TurnID != "turn_s2" {
		t.Fatalf("unexpected attention items %+v err=%v", items, err)
	}
	if next, err := svc.store.NextTurnSeq(w.ID, 0); err != nil || next != 5 {
		t.Fatalf("next seq must continue after highest stored seq, got %d err=%v", next, err)
	}
}

func TestTurnTimelineMigratesLegacyDatabase(t *testing.T) {
	home := t.TempDir()
	if err := openLegacyStoreFixture(t, home); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var version int
	if err := st.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != DBSchemaVersion {
		t.Fatalf("expected upgraded user_version=%d, got %d err=%v", DBSchemaVersion, version, err)
	}
	items, hasMore, err := st.ListTurnsPage("w_legacy", "", 10, false)
	if err != nil || hasMore || len(items) != 3 || items[0].ID != "turn_l3" || items[2].ID != "turn_l1" {
		t.Fatalf("legacy turns not backfilled into timeline order: %+v err=%v", items, err)
	}
	rank, err := st.TurnRank("w_legacy", items[1].StartedAt, items[1].ID)
	if err != nil || rank != 2 {
		t.Fatalf("expected derived rank 2, got %d err=%v", rank, err)
	}
	if next, err := st.NextTurnSeq("w_legacy", 0); err != nil || next != 4 {
		t.Fatalf("new turns must number after legacy turns, got %d err=%v", next, err)
	}
	var finished int64
	if err := st.db.QueryRow(`SELECT finished_at FROM turns WHERE turn_id='turn_l1'`).Scan(&finished); err != nil || finished == 0 {
		t.Fatalf("finished_at not backfilled: %d err=%v", finished, err)
	}

	// Reopening an already-v2 database succeeds cleanly; newer schema versions are rejected.
	st.Close()
	st2, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st2.db.Exec(`PRAGMA user_version = 99`); err != nil {
		st2.Close()
		t.Fatal(err)
	}
	st2.Close()
	if _, err := OpenStore(home); err == nil {
		t.Fatal("expected newer schema version 99 to be rejected")
	}
}

// openLegacyStoreFixture writes a v1 database in the pre-timeline layout (no
// timeline columns, no prompt or pruned-history tables) with three legacy turns.
func openLegacyStoreFixture(t *testing.T, home string) error {
	t.Helper()
	st, err := OpenStore(home)
	if err != nil {
		return err
	}
	st.Close()
	db, err := sql.Open("sqlite", filepath.Join(home, "workers", "workers.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE turns; DROP TABLE turn_prompts; DROP TABLE worker_history_pruned;
	CREATE TABLE turns(
		turn_id TEXT PRIMARY KEY, worker_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
		operation_id TEXT NOT NULL, client_correlation_id TEXT NOT NULL, state TEXT NOT NULL,
		body BLOB NOT NULL, updated_at INTEGER NOT NULL);
	PRAGMA user_version = 1;`); err != nil {
		return err
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i := 1; i <= 3; i++ {
		start := base.Add(time.Duration(i) * time.Minute)
		fin := start.Add(time.Second)
		tr := Turn{ID: "turn_l" + strconv.Itoa(i), WorkerID: "w_legacy", WorkspaceID: "ws", State: TurnCompleted, StartedAt: start, UpdatedAt: base.Add(time.Duration(10-i) * time.Minute), FinishedAt: &fin}
		body, _ := json.Marshal(tr)
		if _, err := db.Exec(`INSERT INTO turns VALUES(?,?,?,?,?,?,?,?)`, tr.ID, tr.WorkerID, tr.WorkspaceID, "op"+tr.ID, "corr"+tr.ID, tr.State, body, tr.UpdatedAt.UnixMilli()); err != nil {
			return err
		}
	}
	return nil
}

func TestTurnPromptCountsTowardDetailBudgetAndRetention(t *testing.T) {
	svc, _, ws := setupTestService(t, &fakeAdapter{})
	w, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := strings.Repeat("p", 5000)
	tr := sendAndWait(t, svc, ws.ID, w.ID, "op_budget", prompt)
	svc.store.mu.Lock()
	usage, err := svc.store.cachedDetailUsageLocked()
	if err != nil {
		svc.store.mu.Unlock()
		t.Fatal(err)
	}
	recount, err := svc.store.detailUsageLocked()
	svc.store.mu.Unlock()
	if err != nil || usage != recount || usage < int64(len(prompt)) {
		t.Fatalf("detail usage must include prompt bytes: cached=%d recount=%d err=%v", usage, recount, err)
	}

	svc.store.PruneDetailsAndCap(time.Now().UTC().Add(DetailRetention + time.Hour))
	page, err := svc.ReadPrompt(tr.ID, ws.ID, 0, 1000)
	if err != nil || !page.Expired || page.Text != "" {
		t.Fatalf("prompt must expire with turn details: %+v err=%v", page, err)
	}
	got, err := svc.GetTurn(tr.ID, ws.ID)
	if err != nil || got.PromptPreview == "" || got.PromptBytes != len(prompt) {
		t.Fatalf("prompt preview must survive detail retention: %+v err=%v", got, err)
	}
}

func TestTurnTimelineQueriesUseIndexAt500Turns(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	base := time.Now().UTC().Add(-48 * time.Hour)
	for i := 1; i <= 500; i++ {
		start := base.Add(time.Duration(i) * time.Minute)
		fin := start.Add(30 * time.Second)
		tr := Turn{ID: "turn_p" + strconv.Itoa(i), WorkerID: "w_perf", WorkspaceID: "ws", TurnSeq: i, State: TurnCompleted,
			StartedAt: start, UpdatedAt: fin, FinishedAt: &fin, PromptPreview: strings.Repeat("任", 60)}
		if err := st.SaveTurn(tr); err != nil {
			t.Fatal(err)
		}
	}
	began := time.Now()
	page, hasMore, err := st.ListTurnsPage("w_perf", "", 20, false)
	if err != nil || !hasMore || len(page) != 20 || page[0].TurnSeq != 500 || page[19].TurnSeq != 481 {
		t.Fatalf("unexpected first page: len=%d hasMore=%v err=%v", len(page), hasMore, err)
	}
	last := page[len(page)-1]
	deep, _, err := st.ListTurnsPage("w_perf", EncodeTurnListAfter(last.StartedAt, last.TurnSeq, last.ID), 20, false)
	if err != nil || len(deep) != 20 || deep[0].TurnSeq != 480 {
		t.Fatalf("unexpected second page: len=%d err=%v", len(deep), err)
	}
	agg, err := st.AggregateTurns("w_perf", time.Now().UnixMilli())
	if err != nil || len(agg) != 1 || agg[0].Count != 500 || agg[0].TotalMillis != 500*30*1000 {
		t.Fatalf("unexpected aggregate: %+v err=%v", agg, err)
	}
	elapsed := time.Since(began)
	t.Logf("500-turn page+page+aggregate took %s", elapsed)
	if elapsed > 2*time.Second {
		t.Fatalf("timeline queries too slow at 500 turns: %s", elapsed)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	for _, tc := range []struct {
		name      string
		sql       string
		args      []any
		wantRange bool
	}{
		{
			name: "head",
			sql:  `EXPLAIN QUERY PLAN SELECT turn_seq, body FROM turns WHERE worker_id=?` + turnTimelineOrder + ` LIMIT 21`,
			args: []any{"w_perf"},
		},
		{
			name:      "deep_cursor",
			sql:       `EXPLAIN QUERY PLAN SELECT turn_seq, body FROM turns WHERE worker_id=? AND (started_at, turn_seq, turn_id) < (?, ?, ?)` + turnTimelineOrder + ` LIMIT 21`,
			args:      []any{"w_perf", timeMillis(last.StartedAt), last.TurnSeq, last.ID},
			wantRange: true,
		},
	} {
		rows, err := st.db.Query(tc.sql, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			plan.WriteString(detail + "\n")
		}
		rows.Close()
		got := plan.String()
		if !strings.Contains(got, "idx_turns_worker_started") || strings.Contains(got, "TEMP B-TREE") {
			t.Fatalf("%s query must use idx_turns_worker_started without temp sort:\n%s", tc.name, got)
		}
		if tc.wantRange && !strings.Contains(got, "started_at") {
			t.Fatalf("deep cursor query must seek on started_at in idx_turns_worker_started, got:\n%s", got)
		}
	}
}

func TestTurnTimelineAuditRegressions(t *testing.T) {
	svc, _, ws := setupTestService(t, &fakeAdapter{})
	w, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "audit-reg"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()

	// 1. Pruning an unnumbered legacy turn marks summary.Partial=true and advances NextTurnSeq.
	for i, ageDays := range []int{31, 1} {
		at := now.Add(-time.Duration(ageDays) * 24 * time.Hour)
		fin := at.Add(10 * time.Second)
		tn := Turn{ID: "turn_leg_" + strconv.Itoa(i), WorkerID: w.ID, WorkspaceID: ws.ID, State: TurnCompleted, StartedAt: at, UpdatedAt: fin, FinishedAt: &fin}
		if err := svc.store.SaveTurn(tn); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.store.DeleteExpiredTerminalTurns(now, nil); err != nil {
		t.Fatal(err)
	}
	sum, err := svc.TurnSummary(w)
	if err != nil || sum.TurnCount != 1 || !sum.Partial {
		t.Fatalf("expected TurnCount=1 Partial=true after pruning legacy turn, got %+v err=%v", sum, err)
	}

	// 2. DeleteExpiredTerminalTurns explicitly removes associated turn_prompts and frees detail budget in the same transaction.
	sent := sendAndWait(t, svc, ws.ID, w.ID, "op_orphan_check", "prompt that must be deleted with expired turn summary")
	if sent.TurnSeq != 3 {
		t.Fatalf("expected NextTurnSeq=3 after 1 retained + 1 pruned legacy turn, got %d", sent.TurnSeq)
	}
	svc.store.mu.Lock()
	_, err = svc.store.db.Exec(`UPDATE turns SET updated_at=? WHERE turn_id=?`, now.Add(-31*24*time.Hour).UnixMilli(), sent.ID)
	svc.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.DeleteExpiredTerminalTurns(now, nil); err != nil {
		t.Fatal(err)
	}
	if _, length, has, err := svc.store.ReadPromptRange(sent.ID, 0, 100); err != nil || has || length != 0 {
		t.Fatalf("prompt must be deleted when turn summary expires: has=%v len=%d err=%v", has, length, err)
	}
	svc.store.mu.Lock()
	usage, err := svc.store.cachedDetailUsageLocked()
	svc.store.mu.Unlock()
	if err != nil || usage != 0 {
		t.Fatalf("detail usage after deleting turn must be 0, got %d err=%v", usage, err)
	}

	// 3. Unfinished reconciliation_required turn freezes elapsed_seconds at UpdatedAt across item, GetTurn, and summary.
	reconStart := now.Add(-2 * time.Hour)
	reconUpdate := reconStart.Add(time.Minute)
	recon := Turn{ID: "turn_recon_freeze", WorkerID: w.ID, WorkspaceID: ws.ID, TurnSeq: 4, State: TurnReconcile, StartedAt: reconStart, UpdatedAt: reconUpdate}
	if err := svc.store.SaveTurn(recon); err != nil {
		t.Fatal(err)
	}
	item := NewTurnListItem(recon, now)
	gotTurn, err := svc.GetTurn(recon.ID, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	ag, err := svc.store.AggregateTurns(w.ID, now.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	var reconAggSec int64
	for _, r := range ag {
		if r.State == TurnReconcile {
			reconAggSec = r.TotalMillis / 1000
		}
	}
	if item.ElapsedSeconds != 60 || gotTurn.ElapsedSeconds != 60 || reconAggSec != 60 {
		t.Fatalf("reconcile elapsed mismatch: item=%d getTurn=%d agg=%d", item.ElapsedSeconds, gotTurn.ElapsedSeconds, reconAggSec)
	}

	// 4. Changed-file counts handle >32 files without cap truncation, avoid MM double counting, and flag ambiguous legacy bounds.
	if n, lb := changedFileCount(&VCSSnapshot{VCS: "git", ChangedCount: 40, UntrackedCount: 40, ChangedFiles: make([]string, 32)}); n != 40 || lb {
		t.Fatalf("exact ChangedCount=40 failed: n=%d lb=%v", n, lb)
	}
	if n, lb := changedFileCount(&VCSSnapshot{VCS: "git", UnstagedCount: 40, ChangedFiles: make([]string, 32)}); n != 40 || lb {
		t.Fatalf("legacy 40 unstaged tracked files failed: n=%d lb=%v", n, lb)
	}
	if n, lb := changedFileCount(&VCSSnapshot{VCS: "git", UntrackedCount: 32, ChangedFiles: make([]string, 32)}); n != 32 || lb {
		t.Fatalf("legacy 32 untracked files within sample cap failed: n=%d lb=%v", n, lb)
	}
	if n, lb := changedFileCount(&VCSSnapshot{VCS: "git", UntrackedCount: 33, ChangedFiles: make([]string, 32)}); n != 33 || !lb {
		t.Fatalf("legacy truncated sample with untracked entries must set lowerBound=true: n=%d lb=%v", n, lb)
	}
	if n, lb := changedFileCount(&VCSSnapshot{VCS: "git", ChangedCount: 1, StagedCount: 1, UnstagedCount: 1, ChangedFiles: []string{"a.go"}}); n != 1 || lb {
		t.Fatalf("MM single file double counted: n=%d lb=%v", n, lb)
	}
	if n, lb := changedFileCount(&VCSSnapshot{VCS: "git", StagedCount: 20, UnstagedCount: 25, ChangedFiles: make([]string, 32)}); n != 32 || !lb {
		t.Fatalf("ambiguous legacy snapshot must set lowerBound=true: n=%d lb=%v", n, lb)
	}
	if n, lb := changedFileCount(&VCSSnapshot{VCS: "git", ChangedCount: 1, UntrackedCount: 1, ChangedFiles: []string{`"new dir/"`}}); n != 1 || !lb {
		t.Fatalf("quoted collapsed dir must set lowerBound=true: n=%d lb=%v", n, lb)
	}
	if _, gitErr := exec.LookPath("git"); gitErr == nil {
		repo := t.TempDir()
		if out, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, string(out))
		}
		sub := filepath.Join(repo, "z-dir")
		if err := os.Mkdir(sub, 0700); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 72; i++ {
			baseDir := repo
			if i >= 32 {
				baseDir = sub
			}
			if err := os.WriteFile(filepath.Join(baseDir, fmt.Sprintf("a-%02d", i)), []byte("f"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		snap := CaptureVCSSnapshot(context.Background(), repo)
		if n, lb := changedFileCount(snap); n != 33 || !lb || !snap.ChangedCountLowerBound {
			t.Fatalf("fresh snapshot with 32 root files + collapsed z-dir (40 files) must report 33 lowerBound=true: snap=%+v n=%d lb=%v", snap, n, lb)
		}
		legacyV1 := VCSSnapshot{
			VCS:            snap.VCS,
			ChangedFiles:   snap.ChangedFiles,
			StagedCount:    snap.StagedCount,
			UnstagedCount:  snap.UnstagedCount,
			UntrackedCount: snap.UntrackedCount,
		}
		if n, lb := changedFileCount(&legacyV1); n != 33 || !lb {
			t.Fatalf("legacy v1 snapshot where 32-path sample stops before collapsed z-dir must report lowerBound=true: n=%d lb=%v", n, lb)
		}
	}

	// 5. Batch TurnRanks preserves global chronological numbers under attention filter in a single covering-index query.
	wLegacy, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "legacy-ranks"})
	if err != nil {
		t.Fatal(err)
	}
	base := now.Add(-5 * time.Hour)
	for i := 1; i <= 30; i++ {
		st := TurnCompleted
		if i == 7 || i == 22 {
			st = TurnFailed
		}
		start := base.Add(time.Duration(i) * time.Minute)
		fin := start.Add(5 * time.Second)
		if err := svc.store.SaveTurn(Turn{
			ID: fmt.Sprintf("turn_lr_%02d", i), WorkerID: wLegacy.ID, WorkspaceID: ws.ID,
			State: st, StartedAt: start, UpdatedAt: fin, FinishedAt: &fin,
		}); err != nil {
			t.Fatal(err)
		}
	}
	attItems, _, _, err := svc.ListTurns(wLegacy.ID, ws.ID, "", 20, TurnFilterAttention)
	if err != nil || len(attItems) != 2 || attItems[0].TurnSeq != 22 || !attItems[0].TurnSeqDerived || attItems[1].TurnSeq != 7 || !attItems[1].TurnSeqDerived {
		t.Fatalf("attention filter must preserve global derived turn numbers (22 and 7), got %+v err=%v", attItems, err)
	}
}
