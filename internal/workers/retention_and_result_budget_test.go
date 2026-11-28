package workers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func makeHTMLTableReport(rows int) string {
	var b strings.Builder
	b.WriteString("<table><thead><tr><th>Module</th><th>Finding</th><th>Diff &amp; Notes</th></tr></thead><tbody>\n")
	for i := 0; i < rows; i++ {
		fmt.Fprintf(&b, "<tr><td>mod_%03d</td><td>&lt;tag-%03d&gt; &amp; 核查结论</td><td><code>if a &lt; b &amp;&amp; c &gt; d { fix(%d) }</code></td></tr>\n", i, i, i)
	}
	b.WriteString("</tbody></table>")
	return strings.TrimSpace(b.String())
}

func TestReadResultEnforcesSerializedJSONBudgetAndExactUTF8Cursors(t *testing.T) {
	reports := []struct {
		name     string
		body     string
		maxBytes int
	}{
		{name: "html_table_default_24k", body: makeHTMLTableReport(460), maxBytes: 24 << 10},
		{name: "html_table_max_32k", body: makeHTMLTableReport(460), maxBytes: 32 << 10},
		{name: "extreme_6x_escape_utf8", body: strings.Repeat("<>&\"中", 4500), maxBytes: 32 << 10},
	}

	idx := 0
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			cur := reports[idx]
			idx++
			ch := make(chan TurnOutcome, 1)
			ch <- TurnOutcome{
				State:        TurnCompleted,
				NativeTurnID: "nt_res_" + cur.name,
				Items:        []NativeItem{{ItemID: "f_" + cur.name, Type: "agentMessage", Phase: "final_answer", Text: cur.body}},
			}
			return "nt_res_" + cur.name, ch, nil
		},
	}
	svc, _, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()

	w, err := svc.Create(context.Background(), CreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "res-budget", AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	for i, tc := range reports {
		t.Run(tc.name, func(t *testing.T) {
			turn, err := svc.Send(context.Background(), SendInput{
				OperationID: fmt.Sprintf("op_res_%d", i), WorkerID: w.ID, Prompt: tc.name,
			})
			if err != nil {
				t.Fatal(err)
			}
			wt, _, _, err := svc.Wait(context.Background(), turn.ID, ws.ID, 5*time.Second)
			if err != nil || wt.State != TurnCompleted {
				t.Fatalf("turn did not complete: state=%s err=%v", wt.State, err)
			}

			var out strings.Builder
			offset := 0
			pages := 0
			for {
				pages++
				if pages > 40 {
					t.Fatalf("pagination did not terminate after %d pages (offset=%d)", pages, offset)
				}
				page, err := svc.ReadResult(turn.ID, ws.ID, offset, tc.maxBytes)
				if err != nil {
					t.Fatalf("ReadResult(offset=%d): %v", offset, err)
				}
				if sz := resultPageJSONSize(page); sz > resultPageMaxPayloadJSONBytes {
					t.Fatalf("page %d JSON size %d exceeded %d-byte budget", pages, sz, resultPageMaxPayloadJSONBytes)
				}
				if len(page.Text) == 0 {
					t.Fatalf("page %d returned empty text at offset %d", pages, offset)
				}
				out.WriteString(page.Text)
				nextOff := offset + len(page.Text)
				if nextOff < len(tc.body) {
					if page.Complete {
						t.Fatalf("page %d reported complete=true with %d/%d bytes delivered", pages, nextOff, len(tc.body))
					}
					if page.NextCursor != strconv.Itoa(nextOff) {
						t.Fatalf("page %d next_cursor=%q, want %d", pages, page.NextCursor, nextOff)
					}
					offset = nextOff
				} else {
					if !page.Complete || page.NextCursor != "" {
						t.Fatalf("final page expected complete=true and empty next_cursor, got complete=%v cursor=%q", page.Complete, page.NextCursor)
					}
					break
				}
			}
			if out.String() != tc.body {
				t.Fatalf("reconstructed body mismatch: got %d bytes, want %d bytes", out.Len(), len(tc.body))
			}
		})
	}
}

