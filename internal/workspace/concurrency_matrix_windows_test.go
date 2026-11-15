//go:build windows

package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func testTurnService(t *testing.T, seed ...Mutation) (*Service, Scope) {
	t.Helper()
	s, pubScope := testService(t)
	if len(seed) > 0 {
		if _, err := s.Mutate("seed-init", pubScope, seed); err != nil {
			t.Fatal(err)
		}
	}
	turnScope := Scope{Conversation: "conv-fixture-0001", Turn: "turn-fixture-0001", Policy: pubScope.Policy}
	if err := s.AcquireTurnScope(turnScope); err != nil {
		t.Fatal(err)
	}
	return s, turnScope
}

func TestDescriptorRegistryAndValidation(t *testing.T) {
	want := []string{
		"read_file", "write_file", "edit_file", "apply_file_patch", "list_dir",
		"search_files", "memory_search", "memory_get", "progress_card",
	}
	got := AssistantToolNames()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AssistantToolNames() = %v, want %v", got, want)
	}
	for _, name := range want {
		desc, ok := LookupDescriptor(name)
		if !ok || desc.Name != name || desc.Prepare == nil {
			t.Fatalf("descriptor missing or incomplete for %s", name)
		}
	}
	if _, ok := LookupDescriptor("unknown_tool"); ok {
		t.Fatal("unexpected descriptor for unknown_tool")
	}
	root := t.TempDir()
	if _, err := PrepareTool("unknown_tool", DescriptorInput{WorkspaceRoot: root}); Code(err) != "unsupported_tool" {
		t.Fatalf("expected unsupported_tool, got %v", err)
	}
	for _, bad := range []ResourceClaim{
		{Domain: "", Key: "a.txt", Coverage: CoverageExact, Access: AccessShared},
		{Domain: DomainFS, Root: "", Key: "a.txt", Coverage: CoverageExact, Access: AccessShared},
		{Domain: DomainFS, Root: DefaultRoot, Key: "a.txt", Coverage: "invalid", Access: AccessShared},
		{Domain: DomainFS, Root: DefaultRoot, Key: "a.txt", Coverage: CoverageExact, Access: "invalid"},
	} {
		if ValidClaim(bad) {
			t.Fatalf("invalid claim accepted: %+v", bad)
		}
	}
	p1, err := PrepareTool("read_file", DescriptorInput{WorkspaceRoot: root, Path: `sub\Note.TXT`})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := PrepareTool("read_file", DescriptorInput{WorkspaceRoot: root, Path: "sub/note.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p1.Claims, p2.Claims) {
		t.Fatalf("normalized claims differ: %+v vs %+v", p1.Claims, p2.Claims)
	}
	for _, invalid := range []string{"../escape.txt", `C:\outside.txt`, "CON.txt", ""} {
		if _, err := PrepareTool("read_file", DescriptorInput{WorkspaceRoot: root, Path: invalid}); err == nil {
			t.Fatalf("accepted invalid read_file path %q", invalid)
		}
	}
}

