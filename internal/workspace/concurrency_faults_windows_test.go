//go:build windows

package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentMissingParentCreation(t *testing.T) {
	s, scope := testTurnService(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, name := range []string{"shared/nested/a.txt", "shared/nested/b.txt"} {
		wg.Add(1)
		go func(idx int, rel string) {
			defer wg.Done()
			id := fmt.Sprintf("mp-%d", idx)
			if _, err := s.Begin(Begin{ID: id, Scope: scope, Tool: "write_file", Targets: []Target{{Path: rel}}, NewBytes: 6, Fingerprint: id}); err != nil {
				errs <- err
				return
			}
			if _, err := s.Write(id, rel, []byte(fmt.Sprintf("val-%d", idx)), false); err != nil {
				_, _ = s.End(id, "failed", Code(err))
				errs <- err
				return
			}
			_, err := s.End(id, "completed", "")
			errs <- err
		}(i, name)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("missing parent concurrent write failed: %v", err)
		}
	}
	for i, name := range []string{"shared/nested/a.txt", "shared/nested/b.txt"} {
		got, err := s.Read(name, "", 0)
		if err != nil || string(got.Data) != fmt.Sprintf("val-%d", i) {
			t.Fatalf("unexpected file %s: %+v %v", name, got, err)
		}
	}
	// Restoring a.txt to missing leaves b.txt intact.
	s.turn = ""
	curA, _ := s.Read("shared/nested/a.txt", "", 0)
	if _, err := s.Restore("restore-mp-0", Scope{Policy: scope.Policy}, "mp-0", "shared/nested/a.txt", curA.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("shared/nested/a.txt", "", 0); Code(err) != "not_found" {
		t.Fatalf("expected a.txt restored to missing, got %v", err)
	}
	if gotB, err := s.Read("shared/nested/b.txt", "", 0); err != nil || string(gotB.Data) != "val-1" {
		t.Fatalf("sibling b.txt affected by restore: %+v %v", gotB, err)
	}
}

func TestDirectoryAndSearchScopeConcurrency(t *testing.T) {
	s, scope := testTurnService(t,
		Mutation{Action: "write", Path: "a/one.txt", Expected: MissingRevision, Content: "needle"},
		Mutation{Action: "write", Path: "b/two.txt", Expected: MissingRevision, Content: "other"},
	)

	// Disjoint: list_dir(a) + write_file(b/three.txt) overlap simultaneously.
	if _, err := s.Begin(Begin{ID: "ld-a", Scope: scope, Tool: "list_dir", Path: "a", Fingerprint: "ld-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "wr-b", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "b/three.txt"}}, NewBytes: 4, Fingerprint: "wr-b"}); err != nil {
		t.Fatalf("disjoint write blocked by list_dir(a): %v", err)
	}
	items, err := s.ReadDirectoryWithCall("ld-a", "a")
	if err != nil || len(items) != 1 || items[0].Path != "a/one.txt" {
		t.Fatalf("unexpected list_dir(a): %+v %v", items, err)
	}
	_, _ = s.Write("wr-b", "b/three.txt", []byte("four"), false)
	_, _ = s.End("wr-b", "completed", "")
	_, _ = s.End("ld-a", "completed", "")

	// Disjoint: search_files(a) + write_file(b/four.txt) overlap simultaneously.
	if _, err := s.Begin(Begin{ID: "sf-a", Scope: scope, Tool: "search_files", Path: "a", Fingerprint: "sf-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "wr-b2", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "b/four.txt"}}, NewBytes: 4, Fingerprint: "wr-b2"}); err != nil {
		t.Fatalf("disjoint write blocked by search_files(a): %v", err)
	}
	page, err := s.SearchWithCall("sf-a", "a", "needle", "", true, true)
	if err != nil || len(page.Items) != 1 || page.Items[0].Path != "a/one.txt" {
		t.Fatalf("unexpected search_files(a): %+v %v", page, err)
	}
	_, _ = s.End("wr-b2", "completed", "")
	_, _ = s.End("sf-a", "completed", "")
}

func TestCallScopedSubOperationsAndIdentityIsolation(t *testing.T) {
	s, scope := testTurnService(t,
		Mutation{Action: "write", Path: "allowed.txt", Expected: MissingRevision, Content: "hello"},
		Mutation{Action: "write", Path: "forbidden.txt", Expected: MissingRevision, Content: "secret"},
	)

	// Start read_file(allowed.txt) AND write_file(forbidden.txt) concurrently.
	if _, err := s.Begin(Begin{ID: "reader", Scope: scope, Tool: "read_file", Path: "allowed.txt", Fingerprint: "r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "writer", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "forbidden.txt"}}, NewBytes: 6, Fingerprint: "w"}); err != nil {
		t.Fatal(err)
	}

	// Missing/unknown call IDs must never borrow an active call's handle.
	if _, err := s.NativeRead("", "allowed.txt"); Code(err) != "invalid_call" {
		t.Fatalf("empty callID accepted on NativeRead: %v", err)
	}
	if _, err := s.Write("", "forbidden.txt", []byte("x"), false); Code(err) != "invalid_call" {
		t.Fatalf("empty callID accepted on Write: %v", err)
	}
	if _, err := s.Write("unknown-id", "forbidden.txt", []byte("x"), false); Code(err) != "invalid_call" {
		t.Fatalf("unknown callID accepted on Write: %v", err)
	}

	// Undeclared target & undeclared effect on readers must fail and taint completion!
	if _, err := s.NativeRead("reader", "forbidden.txt"); Code(err) != "undeclared_effect" {
		t.Fatalf("undeclared read target accepted: %v", err)
	}
	if _, err := s.Begin(Begin{ID: "reader-w", Scope: scope, Tool: "read_file", Path: "allowed.txt", Fingerprint: "rw"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("reader-w", "allowed.txt", []byte("tamper"), false); Code(err) != "undeclared_effect" {
		t.Fatalf("undeclared write effect accepted: %v", err)
	}
	endReaderW, err := s.End("reader-w", "completed", "")
	if err != nil || endReaderW.State != "failed" || endReaderW.Detail != "undeclared_effect" {
		t.Fatalf("tainted reader-w was allowed to end completed: %+v %v", endReaderW, err)
	}
	endReader, err := s.End("reader", "completed", "")
	if err != nil || endReader.State != "failed" || endReader.Detail != "undeclared_effect" {
		t.Fatalf("tainted call was allowed to end completed: %+v %v", endReader, err)
	}

	// Late request after End must fail with invalid_call.
	if _, err := s.NativeRead("reader", "allowed.txt"); Code(err) != "invalid_call" {
		t.Fatalf("post-End NativeRead accepted: %v", err)
	}
	_, _ = s.End("writer", "completed", "")

	// Existing parent directory Mkdir succeeds as a clean no-op before Write, while undeclared dir Mkdir fails.
	if err := os.MkdirAll(filepath.Join(s.root, "existing"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "wr-sub", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "existing/item.txt"}}, NewBytes: 2, Fingerprint: "ws"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Mkdir("wr-sub", "existing"); err != nil {
		t.Fatalf("Mkdir on existing target parent failed: %v", err)
	}
	if _, err := s.Write("wr-sub", "existing/item.txt", []byte("ok"), false); err != nil {
		t.Fatalf("Write after existing-parent Mkdir failed: %v", err)
	}
	if _, err := s.End("wr-sub", "completed", ""); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsSharedReadAndExternalContention(t *testing.T) {
	s, scope := testService(t)
	if _, err := s.Mutate("seed", scope, []Mutation{{Action: "write", Path: "shared.txt", Expected: MissingRevision, Content: "shared-body"}}); err != nil {
		t.Fatal(err)
	}
	hostFile := filepath.Join(s.root, "shared.txt")

	// Block inside NativeRead so the OS shared-read handle is actively open!
	inRead := make(chan struct{})
	releaseRead := make(chan struct{})
	s.SetIOHook(func(callID, _ string, eff Effect, _ string) {
		if callID == "sr-1" && eff == EffectRead {
			close(inRead)
			<-releaseRead
		}
	})
	defer s.SetIOHook(nil)

	if _, err := s.Begin(Begin{ID: "sr-1", Scope: scope, Tool: "read_file", Path: "shared.txt", Fingerprint: "sr1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "sr-2", Scope: scope, Tool: "read_file", Path: "shared.txt", Fingerprint: "sr2"}); err != nil {
		t.Fatal(err)
	}

	readErr := make(chan error, 1)
	go func() {
		_, err := s.NativeRead("sr-1", "shared.txt")
		readErr <- err
	}()
	<-inRead

	// While sr-1 holds its Windows FILE_SHARE_READ handle open:
	// 1) sr-2 can simultaneously read the same file via FILE_SHARE_READ.
	data2, err := s.NativeRead("sr-2", "shared.txt")
	if err != nil || string(data2) != "shared-body" {
		t.Fatalf("second concurrent NativeRead failed: %q %v", data2, err)
	}
	// 2) An external writer attempting to open shared.txt for write is rejected by Windows sharing rules!
	if ext, extErr := os.OpenFile(hostFile, os.O_WRONLY, 0600); extErr == nil {
		ext.Close()
		t.Fatal("external writer opened file while shared-read handle was pinned")
	}

	close(releaseRead)
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
	_, _ = s.End("sr-1", "completed", "")
	_, _ = s.End("sr-2", "completed", "")
}

func TestConcurrentQuotaAndJournalPinning(t *testing.T) {
	s, scope := testTurnService(t)
	// Pre-fill workspace to leave 20 MiB free so two 12 MiB writes (< 16 MiB batch limit) compete for quota.
	fillPath := filepath.Join(s.root, "fill.bin")
	if err := os.WriteFile(fillPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(fillPath, MaxWorkspaceBytes-20<<20); err != nil {
		t.Fatal(err)
	}
	twelveMiB := int64(12 << 20)

	// Atomic virtual quota reservation: two 12 MiB writes cannot both acquire when 20 MiB remains!
	if _, err := s.Begin(Begin{ID: "q-1", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "big1.txt"}}, NewBytes: twelveMiB, Fingerprint: "q1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "q-2", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "big2.txt"}}, NewBytes: twelveMiB, Fingerprint: "q2"}); Code(err) != "workspace_limit" {
		t.Fatalf("expected workspace_limit on concurrent over-allocation, got %v", err)
	}
	// Ending q-1 without writing releases the virtual reservation immediately.
	if _, err := s.End("q-1", "cancelled", "cancelled"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "q-3", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "big3.txt"}}, NewBytes: twelveMiB, Fingerprint: "q3"}); err != nil {
		t.Fatalf("reservation leaked after cancelled call: %v", err)
	}
	_, _ = s.End("q-3", "completed", "")
	_ = os.Remove(fillPath)

	// Pinning check: an active call's before-image is protected from journal GC.
	if _, err := s.Begin(Begin{ID: "seed-pin", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "pin.txt"}}, NewBytes: 10, Fingerprint: "sp"}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Write("seed-pin", "pin.txt", []byte("before-pin"), false)
	_, _ = s.End("seed-pin", "completed", "")
	if _, err := s.Begin(Begin{ID: "active-pin", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "pin.txt"}}, NewBytes: 10, Fingerprint: "ap"}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	pinnedCount := s.pinnedJournals["active-pin"]
	errPrune := s.pruneLocked()
	s.mu.Unlock()
	if errPrune != nil || pinnedCount != 1 {
		t.Fatalf("expected active-pin journal to be pinned: count=%d err=%v", pinnedCount, errPrune)
	}
	if _, err := os.Stat(filepath.Join(s.journal, "active-pin.json")); err != nil {
		t.Fatalf("active journal deleted during prune: %v", err)
	}
	_, _ = s.End("active-pin", "completed", "")
}

func TestMemoryCancellationUnknownAndControlSlots(t *testing.T) {
	s, scope := testTurnService(t,
		Mutation{Action: "write", Path: "MEMORY.md", Expected: MissingRevision, Content: "# Memory\nalpha"},
		Mutation{Action: "write", Path: "src/app.txt", Expected: MissingRevision, Content: "code"},
	)

	// WC15: memory_search overlaps with non-memory write_file, while blocking a second memory_search.
	if _, err := s.Begin(Begin{ID: "ms-1", Scope: scope, Tool: "memory_search", Fingerprint: "ms1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "wr-code", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "src/app.txt"}}, NewBytes: 5, Fingerprint: "wc"}); err != nil {
		t.Fatalf("non-memory write blocked by memory_search: %v", err)
	}
	_, _ = s.Register(Register{ID: "ms-2", Batch: "b-ms2", Order: 0, Scope: scope, Tool: "memory_search", InputFingerprint: "ms2"})
	_ = s.SealBatch(SealBatch{Batch: "b-ms2", Scope: scope, Items: []SealItem{{ID: "ms-2", Tool: "memory_search", Fingerprint: "ms2"}}})
	res, err := s.acquireBoundedPoll(context.Background(), "ms-2", "ms2", 50)
	if err != nil || res["granted"] == true {
		t.Fatalf("second memory_search ran concurrently with first: %+v %v", res, err)
	}
	_ = s.CancelCall("ms-2", "cancelled")
	_, _ = s.End("wr-code", "completed", "")
	_, _ = s.End("ms-1", "completed", "")
	opMS2, err := s.Operation("ms-2")
	if err != nil || opMS2.State != "cancelled" {
		t.Fatalf("expected cancelled queued ms-2 operation, got %+v %v", opMS2, err)
	}

	verifySaturatedTransportControlSlots(t, s, scope)

	// WC19: Public Read works during active turn on non-conflicting files, while public Mutate rejects with busy.
	if _, err := s.Begin(Begin{ID: "turn-wr", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "MEMORY.md"}}, NewBytes: 10, Fingerprint: "tw"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read("src/app.txt", "", 0); err != nil || string(got.Data) != "code" {
		t.Fatalf("public disjoint Read failed during turn: %+v %v", got, err)
	}
	if _, err := s.Mutate("pub-mut", Scope{Policy: scope.Policy}, []Mutation{{Action: "write", Path: "other.txt", Expected: MissingRevision, Content: "x"}}); Code(err) != "busy" {
		t.Fatalf("public Mutate during active turn returned %v, want busy", err)
	}

	// WC17: Unknown failure blocks new mutating calls until reconciled.
	if _, err := s.End("turn-wr", "unknown", "simulated_transport_loss"); err != nil {
		t.Fatal(err)
	}
	if !s.Status().Busy && s.Status().Ready {
		t.Fatal("expected service Ready=false after unknown outcome")
	}
	if _, err := s.Begin(Begin{ID: "after-unk", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "src/app.txt"}}, NewBytes: 2, Fingerprint: "au"}); Code(err) != "reconciliation_required" {
		t.Fatalf("expected reconciliation_required after unknown outcome, got %v", err)
	}
	if err := s.RuntimeStopped(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve("turn-wr", "Verified MEMORY.md remained intact."); err != nil {
		t.Fatal(err)
	}
	if !s.Status().Ready {
		t.Fatal("expected service Ready=true after Resolve")
	}
}

func verifySaturatedTransportControlSlots(t *testing.T, s *Service, scope Scope) {
	t.Helper()
	tr, err := Serve(s)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	blockIO := make(chan struct{})
	var enteredIO atomic.Int32
	s.SetIOHook(func(callID, _ string, eff Effect, _ string) {
		if eff == EffectRead && len(callID) > 4 && callID[:4] == "sat-" {
			enteredIO.Add(1)
			<-blockIO
		}
	})
	s.sched.mu.Lock()
	s.sched.maxActive = 16
	s.sched.mu.Unlock()

	post := func(payload map[string]any) (*http.Response, error) {
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", tr.URL, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tr.Token)
		req.Header.Set("Content-Type", "application/json")
		return (&http.Client{Timeout: 2 * time.Second}).Do(req)
	}

	var ioWG sync.WaitGroup
	for i := range 12 {
		id := fmt.Sprintf("sat-%02d", i)
		if _, err := s.Begin(Begin{ID: id, Scope: scope, Tool: "read_file", Path: "src/app.txt", Fingerprint: id}); err != nil {
			t.Fatal(err)
		}
		ioWG.Add(1)
		go func(cid string) {
			defer ioWG.Done()
			if resp, reqErr := post(map[string]any{"action": "read", "id": cid, "path": "src/app.txt"}); reqErr == nil {
				resp.Body.Close()
			}
		}(id)
	}
	deadline := time.Now().Add(2 * time.Second)
	for enteredIO.Load() < 12 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if enteredIO.Load() != 12 {
		close(blockIO)
		t.Fatalf("expected 12 saturated ioSlots, got %d", enteredIO.Load())
	}

	startCtrl := time.Now()
	resp, err := post(map[string]any{
		"action": "register", "id": "ctrl-probe", "batch": "ctrl-batch", "order": 0,
		"run": scope.Turn, "conversation": scope.Conversation, "tool": "progress_card", "input_fingerprint": "cfp",
	})
	if err != nil || resp.StatusCode != 200 {
		close(blockIO)
		t.Fatalf("control register starved by saturated ioSlots: resp=%v err=%v", resp, err)
	}
	resp.Body.Close()
	resp, err = post(map[string]any{"action": "cancel", "id": "ctrl-probe", "detail": "cancelled"})
	if err != nil || resp.StatusCode != 200 || time.Since(startCtrl) > time.Second {
		close(blockIO)
		t.Fatalf("control cancel starved: elapsed=%v err=%v", time.Since(startCtrl), err)
	}
	resp.Body.Close()

	close(blockIO)
	ioWG.Wait()
	s.SetIOHook(nil)
	for i := range 12 {
		_, _ = s.End(fmt.Sprintf("sat-%02d", i), "completed", "")
	}
}

func TestBootstrapBridgeGateAndContextCancellation(t *testing.T) {
	s, scope := testTurnService(t, Mutation{Action: "write", Path: "src/app.txt", Expected: MissingRevision, Content: "hello"})
	tr, err := Serve(s)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	callBridge := func(payload map[string]any) string {
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", tr.URL, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tr.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, reqErr := (&http.Client{Timeout: 2 * time.Second}).Do(req)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		defer resp.Body.Close()
		var decoded struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&decoded)
		return decoded.Error
	}

	for _, action := range []string{"read", "stat", "list", "search"} {
		if got := callBridge(map[string]any{"action": action, "id": "", "path": "src/app.txt"}); got != "invalid_call" {
			t.Fatalf("expected unbootstrapped %s with empty id to fail invalid_call, got %q", action, got)
		}
	}
	if got := callBridge(map[string]any{"action": "search", "id": "", "bootstrap": true, "path": "src"}); got != "invalid_call" {
		t.Fatalf("expected search with empty id to reject bootstrap flag, got %q", got)
	}
	for _, tc := range []map[string]any{
		{"action": "read", "id": "", "bootstrap": true, "path": "src/app.txt"},
		{"action": "stat", "id": "", "bootstrap": true, "path": "src/app.txt"},
		{"action": "list", "id": "", "bootstrap": true, "path": "src"},
	} {
		if got := callBridge(tc); got != "" {
			t.Fatalf("expected explicit bootstrap %s to succeed, got %q", tc["action"], got)
		}
	}

	if _, err := s.Begin(Begin{ID: "hold-x", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "src/app.txt"}}, NewBytes: 5, Fingerprint: "hx"}); err != nil {
		t.Fatal(err)
	}
	defer s.End("hold-x", "cancelled", "cancelled")

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := s.ReadContext(ctx, "src/app.txt", "", 0); Code(err) != "cancelled" {
		t.Fatalf("ReadContext expected cancelled, got %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel2()
	if _, err := s.ReadDirectoryContext(ctx2, "src"); Code(err) != "cancelled" {
		t.Fatalf("ReadDirectoryContext expected cancelled, got %v", err)
	}
	ctx3, cancel3 := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel3()
	if _, err := s.SearchContext(ctx3, "src", "app", "", false, true); Code(err) != "cancelled" {
		t.Fatalf("SearchContext expected cancelled, got %v", err)
	}
}

func TestActivityQuotaHeadroomAndSequenceCursor(t *testing.T) {
	s, scope := testTurnService(t, Mutation{Action: "write", Path: "item.txt", Expected: MissingRevision, Content: "v1"})

	if _, err := s.Begin(Begin{ID: "tail-1", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "item.txt"}}, NewBytes: 2, Fingerprint: "t1"}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	savedReserve := s.reservedActivityBytes
	s.reservedActivityBytes = MaxActivityBytes - 4096
	s.mu.Unlock()
	if _, err := s.Begin(Begin{ID: "tail-over", Scope: scope, Tool: "progress_card", Fingerprint: "to"}); Code(err) != "activity_limit" {
		t.Fatalf("expected activity_limit when < MaxOperationBytes remains, got %v", err)
	}
	if _, err := s.End("tail-1", "completed", ""); err != nil {
		t.Fatalf("active End must not fail with activity_limit over future call headroom: %v", err)
	}
	s.mu.Lock()
	s.reservedActivityBytes = savedReserve
	s.mu.Unlock()

	if _, err := s.Begin(Begin{ID: "blocker", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "other.txt"}}, NewBytes: 1, Fingerprint: "bl"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(Register{ID: "keep-res", Batch: "b-keep", Order: 0, Scope: scope, Tool: "write_file", NewBytes: 1, InputFingerprint: "kr"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SealBatch(SealBatch{Batch: "b-keep", Scope: scope, Items: []SealItem{{ID: "keep-res", Tool: "write_file", Targets: []Target{{Path: "other.txt"}}, NewBytes: 1, Fingerprint: "kr"}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.journal, "persist-fail.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "persist-fail", Scope: scope, Tool: "read_file", Path: "item.txt", Fingerprint: "pf"}); err == nil {
		t.Fatal("expected persist failure when journal path is a directory")
	}
	_ = os.RemoveAll(filepath.Join(s.journal, "persist-fail.json"))
	s.mu.Lock()
	remReserve := s.reservedActivityBytes
	s.mu.Unlock()
	if remReserve != MaxOperationBytes {
		t.Fatalf("persist failure underflowed sibling activity reservation: got %d, want %d", remReserve, MaxOperationBytes)
	}
	_ = s.CancelCall("keep-res", "cancelled")
	_, _ = s.End("blocker", "completed", "")

	// Incremental cursor across queued -> running -> terminal + batch cleanup.
	_, cursor := s.Activities(scope.Conversation, scope.Turn, 0)
	if _, err := s.Begin(Begin{ID: "seq-hold", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "item.txt"}}, NewBytes: 2, Fingerprint: "sh"}); err != nil {
		t.Fatal(err)
	}
	_, cursor = s.Activities(scope.Conversation, scope.Turn, cursor)

	if _, err := s.Register(Register{ID: "seq-wait", Batch: "b-seq", Order: 0, Scope: scope, Tool: "write_file", NewBytes: 2, InputFingerprint: "sw"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SealBatch(SealBatch{Batch: "b-seq", Scope: scope, Items: []SealItem{{ID: "seq-wait", Tool: "write_file", Targets: []Target{{Path: "item.txt"}}, NewBytes: 2, Fingerprint: "sw"}}}); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		_, _ = s.Acquire(context.Background(), "seq-wait", "sw")
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		items, next := s.Activities(scope.Conversation, scope.Turn, cursor)
		if len(items) > 0 && items[len(items)-1].ID == "seq-wait" && items[len(items)-1].Phase == PhaseQueued {
			cursor = next
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for queued incremental activity, last items=%+v", items)
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, _ = s.End("seq-hold", "completed", "")
	<-waitDone
	items, next := s.Activities(scope.Conversation, scope.Turn, cursor)
	if len(items) == 0 || items[len(items)-1].ID != "seq-wait" || items[len(items)-1].Phase != PhaseRunning {
		t.Fatalf("expected running update on incremental cursor, got %+v", items)
	}
	cursor = next
	_, _ = s.End("seq-wait", "completed", "")
	items, _ = s.Activities(scope.Conversation, scope.Turn, cursor)
	if len(items) != 1 || items[0].ID != "seq-wait" || items[0].State != "completed" {
		t.Fatalf("expected terminal update on incremental cursor, got %+v", items)
	}
	s.sched.mu.Lock()
	batchesLeft := len(s.sched.batches)
	s.sched.mu.Unlock()
	if batchesLeft != 0 {
		t.Fatalf("expected 0 remaining batches, got %d", batchesLeft)
	}
}