func seedAgedRetentionFixtures(t *testing.T, st *Store, now time.Time) {
	t.Helper()
	if err := st.SaveWorker(Worker{
		ID: "wkr_ret", WorkspaceID: "ws_ret", HarnessID: harnesses.HarnessCodex,
		AccessMode: harnesses.AccessReadOnly, State: WorkerIdle, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		id    string
		state string
		age   time.Duration
		body  string
	}{
		{"turn_term_6d", TurnCompleted, 6 * 24 * time.Hour, "result-from-6-days-ago-ok!!"},
		{"turn_term_8d", TurnCompleted, 8 * 24 * time.Hour, "result-from-8-days-ago-old!"},
		{"turn_term_31d", TurnCompleted, 31 * 24 * time.Hour, "result-from-31-days-ago-bye"},
		{"turn_reconcile_31d", TurnReconcile, 31 * 24 * time.Hour, "reconcile-evidence-keep-31d"},
		{"turn_active_10d", TurnWaitingApproval, 10 * 24 * time.Hour, "waiting-approval-keep-10d!!"},
	}
	for _, f := range fixtures {
		ts := now.Add(-f.age)
		turn := Turn{
			ID: f.id, WorkerID: "wkr_ret", WorkspaceID: "ws_ret", HarnessID: harnesses.HarnessCodex,
			OperationID: "op_" + f.id, ClientCorrelationID: "corr_" + f.id, State: f.state,
			AnswerAvailable: f.state == TurnCompleted, AnswerSource: AnswerSourceFinal,
			ResultBytes: len(f.body), StartedAt: ts, UpdatedAt: ts,
		}
		if err := st.SaveTurn(turn); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.SaveResultText(f.id, f.body, ts); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AppendLog(f.id, LogEntry{Timestamp: ts, Kind: "progress", Summary: "log-" + f.id}); err != nil {
			t.Fatal(err)
		}
	}
}

func assertRetentionOutcomes(t *testing.T, svc *Service, mode string) {
	t.Helper()
	// 6d terminal: summary + result + logs all retained.
	p6, err := svc.ReadResult("turn_term_6d", "ws_ret", 0, 24<<10)
	if err != nil || p6.Expired || len(p6.Text) != 27 {
		t.Fatalf("%s: 6d result expected intact 27 bytes, got page=%+v err=%v", mode, p6, err)
	}
	if _, logs6, _, err := svc.ReadLogs("turn_term_6d", "ws_ret", 0, 10); err != nil || len(logs6) != 1 {
		t.Fatalf("%s: 6d logs expected 1 entry, got %d err=%v", mode, len(logs6), err)
	}

	// 8d terminal: summary retained, result + logs expired.
	if _, err := svc.GetTurn("turn_term_8d", "ws_ret"); err != nil {
		t.Fatalf("%s: 8d summary should remain readable: %v", mode, err)
	}
	p8, err := svc.ReadResult("turn_term_8d", "ws_ret", 0, 24<<10)
	if err != nil || !p8.Expired || p8.Text != "" {
		t.Fatalf("%s: 8d result expected expired, got page=%+v err=%v", mode, p8, err)
	}
	if _, logs8, _, err := svc.ReadLogs("turn_term_8d", "ws_ret", 0, 10); err != nil || len(logs8) != 0 {
		t.Fatalf("%s: 8d logs expected pruned, got %d err=%v", mode, len(logs8), err)
	}

	// 31d terminal: summary, result, and logs all evicted.
	if _, err := svc.GetTurn("turn_term_31d", "ws_ret"); err == nil {
		t.Fatalf("%s: 31d terminal summary should have been evicted", mode)
	}
	if _, ok, _ := svc.store.ReadResultText("turn_term_31d"); ok {
		t.Fatalf("%s: 31d terminal result should have been evicted", mode)
	}
	if logs31, _, _ := svc.store.ReadLogs("turn_term_31d", 0, 10); len(logs31) != 0 {
		t.Fatalf("%s: 31d terminal logs should have been evicted", mode)
	}

	// Protected turns (31d reconciliation_required & 10d waiting/reconciled) must survive completely.
	for _, id := range []string{"turn_reconcile_31d", "turn_active_10d"} {
		if _, err := svc.GetTurn(id, "ws_ret"); err != nil {
			t.Fatalf("%s: protected %s summary lost: %v", mode, id, err)
		}
		if body, ok, err := svc.store.ReadResultText(id); err != nil || !ok || len(body) != 27 {
			t.Fatalf("%s: protected %s result lost: ok=%v len=%d err=%v", mode, id, ok, len(body), err)
		}
		if logs, _, err := svc.store.ReadLogs(id, 0, 10); err != nil || len(logs) != 1 {
			t.Fatalf("%s: protected %s logs lost: len=%d err=%v", mode, id, len(logs), err)
		}
	}
}

func TestRetentionOnlineMaintenanceMatchesStartupCleanup(t *testing.T) {
	now := time.Now().UTC()

	// 1. Online maintenance path: service stays running, turns age in DB, reapIdleResidents runs.
	onlineHome := t.TempDir()
	onlineHS, err := harnesses.Open(onlineHome, "")
	if err != nil {
		t.Fatal(err)
	}
	onlineSvc, err := NewService(onlineHome, onlineHS, &fakeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = onlineSvc.Close(context.Background()) }()
	onlineSvc.now = func() time.Time { return now }
	seedAgedRetentionFixtures(t, onlineSvc.store, now)
	onlineSvc.reapIdleResidents()
	assertRetentionOutcomes(t, onlineSvc, "online-maintenance")

	// 2. Startup cleanup path: seed DB before restart, reopen NewService.
	startupHome := t.TempDir()
	st, err := OpenStore(startupHome)
	if err != nil {
		t.Fatal(err)
	}
	seedAgedRetentionFixtures(t, st, now)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	startupHS, err := harnesses.Open(startupHome, "")
	if err != nil {
		t.Fatal(err)
	}
	startupSvc, err := NewService(startupHome, startupHS, &fakeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = startupSvc.Close(context.Background()) }()
	assertRetentionOutcomes(t, startupSvc, "startup-cleanup")
}

func TestStoreDetailCapacityCapEvictsOldestTerminalAndProtectsActive(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	baseTime := time.Now().UTC().Add(-2 * time.Hour)
	chunk512K := strings.Repeat("R", 512<<10)

	// Oldest record is reconciliation_required and MUST NEVER be evicted by the 64 MiB cap.
	protID := "turn_prot_reconcile"
	if err := st.SaveTurn(Turn{
		ID: protID, WorkerID: "wkr_cap", WorkspaceID: "ws_cap", HarnessID: harnesses.HarnessCodex,
		OperationID: "op_" + protID, ClientCorrelationID: "corr_" + protID, State: TurnReconcile,
		ResultBytes: len(chunk512K), StartedAt: baseTime, UpdatedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.SaveResultText(protID, chunk512K, baseTime); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendLog(protID, LogEntry{Timestamp: baseTime, Kind: "progress", Summary: "protected-log"}); err != nil {
		t.Fatal(err)
	}

	// Write 130 terminal turns of 512 KiB each (+ logs) -> 65+ MiB written; node total must stay <= 64 MiB.
	for i := 1; i <= 130; i++ {
		ts := baseTime.Add(time.Duration(i) * time.Second)
		id := fmt.Sprintf("turn_cap_%03d", i)
		if err := st.SaveTurn(Turn{
			ID: id, WorkerID: "wkr_cap", WorkspaceID: "ws_cap", HarnessID: harnesses.HarnessCodex,
			OperationID: "op_" + id, ClientCorrelationID: "corr_" + id, State: TurnCompleted,
			AnswerAvailable: true, AnswerSource: AnswerSourceFinal,
			ResultBytes: len(chunk512K), StartedAt: ts, UpdatedAt: ts,
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.SaveResultText(id, chunk512K, ts); err != nil {
			t.Fatalf("SaveResultText(%s): %v", id, err)
		}
		if _, err := st.AppendLog(id, LogEntry{Timestamp: ts, Kind: "progress", Summary: "done-" + id}); err != nil {
			t.Fatalf("AppendLog(%s): %v", id, err)
		}
	}

	st.mu.Lock()
	used, err := st.detailUsageLocked()
	st.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if used > MaxTotalDetailBytes {
		t.Fatalf("retained detail bytes %d exceeded 64 MiB cap %d", used, MaxTotalDetailBytes)
	}

	// Protected reconciliation_required turn kept both result and log.
	if body, ok, _ := st.ReadResultText(protID); !ok || len(body) != len(chunk512K) {
		t.Fatalf("protected reconciliation turn result was evicted (ok=%v len=%d)", ok, len(body))
	}
	if logs, _, _ := st.ReadLogs(protID, 0, 10); len(logs) != 1 {
		t.Fatalf("protected reconciliation turn log was evicted (len=%d)", len(logs))
	}

	// Oldest terminal turns (#1..#3) had their details evicted while summaries stay readable.
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("turn_cap_%03d", i)
		if _, err := st.GetTurn(id); err != nil {
			t.Fatalf("evicted terminal turn %s summary should still exist: %v", id, err)
		}
		if _, ok, _ := st.ReadResultText(id); ok {
			t.Fatalf("oldest terminal turn %s result should have been evicted under 64 MiB cap", id)
		}
		if logs, _, _ := st.ReadLogs(id, 0, 10); len(logs) != 0 {
			t.Fatalf("oldest terminal turn %s logs should have been evicted under 64 MiB cap", id)
		}
	}

	// Newest terminal turn (#130) remains intact.
	if body, ok, _ := st.ReadResultText("turn_cap_130"); !ok || len(body) != len(chunk512K) {
		t.Fatalf("newest terminal turn result missing: ok=%v len=%d", ok, len(body))
	}

	// Appending additional logs on a new active turn evicts the next oldest terminal turn cleanly.
	activeID := "turn_cap_active_log"
	tsAct := baseTime.Add(300 * time.Second)
	if err := st.SaveTurn(Turn{
		ID: activeID, WorkerID: "wkr_cap", WorkspaceID: "ws_cap", HarnessID: harnesses.HarnessCodex,
		OperationID: "op_" + activeID, ClientCorrelationID: "corr_" + activeID, State: TurnRunning,
		StartedAt: tsAct, UpdatedAt: tsAct,
	}); err != nil {
		t.Fatal(err)
	}
	bigLog := strings.Repeat("L", 200<<10)
	if _, err := st.AppendLog(activeID, LogEntry{Timestamp: tsAct, Kind: "tool", Summary: bigLog}); err != nil {
		t.Fatalf("AppendLog under 64 MiB cap should evict oldest terminal turn, got err=%v", err)
	}
	st.mu.Lock()
	usedAfterLog, _ := st.detailUsageLocked()
	st.mu.Unlock()
	if usedAfterLog > MaxTotalDetailBytes {
		t.Fatalf("detail usage after AppendLog %d exceeded 64 MiB cap %d", usedAfterLog, MaxTotalDetailBytes)
	}
}

func TestStoreDetailCapacityCapRejectsWhenOnlyProtectedTurnsRemain(t *testing.T) {
	st, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now().UTC()
	chunk2M := strings.Repeat("P", MaxResultBytes) // 2 MiB each; 32 * 2 MiB == 64 MiB
	for i := 0; i < 32; i++ {
		id := fmt.Sprintf("turn_prot_%02d", i)
		if err := st.SaveTurn(Turn{
			ID: id, WorkerID: "wkr_p", WorkspaceID: "ws_p", HarnessID: harnesses.HarnessCodex,
			OperationID: "op_" + id, ClientCorrelationID: "corr_" + id, State: TurnReconcile,
			ResultBytes: len(chunk2M), StartedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.SaveResultText(id, chunk2M, now); err != nil {
			t.Fatalf("seed protected %d: %v", i, err)
		}
	}

	if _, _, err := st.SaveResultText("turn_overflow", "extra", now); automation.PublicError(err).Code != "capacity" {
		t.Fatalf("expected capacity error when 64 MiB filled by protected turns, got %v", err)
	}
	if _, err := st.AppendLog("turn_overflow", LogEntry{Timestamp: now, Kind: "progress", Summary: "x"}); automation.PublicError(err).Code != "capacity" {
		t.Fatalf("expected capacity error on AppendLog when 64 MiB filled by protected turns, got %v", err)
	}
}

func assertStoreCachedUsageMatchesSQL(t *testing.T, st *Store, label string) int64 {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	sqlSum, err := st.detailUsageLocked()
	if err != nil {
		t.Fatalf("%s: detailUsageLocked: %v", label, err)
	}
	if !st.detailBytesLoaded || st.detailBytes != sqlSum {
		t.Fatalf("%s: cached detailBytes=%d (loaded=%v) drifted from SQLite sum=%d", label, st.detailBytes, st.detailBytesLoaded, sqlSum)
	}
	return sqlSum
}

func TestStoreCachedDetailBytesExactAcrossWritesEvictionsAndReopen(t *testing.T) {
	home := t.TempDir()
	st, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	assertStoreCachedUsageMatchesSQL(t, st, "fresh-open")

	// Insert, grow-overwrite, shrink-overwrite, and log appends across multiple turns.
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("turn_hist_%02d", i)
		ts := now.Add(-time.Duration(40-i) * time.Minute)
		if err := st.SaveTurn(Turn{
			ID: id, WorkerID: "wkr_c", WorkspaceID: "ws_c", HarnessID: harnesses.HarnessCodex,
			OperationID: "op_" + id, ClientCorrelationID: "corr_" + id, State: TurnCompleted,
			StartedAt: ts, UpdatedAt: ts,
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.SaveResultText(id, strings.Repeat("a", 1024), ts); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 25; j++ {
			if _, err := st.AppendLog(id, LogEntry{Timestamp: ts, Kind: "tool", Summary: fmt.Sprintf("step %d", j)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertStoreCachedUsageMatchesSQL(t, st, "after-1000-logs")

	// Overwrite existing result larger then smaller; cache must stay exact.
	if _, _, err := st.SaveResultText("turn_hist_00", strings.Repeat("b", 4096), now); err != nil {
		t.Fatal(err)
	}
	assertStoreCachedUsageMatchesSQL(t, st, "after-grow-overwrite")
	if _, _, err := st.SaveResultText("turn_hist_00", "tiny", now); err != nil {
		t.Fatal(err)
	}
	assertStoreCachedUsageMatchesSQL(t, st, "after-shrink-overwrite")

	// Prune 8 days later: details vanish and cache updates to 0.
	st.PruneDetailsAndCap(now.Add(8 * 24 * time.Hour))
	if got := assertStoreCachedUsageMatchesSQL(t, st, "after-prune"); got != 0 {
		t.Fatalf("expected 0 detail bytes after 8d prune, got %d", got)
	}

	// Reopen store and verify OpenStore recounts accurately.
	if _, _, err := st.SaveResultText("turn_hist_01", "reopen-payload", now); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st2, err := OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	if got := assertStoreCachedUsageMatchesSQL(t, st2, "after-reopen"); got != int64(len("reopen-payload")) {
		t.Fatalf("reopened cached usage=%d, want %d", got, len("reopen-payload"))
	}
}

func TestWaitAndWaitAnySurfaceExpiredAndPropagateStoreErrors(t *testing.T) {
	const answer33 = "123456789012345678901234567890123"
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			ch := make(chan TurnOutcome, 1)
			ch <- TurnOutcome{
				State:        TurnCompleted,
				NativeTurnID: "nt_exp33",
				Items:        []NativeItem{{ItemID: "f_exp33", Type: "agentMessage", Phase: "final_answer", Text: answer33}},
			}
			return "nt_exp33", ch, nil
		},
	}
	svc, _, ws := setupTestService(t, fa)
	defer func() { _ = svc.Close(context.Background()) }()

	w, err := svc.Create(context.Background(), CreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "exp-check", AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{OperationID: "op_exp33", WorkerID: w.ID, Prompt: "short answer"})
	if err != nil {
		t.Fatal(err)
	}
	res1, err := svc.WaitSnapshot(context.Background(), turn.ID, ws.ID, WaitOptions{WaitDuration: 5 * time.Second})
	if err != nil || res1.Expired || res1.Preview != answer33 {
		t.Fatalf("expected non-expired answer before prune, got %+v err=%v", res1, err)
	}

	// Expire details past 7 days while keeping summary.
	svc.PruneRetention(time.Now().UTC().Add(8 * 24 * time.Hour))

	res2, err := svc.WaitSnapshot(context.Background(), turn.ID, ws.ID, WaitOptions{WaitDuration: 1 * time.Second})
	if err != nil {
		t.Fatalf("WaitSnapshot after expiry failed: %v", err)
	}
	if !res2.Expired || res2.Preview != "" || res2.Truncated || !res2.Turn.AnswerAvailable || res2.Turn.ResultBytes != len(answer33) {
		t.Fatalf("expected WaitSnapshot expired=true with result_bytes=%d, got %+v", len(answer33), res2)
	}
	items, _, err := svc.WaitAny(context.Background(), []string{turn.ID}, ws.ID, 1*time.Second)
	if err != nil || len(items) != 1 {
		t.Fatalf("WaitAny after expiry failed: items=%+v err=%v", items, err)
	}
	if !items[0].Expired || items[0].AnswerPreview != "" || items[0].Truncated || !items[0].AnswerAvailable || items[0].ResultBytes != len(answer33) {
		t.Fatalf("expected WaitAny expired=true with result_bytes=%d, got %+v", len(answer33), items[0])
	}

	// Simulate a DB read error on turn_results while turns table remains readable:
	// WaitSnapshot and WaitAny must propagate the error instead of pretending empty/expired.
	svc.store.mu.Lock()
	_, err = svc.store.db.Exec(`DROP TABLE turn_results`)
	svc.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.WaitSnapshot(context.Background(), turn.ID, ws.ID, WaitOptions{WaitDuration: 1 * time.Second}); err == nil {
		t.Fatal("expected WaitSnapshot to propagate turn_results read error")
	}
	if _, _, err := svc.WaitAny(context.Background(), []string{turn.ID}, ws.ID, 1*time.Second); err == nil {
		t.Fatal("expected WaitAny to propagate turn_results read error")
	}
}

func TestBeforePruneTerminalTurnsHookRetainsUnreconciledTurnOnly(t *testing.T) {
	now := time.Now().UTC()
	home := t.TempDir()
	hs, err := harnesses.Open(home, "")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(home, hs, &fakeAdapter{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.Close(context.Background()) }()

	age31 := now.Add(-31 * 24 * time.Hour)
	for _, id := range []string{"turn_keep_31d", "turn_drop_31d"} {
		if err := svc.store.SaveTurn(Turn{
			ID: id, WorkerID: "wkr_hk", WorkspaceID: "ws_hk", HarnessID: harnesses.HarnessCodex,
			OperationID: "op_" + id, OperationKey: "nat_" + id, ClientCorrelationID: "corr_" + id,
			State: TurnCompleted, AnswerAvailable: true, AnswerSource: AnswerSourceFinal,
			ResultBytes: 12, StartedAt: age31, UpdatedAt: age31,
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.store.SaveResultText(id, "twelve-bytes", age31); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.store.AppendLog(id, LogEntry{Timestamp: age31, Kind: "progress", Summary: "old"}); err != nil {
			t.Fatal(err)
		}
	}

	// Hook error conservatively retains all summaries while still pruning 7d details.
	svc.SetBeforePruneTerminalTurns(func(_ *Service, expired []Turn, _ time.Time) ([]string, error) {
		return nil, fmt.Errorf("receipt store temporarily unavailable")
	})
	svc.PruneRetention(now)
	for _, id := range []string{"turn_keep_31d", "turn_drop_31d"} {
		if _, err := svc.store.GetTurn(id); err != nil {
			t.Fatalf("hook error should retain summary %s: %v", id, err)
		}
		if _, ok, _ := svc.store.ReadResultText(id); ok {
			t.Fatalf("7d detail for %s should still be pruned even when hook errs", id)
		}
		if logs, _, _ := svc.store.ReadLogs(id, 0, 10); len(logs) != 0 {
			t.Fatalf("7d logs for %s should still be pruned even when hook errs", id)
		}
	}

	// Hook keeping only turn_keep_31d deletes turn_drop_31d and preserves turn_keep_31d.
	svc.SetBeforePruneTerminalTurns(func(_ *Service, expired []Turn, _ time.Time) ([]string, error) {
		return []string{"turn_keep_31d"}, nil
	})
	svc.PruneRetention(now)
	if _, err := svc.store.GetTurn("turn_keep_31d"); err != nil {
		t.Fatalf("expected turn_keep_31d retained, got %v", err)
	}
	if _, err := svc.store.GetTurn("turn_drop_31d"); err == nil {
		t.Fatal("expected turn_drop_31d pruned")
	}
}