func TestResourceClaimTruthTable(t *testing.T) {
	fsClaim := func(root, key string, cov Coverage, acc AccessMode) ResourceClaim {
		return ResourceClaim{Domain: DomainFS, Root: root, Key: pathKey(key), Coverage: cov, Access: acc}
	}
	cases := []struct {
		name     string
		a, b     ResourceClaim
		conflict bool
	}{
		{"same_exact_SS", fsClaim("workspace", "a.txt", CoverageExact, AccessShared), fsClaim("workspace", "a.txt", CoverageExact, AccessShared), false},
		{"same_exact_SX", fsClaim("workspace", "a.txt", CoverageExact, AccessShared), fsClaim("workspace", "a.txt", CoverageExact, AccessExclusive), true},
		{"same_exact_XX", fsClaim("workspace", "a.txt", CoverageExact, AccessExclusive), fsClaim("workspace", "a.txt", CoverageExact, AccessExclusive), true},
		{"case_fold_SX", fsClaim("workspace", "Dir/File.MD", CoverageExact, AccessShared), fsClaim("workspace", "dir/file.md", CoverageExact, AccessExclusive), true},
		{"prefix_non_ancestor", fsClaim("workspace", "a", CoverageSubtree, AccessExclusive), fsClaim("workspace", "ab/file.txt", CoverageExact, AccessExclusive), false},
		{"subtree_descendant", fsClaim("workspace", "a", CoverageSubtree, AccessShared), fsClaim("workspace", "a/b/c.txt", CoverageExact, AccessExclusive), true},
		{"root_subtree_any", fsClaim("workspace", "", CoverageSubtree, AccessShared), fsClaim("workspace", "deep/file.txt", CoverageExact, AccessExclusive), true},
		{"children_direct_child", fsClaim("workspace", "dir", CoverageChildren, AccessShared), fsClaim("workspace", "dir/file.txt", CoverageExact, AccessExclusive), true},
		{"children_grandchild_no_conflict", fsClaim("workspace", "dir", CoverageChildren, AccessShared), fsClaim("workspace", "dir/sub/file.txt", CoverageExact, AccessExclusive), false},
		{"root_children_top_file", fsClaim("workspace", "", CoverageChildren, AccessShared), fsClaim("workspace", "top.txt", CoverageExact, AccessExclusive), true},
		{"root_children_nested_file", fsClaim("workspace", "", CoverageChildren, AccessShared), fsClaim("workspace", "sub/nested.txt", CoverageExact, AccessExclusive), false},
		{"different_roots_isolated", fsClaim("workspace", "a.txt", CoverageExact, AccessExclusive), fsClaim("grant_docs", "a.txt", CoverageExact, AccessExclusive), false},
		{"different_domains_isolated", fsClaim("workspace", "memory_index", CoverageExact, AccessExclusive), ResourceClaim{Domain: DomainMemoryIndex, Root: "workspace", Key: "fts", Coverage: CoverageExact, Access: AccessExclusive}, false},
	}
	for _, tc := range cases {
		if got := ClaimsConflict(tc.a, tc.b); got != tc.conflict {
			t.Fatalf("%s: ClaimsConflict=%v, want %v", tc.name, got, tc.conflict)
		}
		if got := ClaimsConflict(tc.b, tc.a); got != tc.conflict {
			t.Fatalf("%s (reversed): ClaimsConflict=%v, want %v", tc.name, got, tc.conflict)
		}
	}

	// Verify 8.3 short-name directory alias rejection when NTFS 8.3 names exist.
	root := t.TempDir()
	longDir := filepath.Join(root, "LongDirectoryNameForAliasCheck")
	if err := os.MkdirAll(longDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(longDir, "item.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("cmd", "/c", "for %I in (\""+longDir+"\") do @echo %~sI").CombinedOutput()
	if err == nil {
		shortBase := filepath.Base(strings.TrimSpace(string(out)))
		if shortBase != "" && !strings.EqualFold(shortBase, "LongDirectoryNameForAliasCheck") {
			if _, err := PrepareTool("read_file", DescriptorInput{WorkspaceRoot: root, Path: shortBase + "/item.txt"}); err == nil {
				t.Fatalf("expected 8.3 alias %s to be rejected", shortBase)
			}
		}
	}
}

func makePairInput(root, tool, variant string) DescriptorInput {
	switch tool {
	case "read_file":
		if variant == "same" {
			return DescriptorInput{WorkspaceRoot: root, Path: "dir/a.txt"}
		}
		if variant == "mem" {
			return DescriptorInput{WorkspaceRoot: root, Path: "memory/notes.md"}
		}
		return DescriptorInput{WorkspaceRoot: root, Path: "other/b.txt"}
	case "write_file", "edit_file":
		if variant == "same" {
			return DescriptorInput{WorkspaceRoot: root, Targets: []Target{{Path: "dir/a.txt"}}, NewBytes: 10}
		}
		if variant == "mem" {
			return DescriptorInput{WorkspaceRoot: root, Targets: []Target{{Path: "memory/notes.md"}}, NewBytes: 10}
		}
		if variant == "missing_parent" {
			return DescriptorInput{WorkspaceRoot: root, Targets: []Target{{Path: "newparent/sub/c.txt"}}, NewBytes: 10}
		}
		return DescriptorInput{WorkspaceRoot: root, Targets: []Target{{Path: "other/b.txt"}}, NewBytes: 10}
	case "apply_file_patch":
		if variant == "same" {
			return DescriptorInput{WorkspaceRoot: root, Targets: []Target{{Path: "dir/a.txt", Remove: true}, {Path: "dir/moved.txt"}}, NewBytes: 20}
		}
		return DescriptorInput{WorkspaceRoot: root, Targets: []Target{{Path: "patch/p.txt"}}, NewBytes: 20}
	case "list_dir":
		if variant == "same" {
			return DescriptorInput{WorkspaceRoot: root, Path: "dir"}
		}
		return DescriptorInput{WorkspaceRoot: root, Path: "other"}
	case "search_files":
		if variant == "same" {
			return DescriptorInput{WorkspaceRoot: root, Path: "dir"}
		}
		return DescriptorInput{WorkspaceRoot: root, Path: "other"}
	case "memory_search":
		return DescriptorInput{WorkspaceRoot: root}
	case "memory_get":
		return DescriptorInput{WorkspaceRoot: root, Path: "memory/notes.md"}
	case "progress_card":
		return DescriptorInput{WorkspaceRoot: root}
	default:
		panic(tool)
	}
}

func plansConflict(a, b PreparedPlan) bool {
	for _, ca := range a.Claims {
		for _, cb := range b.Claims {
			if ClaimsConflict(ca, cb) {
				return true
			}
		}
	}
	return false
}

func TestOrderedToolPairMatrix(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"dir", "other", "patch", "memory"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	tools := AssistantToolNames()
	isWriter := func(name string) bool {
		return name == "write_file" || name == "edit_file" || name == "apply_file_patch"
	}

	// 1. Disjoint scopes: every pair should be non-conflicting EXCEPT memory_search vs memory_search (index X)
	// and memory_get vs memory_get (both read memory/notes.md S -> non-conflicting!).
	for _, tA := range tools {
		for _, tB := range tools {
			inA := makePairInput(root, tA, "same")
			inB := makePairInput(root, tB, "disjoint")
			pA, err := PrepareTool(tA, inA)
			if err != nil {
				t.Fatalf("%s: %v", tA, err)
			}
			pB, err := PrepareTool(tB, inB)
			if err != nil {
				t.Fatalf("%s: %v", tB, err)
			}
			got := plansConflict(pA, pB)
			want := (tA == "memory_search" && tB == "memory_search") || (tA == "progress_card" && tB == "progress_card")
			if got != want {
				t.Fatalf("disjoint (%s, %s): conflict=%v, want %v (claimsA=%+v claimsB=%+v)", tA, tB, got, want, pA.Claims, pB.Claims)
			}
		}
	}

	// 2. Same scope ("dir/a.txt" / "dir"): writers on dir/a.txt conflict with read_file(dir/a.txt), writers(dir/a.txt), list_dir(dir), search_files(dir).
	fsScoped := func(name string) bool {
		return name != "memory_search" && name != "memory_get" && name != "progress_card"
	}
	for _, tA := range tools {
		for _, tB := range tools {
			pA, err := PrepareTool(tA, makePairInput(root, tA, "same"))
			if err != nil {
				t.Fatal(err)
			}
			pB, err := PrepareTool(tB, makePairInput(root, tB, "same"))
			if err != nil {
				t.Fatal(err)
			}
			got := plansConflict(pA, pB)
			want := (tA == "memory_search" && tB == "memory_search") ||
				(tA == "progress_card" && tB == "progress_card") ||
				(fsScoped(tA) && fsScoped(tB) && (isWriter(tA) || isWriter(tB)))
			if got != want {
				t.Fatalf("same-scope (%s, %s): conflict=%v, want %v", tA, tB, got, want)
			}
		}
	}

	// 3. Memory file writes conflict with memory_search and memory_get on same file, but NOT with non-memory writes.
	memWrite, err := PrepareTool("write_file", makePairInput(root, "write_file", "mem"))
	if err != nil {
		t.Fatal(err)
	}
	memSearch, _ := PrepareTool("memory_search", makePairInput(root, "memory_search", "same"))
	memGet, _ := PrepareTool("memory_get", makePairInput(root, "memory_get", "mem"))
	otherWrite, _ := PrepareTool("write_file", makePairInput(root, "write_file", "disjoint"))
	if !plansConflict(memWrite, memSearch) || !plansConflict(memWrite, memGet) {
		t.Fatal("expected memory write to conflict with memory_search and memory_get")
	}
	if plansConflict(otherWrite, memSearch) || plansConflict(otherWrite, memGet) {
		t.Fatal("non-memory write should not conflict with memory_search or memory_get")
	}
}

func TestParallelExecutionOverlap(t *testing.T) {
	s, scope := testTurnService(t,
		Mutation{Action: "write", Path: "a.txt", Expected: MissingRevision, Content: "alpha"},
		Mutation{Action: "write", Path: "b.txt", Expected: MissingRevision, Content: "beta"},
	)

	// Barrier helper proving two calls simultaneously hold active grants in running phase.
	assertOverlap := func(name string, b1, b2 Begin) {
		t.Helper()
		entered := make(chan string, 2)
		release := make(chan struct{})
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, req := range []Begin{b1, b2} {
			wg.Add(1)
			go func(r Begin) {
				defer wg.Done()
				if _, err := s.Begin(r); err != nil {
					errs <- err
					return
				}
				entered <- r.ID
				<-release
				_, err := s.End(r.ID, "completed", "")
				errs <- err
			}(req)
		}
		for range 2 {
			select {
			case <-entered:
			case err := <-errs:
				t.Fatalf("%s failed to overlap: %v", name, err)
			case <-time.After(3 * time.Second):
				t.Fatalf("%s timed out waiting for simultaneous overlap", name)
			}
		}
		close(release)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("%s end error: %v", name, err)
			}
		}
	}

	assertOverlap("same-file-reads",
		Begin{ID: "r1", Scope: scope, Tool: "read_file", Path: "a.txt", Fingerprint: "fp1"},
		Begin{ID: "r2", Scope: scope, Tool: "read_file", Path: "a.txt", Fingerprint: "fp2"},
	)
	assertOverlap("disjoint-writes",
		Begin{ID: "w1", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "a.txt"}}, NewBytes: 5, Fingerprint: "fp3"},
		Begin{ID: "w2", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "b.txt"}}, NewBytes: 5, Fingerprint: "fp4"},
	)
	assertOverlap("disjoint-read-write",
		Begin{ID: "rw1", Scope: scope, Tool: "read_file", Path: "a.txt", Fingerprint: "fp5"},
		Begin{ID: "rw2", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "b.txt"}}, NewBytes: 5, Fingerprint: "fp6"},
	)
	assertOverlap("progress-card-with-write",
		Begin{ID: "pc1", Scope: scope, Tool: "progress_card", Progress: []Step{{Text: "Working", Status: "in_progress"}}, Fingerprint: "fp7"},
		Begin{ID: "pc2", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "a.txt"}}, NewBytes: 5, Fingerprint: "fp8"},
	)
}

func TestConflictingBatchOrderAndAtomicity(t *testing.T) {
	s, scope := testTurnService(t)
	s.EnableTrace(256)

	// Batch 1: write_file(x.txt) [order 0] followed by read_file(x.txt) [order 1].
	// Even if read_file calls Acquire before write_file, batch order guarantees write_file runs first.
	for i, tool := range []string{"write_file", "read_file"} {
		if _, err := s.Register(Register{
			ID: fmt.Sprintf("ord-%d", i), Batch: "batch-order-1", Order: i, Scope: scope,
			Tool: tool, InputFingerprint: fmt.Sprintf("in-%d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SealBatch(SealBatch{
		Batch: "batch-order-1", Scope: scope,
		Items: []SealItem{
			{ID: "ord-0", Tool: "write_file", Targets: []Target{{Path: "x.txt"}}, NewBytes: 7, Fingerprint: "ex-0"},
			{ID: "ord-1", Tool: "read_file", Path: "x.txt", Fingerprint: "ex-1"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	readDone := make(chan string, 1)
	go func() {
		if _, err := s.Acquire(context.Background(), "ord-1", "ex-1"); err != nil {
			readDone <- "err:" + err.Error()
			return
		}
		data, err := s.NativeRead("ord-1", "x.txt")
		_, _ = s.End("ord-1", "completed", "")
		if err != nil {
			readDone <- "err:" + err.Error()
			return
		}
		readDone <- string(data)
	}()

	time.Sleep(40 * time.Millisecond)
	if _, err := s.Acquire(context.Background(), "ord-0", "ex-0"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("ord-0", "x.txt", []byte("written"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.End("ord-0", "completed", ""); err != nil {
		t.Fatal(err)
	}
	if got := <-readDone; got != "written" {
		t.Fatalf("read_file after write_file saw %q, want %q", got, "written")
	}

	// Atomic multi-target patch grant: patch1(a,b) vs patch2(b,a) never deadlocks.
	var wg sync.WaitGroup
	for idx, pair := range [][]Target{
		{{Path: "a.txt"}, {Path: "b.txt"}},
		{{Path: "b.txt"}, {Path: "a.txt"}},
	} {
		wg.Add(1)
		go func(i int, targets []Target) {
			defer wg.Done()
			id := fmt.Sprintf("patch-atom-%d", i)
			if _, err := s.Begin(Begin{ID: id, Scope: scope, Tool: "apply_file_patch", Targets: targets, NewBytes: 12, Fingerprint: id}); err != nil {
				t.Errorf("patch %d begin: %v", i, err)
				return
			}
			_, _ = s.Write(id, targets[0].Path, []byte(fmt.Sprintf("v%d", i)), false)
			_, _ = s.End(id, "completed", "")
		}(idx, pair)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("atomic multi-target claims deadlocked")
	}
}

func TestWriterFairnessAndBypass(t *testing.T) {
	s, scope := testTurnService(t,
		Mutation{Action: "write", Path: "hot.txt", Expected: MissingRevision, Content: "v0"},
		Mutation{Action: "write", Path: "cold.txt", Expected: MissingRevision, Content: "cold"},
	)

	// Hold r1 on hot.txt.
	if _, err := s.Begin(Begin{ID: "r1", Scope: scope, Tool: "read_file", Path: "hot.txt", Fingerprint: "r1"}); err != nil {
		t.Fatal(err)
	}
	// Register & seal w1(hot.txt), then r2(hot.txt), then r_bypass(cold.txt).
	if _, err := s.Register(Register{ID: "w1", Batch: "b-fair", Order: 0, Scope: scope, Tool: "write_file", InputFingerprint: "w1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(Register{ID: "r2", Batch: "b-fair", Order: 1, Scope: scope, Tool: "read_file", InputFingerprint: "r2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(Register{ID: "rbypass", Batch: "b-fair", Order: 2, Scope: scope, Tool: "read_file", InputFingerprint: "rb"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SealBatch(SealBatch{
		Batch: "b-fair", Scope: scope,
		Items: []SealItem{
			{ID: "w1", Tool: "write_file", Targets: []Target{{Path: "hot.txt"}}, NewBytes: 2, Fingerprint: "w1"},
			{ID: "r2", Tool: "read_file", Path: "hot.txt", Fingerprint: "r2"},
			{ID: "rbypass", Tool: "read_file", Path: "cold.txt", Fingerprint: "rb"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Non-conflicting rbypass on cold.txt must grant immediately even while w1 is queued on hot.txt!
	if _, err := s.Acquire(context.Background(), "rbypass", "rb"); err != nil {
		t.Fatalf("non-conflicting reader blocked by queued writer: %v", err)
	}
	_, _ = s.End("rbypass", "completed", "")

	// r2 on hot.txt must NOT bypass queued writer w1!
	r2Ctx, r2Cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer r2Cancel()
	// Use acquireBoundedPoll so r2 stays queued after short poll timeout.
	res, err := s.acquireBoundedPoll(r2Ctx, "r2", "r2", 60)
	if err != nil || res["granted"] == true {
		t.Fatalf("later reader bypassed queued writer: res=%+v err=%v", res, err)
	}

	// Now cancel queued writer w1 while r1 is still active -> r2 should immediately become grantable!
	if err := s.CancelCall("w1", "cancelled"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), "r2", "r2"); err != nil {
		t.Fatalf("cancelling head writer did not wake waiting reader: %v", err)
	}
	_, _ = s.End("r2", "completed", "")
	_, _ = s.End("r1", "completed", "")
}

func TestBatchSealOrderingAndTamperRejection(t *testing.T) {
	s, scope := testTurnService(t, Mutation{Action: "write", Path: "b.txt", Expected: MissingRevision, Content: "old"})

	// Call 0 initially registered with input pointing to a.txt, rewritten before seal to b.txt;
	// Call 1 reads b.txt and calls Acquire BEFORE seal completes. Call 1 must still wait for Call 0!
	_, _ = s.Register(Register{ID: "rewrite-0", Batch: "b-seal", Order: 0, Scope: scope, Tool: "write_file", InputFingerprint: "raw-a"})
	_, _ = s.Register(Register{ID: "rewrite-1", Batch: "b-seal", Order: 1, Scope: scope, Tool: "read_file", InputFingerprint: "raw-b"})

	observed := make(chan string, 1)
	go func() {
		if _, err := s.Acquire(context.Background(), "rewrite-1", "final-b"); err != nil {
			observed <- "err:" + err.Error()
			return
		}
		data, _ := s.NativeRead("rewrite-1", "b.txt")
		_, _ = s.End("rewrite-1", "completed", "")
		observed <- string(data)
	}()

	time.Sleep(40 * time.Millisecond)
	if err := s.SealBatch(SealBatch{
		Batch: "b-seal", Scope: scope,
		Items: []SealItem{
			{ID: "rewrite-0", Tool: "write_file", Targets: []Target{{Path: "b.txt"}}, NewBytes: 3, Fingerprint: "final-0"},
			{ID: "rewrite-1", Tool: "read_file", Path: "b.txt", Fingerprint: "final-b"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), "rewrite-0", "wrong-fp"); Code(err) != "fingerprint_mismatch" {
		t.Fatalf("expected fingerprint_mismatch on tampered acquire, got %v", err)
	}
	if _, err := s.Acquire(context.Background(), "rewrite-0", "final-0"); err != nil {
		t.Fatal(err)
	}
	// Duplicate Acquire while active is idempotent with matching fingerprint.
	if _, err := s.Acquire(context.Background(), "rewrite-0", "final-0"); err != nil {
		t.Fatalf("duplicate Acquire failed: %v", err)
	}
	_, _ = s.Write("rewrite-0", "b.txt", []byte("new"), false)
	_, _ = s.End("rewrite-0", "completed", "")
	if got := <-observed; got != "new" {
		t.Fatalf("expected rewritten target order to yield 'new', got %q", got)
	}

	// Incomplete batch seal must fail with invalid_batch and clean up registrations without leaking slots.
	_, _ = s.Register(Register{ID: "inc-0", Batch: "b-inc", Order: 0, Scope: scope, Tool: "read_file", InputFingerprint: "i0"})
	_, _ = s.Register(Register{ID: "inc-1", Batch: "b-inc", Order: 1, Scope: scope, Tool: "read_file", InputFingerprint: "i1"})
	if err := s.SealBatch(SealBatch{Batch: "b-inc", Scope: scope, Items: []SealItem{{ID: "inc-0", Tool: "read_file", Path: "b.txt", Fingerprint: "f0"}}}); Code(err) != "invalid_batch" {
		t.Fatalf("expected invalid_batch, got %v", err)
	}
	if slices.ContainsFunc([]string{"inc-0", "inc-1"}, func(id string) bool {
		s.sched.mu.Lock()
		defer s.sched.mu.Unlock()
		_, ok := s.sched.entries[id]
		return ok
	}) {
		t.Fatal("incomplete batch leaked scheduler entries")
	}
}

func TestSameBatchWriteConflictAndOrderedPipelines(t *testing.T) {
	s, scope := testTurnService(t)

	// 1. Same-batch write_file + write_file on the same canonical path (case-insensitive) rejects the second write.
	_, _ = s.Register(Register{ID: "cw-0", Batch: "b-clobber-1", Order: 0, Scope: scope, Tool: "write_file", InputFingerprint: "in-cw0"})
	_, _ = s.Register(Register{ID: "cw-1", Batch: "b-clobber-1", Order: 1, Scope: scope, Tool: "write_file", InputFingerprint: "in-cw1"})
	if err := s.SealBatch(SealBatch{
		Batch: "b-clobber-1", Scope: scope,
		Items: []SealItem{
			{ID: "cw-0", Tool: "write_file", Targets: []Target{{Path: "Race.txt"}}, NewBytes: 8, Fingerprint: "ex-cw0"},
			{ID: "cw-1", Tool: "write_file", Targets: []Target{{Path: "race.txt"}}, NewBytes: 8, Fingerprint: "ex-cw1"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), "cw-1", "ex-cw1"); Code(err) != "conflicting_batch_write" {
		t.Fatalf("expected second write_file to fail with conflicting_batch_write, got %v", err)
	}
	if _, err := s.Acquire(context.Background(), "cw-0", "ex-cw0"); err != nil {
		t.Fatalf("first write_file should still acquire cleanly: %v", err)
	}
	if _, err := s.Write("cw-0", "Race.txt", []byte("winner-0"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.End("cw-0", "completed", ""); err != nil {
		t.Fatal(err)
	}

	// 2. Same-batch edit_file [order 0] + write_file [order 1] rejects the blind overwrite at order 1.
	_, _ = s.Register(Register{ID: "ew-0", Batch: "b-clobber-2", Order: 0, Scope: scope, Tool: "edit_file", InputFingerprint: "in-ew0"})
	_, _ = s.Register(Register{ID: "ew-1", Batch: "b-clobber-2", Order: 1, Scope: scope, Tool: "write_file", InputFingerprint: "in-ew1"})
	if err := s.SealBatch(SealBatch{
		Batch: "b-clobber-2", Scope: scope,
		Items: []SealItem{
			{ID: "ew-0", Tool: "edit_file", Targets: []Target{{Path: "Race.txt"}}, NewBytes: 8, Fingerprint: "ex-ew0"},
			{ID: "ew-1", Tool: "write_file", Targets: []Target{{Path: "Race.txt"}}, NewBytes: 8, Fingerprint: "ex-ew1"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Acquire(context.Background(), "ew-1", "ex-ew1"); Code(err) != "conflicting_batch_write" {
		t.Fatalf("expected write_file after edit_file in same batch to fail with conflicting_batch_write, got %v", err)
	}
	if _, err := s.Acquire(context.Background(), "ew-0", "ex-ew0"); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Write("ew-0", "Race.txt", []byte("edited-0"), false)
	_, _ = s.End("ew-0", "completed", "")

	// 3. Reverse order write_file [order 0] -> edit_file [order 1] is a valid pipeline and executes in strict order.
	_, _ = s.Register(Register{ID: "we-0", Batch: "b-pipe", Order: 0, Scope: scope, Tool: "write_file", InputFingerprint: "in-we0"})
	_, _ = s.Register(Register{ID: "we-1", Batch: "b-pipe", Order: 1, Scope: scope, Tool: "edit_file", InputFingerprint: "in-we1"})
	if err := s.SealBatch(SealBatch{
		Batch: "b-pipe", Scope: scope,
		Items: []SealItem{
			{ID: "we-0", Tool: "write_file", Targets: []Target{{Path: "pipe.txt"}}, NewBytes: 4, Fingerprint: "ex-we0"},
			{ID: "we-1", Tool: "edit_file", Targets: []Target{{Path: "pipe.txt"}}, NewBytes: 6, Fingerprint: "ex-we1"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	editDone := make(chan string, 1)
	go func() {
		if _, err := s.Acquire(context.Background(), "we-1", "ex-we1"); err != nil {
			editDone <- "err:" + err.Error()
			return
		}
		prev, _ := s.NativeRead("we-1", "pipe.txt")
		_, _ = s.Write("we-1", "pipe.txt", append(prev, []byte("+1")...), false)
		_, _ = s.End("we-1", "completed", "")
		editDone <- string(prev) + "+1"
	}()
	time.Sleep(30 * time.Millisecond)
	if _, err := s.Acquire(context.Background(), "we-0", "ex-we0"); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Write("we-0", "pipe.txt", []byte("base"), false)
	_, _ = s.End("we-0", "completed", "")
	if got := <-editDone; got != "base+1" {
		t.Fatalf("write_file -> edit_file pipeline produced %q, want %q", got, "base+1")
	}

	// 4. Subsequent batch write_file on the same path remains allowed across batches/turns.
	if _, err := s.Begin(Begin{ID: "next-batch-w", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "Race.txt"}}, NewBytes: 4, Fingerprint: "nbw"}); err != nil {
		t.Fatalf("cross-batch write_file should remain allowed: %v", err)
	}
	_, _ = s.Write("next-batch-w", "Race.txt", []byte("next"), false)
	_, _ = s.End("next-batch-w", "completed", "")
}
