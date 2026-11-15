//go:build windows

package workspace

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func (sc *scheduler) setFaultsForTest(f *mutationFaultHooks) {
	sc.mu.Lock()
	sc.faults = f
	sc.mu.Unlock()
}

func TestMutationDetectionSuite(t *testing.T) {
	runClaimAndIdentityMutations(t)
	runLifecycleAndQuotaMutations(t)
	runFairnessAndSealMutations(t)
}

func runClaimAndIdentityMutations(t *testing.T) {
	t.Helper()
	t.Run("mutation_01_write_downgraded_to_shared", func(t *testing.T) {
		s, scope := testService(t)
		s.sched.setFaultsForTest(&mutationFaultHooks{downgradeWriteToShared: true})
		defer s.sched.setFaultsForTest(nil)
		_, err1 := s.Begin(Begin{ID: "m1-a", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "x.txt"}}, NewBytes: 2, Fingerprint: "a"})
		s.sched.mu.Lock()
		pA, _ := PrepareTool("write_file", DescriptorInput{WorkspaceRoot: s.root, Targets: []Target{{Path: "x.txt"}}, NewBytes: 2})
		pB, _ := PrepareTool("write_file", DescriptorInput{WorkspaceRoot: s.root, Targets: []Target{{Path: "x.txt"}}, NewBytes: 2})
		s.sched.applyClaimFaults(&pA)
		s.sched.applyClaimFaults(&pB)
		conflict := s.sched.claimsConflictLocked(pA.Claims, pB.Claims)
		s.sched.mu.Unlock()
		_, _ = s.End("m1-a", "completed", "")
		if err1 != nil || conflict {
			t.Fatal("mutation 01 detector did not observe missing write/write conflict")
		}
	})

	t.Run("mutation_02_shared_exclusive_compatible", func(t *testing.T) {
		s, _ := testService(t)
		s.sched.setFaultsForTest(&mutationFaultHooks{treatSharedExclusiveOK: true})
		defer s.sched.setFaultsForTest(nil)
		pR, _ := PrepareTool("read_file", DescriptorInput{WorkspaceRoot: s.root, Path: "x.txt"})
		pW, _ := PrepareTool("write_file", DescriptorInput{WorkspaceRoot: s.root, Targets: []Target{{Path: "x.txt"}}, NewBytes: 2})
		s.sched.mu.Lock()
		conflict := s.sched.claimsConflictLocked(pR.Claims, pW.Claims)
		s.sched.mu.Unlock()
		if conflict {
			t.Fatal("mutation 02 fault failed to suppress S/X conflict")
		}
	})

	t.Run("mutation_03_omit_missing_parent_claims", func(t *testing.T) {
		s, _ := testService(t)
		s.sched.setFaultsForTest(&mutationFaultHooks{omitMissingParentClaims: true})
		defer s.sched.setFaultsForTest(nil)
		pA, _ := PrepareTool("write_file", DescriptorInput{WorkspaceRoot: s.root, Targets: []Target{{Path: "newdir/a.txt"}}, NewBytes: 2})
		pB, _ := PrepareTool("write_file", DescriptorInput{WorkspaceRoot: s.root, Targets: []Target{{Path: "newdir/b.txt"}}, NewBytes: 2})
		s.sched.applyClaimFaults(&pA)
		s.sched.applyClaimFaults(&pB)
		if plansConflict(pA, pB) {
			t.Fatal("mutation 03 fault still conflicted without parent claims")
		}
		s.sched.setFaultsForTest(nil)
		nA, _ := PrepareTool("write_file", DescriptorInput{WorkspaceRoot: s.root, Targets: []Target{{Path: "newdir/a.txt"}}, NewBytes: 2})
		nB, _ := PrepareTool("write_file", DescriptorInput{WorkspaceRoot: s.root, Targets: []Target{{Path: "newdir/b.txt"}}, NewBytes: 2})
		if !plansConflict(nA, nB) {
			t.Fatal("normal mode failed to detect missing-parent contention")
		}
	})

	t.Run("mutation_04_ignore_call_identity", func(t *testing.T) {
		s, scope := testTurnService(t, Mutation{Action: "write", Path: "a.txt", Expected: MissingRevision, Content: "a"})
		_, _ = s.Begin(Begin{ID: "c4", Scope: scope, Tool: "read_file", Path: "a.txt", Fingerprint: "c4"})
		if _, err := s.NativeRead("", "a.txt"); Code(err) != "invalid_call" {
			t.Fatalf("normal mode failed to block empty callID: %v", err)
		}
		s.sched.setFaultsForTest(&mutationFaultHooks{ignoreCallIdentity: true})
		if _, err := s.NativeRead("", "a.txt"); err != nil {
			t.Fatalf("fault hook did not trigger empty-ID bypass: %v", err)
		}
		s.sched.setFaultsForTest(nil)
		_, _ = s.End("c4", "completed", "")
	})
}

func runLifecycleAndQuotaMutations(t *testing.T) {
	t.Helper()
	t.Run("mutation_05_release_before_drain", func(t *testing.T) {
		s, scope := testTurnService(t, Mutation{Action: "write", Path: "d.txt", Expected: MissingRevision, Content: "data"})
		_, _ = s.Begin(Begin{ID: "c5", Scope: scope, Tool: "read_file", Path: "d.txt", Fingerprint: "c5"})
		inIO, holdIO := make(chan struct{}), make(chan struct{})
		var ioFinished atomic.Bool
		s.SetIOHook(func(callID, _ string, eff Effect, _ string) {
			if callID == "c5" && eff == EffectRead {
				close(inIO)
				<-holdIO
				ioFinished.Store(true)
			}
		})
		defer s.SetIOHook(nil)
		go func() { _, _ = s.NativeRead("c5", "d.txt") }()
		<-inIO
		endDone := make(chan struct{})
		go func() { _, _ = s.End("c5", "completed", ""); close(endDone) }()
		select {
		case <-endDone:
			t.Fatal("End returned before in-flight child I/O drained")
		case <-time.After(60 * time.Millisecond):
		}
		close(holdIO)
		<-endDone
		if !ioFinished.Load() {
			t.Fatal("expected child I/O to drain before End completed")
		}
	})

	t.Run("mutation_06_non_atomic_quota", func(t *testing.T) {
		s, scope := testTurnService(t)
		fill := filepath.Join(s.root, "fill.bin")
		_ = os.WriteFile(fill, nil, 0600)
		_ = os.Truncate(fill, MaxWorkspaceBytes-20<<20)
		twelveMiB := int64(12 << 20)
		s.sched.setFaultsForTest(&mutationFaultHooks{nonAtomicQuotaCheck: true})
		_, e1 := s.Begin(Begin{ID: "q6-a", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "1.txt"}}, NewBytes: twelveMiB, Fingerprint: "1"})
		_, e2 := s.Begin(Begin{ID: "q6-b", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "2.txt"}}, NewBytes: twelveMiB, Fingerprint: "2"})
		s.sched.setFaultsForTest(nil)
		_, _ = s.End("q6-a", "cancelled", "")
		_, _ = s.End("q6-b", "cancelled", "")
		if e1 != nil || e2 != nil {
			t.Fatalf("expected nonAtomicQuotaCheck fault to over-allocate: %v %v", e1, e2)
		}
	})

	t.Run("mutation_07_unpinned_active_journal", func(t *testing.T) {
		s, pubScope := testService(t)
		_, _ = s.Mutate("seed7", pubScope, []Mutation{{Action: "write", Path: "p.txt", Expected: MissingRevision, Content: "v0"}})
		scope := Scope{Conversation: "c7", Turn: "t7", Policy: pubScope.Policy}
		_ = s.AcquireTurnScope(scope)
		_, _ = s.Begin(Begin{ID: "p7", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "p.txt"}}, NewBytes: 3, Fingerprint: "p7"})
		s.mu.Lock()
		for i := 1; i <= 22; i++ {
			id := fmt.Sprintf("hist-%02d", i)
			s.sequence++
			op := Operation{
				ID: id, Scope: scope, Tool: "write_file", Fingerprint: id, Sequence: s.sequence,
				Started: time.Now().UTC().Add(time.Duration(i) * time.Second), State: "completed",
				Changes: []Change{{Path: "p.txt", Before: "a", After: "b", State: "changed", Restorable: true, Content: []byte("x")}},
			}
			_, _ = s.persist(op)
			s.records[id] = publicOperation(op)
		}
		_ = s.pruneLocked()
		normalRestorable := s.records["p7"].Changes[0].Restorable
		s.sched.setFaultsForTest(&mutationFaultHooks{skipPinActiveJournals: true})
		_ = s.pruneLocked()
		faultRestorable := s.records["p7"].Changes[0].Restorable
		s.sched.setFaultsForTest(nil)
		s.mu.Unlock()
		_, _ = s.End("p7", "cancelled", "")
		if !normalRestorable || faultRestorable {
			t.Fatalf("expected normalRestorable=true and faultRestorable=false, got %v / %v", normalRestorable, faultRestorable)
		}
	})
}

func runFairnessAndSealMutations(t *testing.T) {
	t.Helper()
	t.Run("mutation_08_ignore_batch_order_fairness", func(t *testing.T) {
		s, scope := testTurnService(t, Mutation{Action: "write", Path: "f.txt", Expected: MissingRevision, Content: "0"})
		_, _ = s.Begin(Begin{ID: "hold8", Scope: scope, Tool: "read_file", Path: "f.txt", Fingerprint: "h8"})
		_, _ = s.Register(Register{ID: "w8", Batch: "b8", Order: 0, Scope: scope, Tool: "write_file", InputFingerprint: "w8"})
		_, _ = s.Register(Register{ID: "r8", Batch: "b8", Order: 1, Scope: scope, Tool: "read_file", InputFingerprint: "r8"})
		_ = s.SealBatch(SealBatch{Batch: "b8", Scope: scope, Items: []SealItem{
			{ID: "w8", Tool: "write_file", Targets: []Target{{Path: "f.txt"}}, NewBytes: 1, Fingerprint: "w8"},
			{ID: "r8", Tool: "read_file", Path: "f.txt", Fingerprint: "r8"},
		}})
		s.sched.setFaultsForTest(&mutationFaultHooks{ignoreBatchOrderFairness: true})
		res, _ := s.acquireBoundedPoll(context.Background(), "r8", "r8", 40)
		s.sched.setFaultsForTest(nil)
		if res["granted"] != true {
			t.Fatal("expected fairness fault to let reader bypass queued writer")
		}
		_, _ = s.End("r8", "completed", "")
		_ = s.CancelCall("w8", "cancelled")
		_, _ = s.End("hold8", "completed", "")
	})

	t.Run("mutation_09_global_serialization_regression", func(t *testing.T) {
		s, scope := testTurnService(t,
			Mutation{Action: "write", Path: "1.txt", Expected: MissingRevision, Content: "1"},
			Mutation{Action: "write", Path: "2.txt", Expected: MissingRevision, Content: "2"},
		)
		s.sched.setFaultsForTest(&mutationFaultHooks{forceGlobalSerialize: true})
		defer s.sched.setFaultsForTest(nil)
		_, _ = s.Begin(Begin{ID: "g1", Scope: scope, Tool: "read_file", Path: "1.txt", Fingerprint: "g1"})
		_, _ = s.Register(Register{ID: "g2", Batch: "bg", Order: 0, Scope: scope, Tool: "read_file", InputFingerprint: "g2"})
		_ = s.SealBatch(SealBatch{Batch: "bg", Scope: scope, Items: []SealItem{{ID: "g2", Tool: "read_file", Path: "2.txt", Fingerprint: "g2"}}})
		res, _ := s.acquireBoundedPoll(context.Background(), "g2", "g2", 40)
		if res["granted"] == true {
			t.Fatal("global serialize fault failed to block disjoint read")
		}
		_ = s.CancelCall("g2", "cancelled")
		_, _ = s.End("g1", "completed", "")
	})

	t.Run("mutation_10_unsealed_batch_and_post_seal_tamper", func(t *testing.T) {
		s, scope := testTurnService(t, Mutation{Action: "write", Path: "t.txt", Expected: MissingRevision, Content: "t"})
		_, _ = s.Register(Register{ID: "u10", Batch: "b10", Order: 0, Scope: scope, Tool: "read_file", InputFingerprint: "u10"})
		res, err := s.acquireBoundedPoll(context.Background(), "u10", "u10", 40)
		if err != nil || res["granted"] == true {
			t.Fatalf("unsealed batch item was granted prematurely: %+v %v", res, err)
		}
		_ = s.SealBatch(SealBatch{Batch: "b10", Scope: scope, Items: []SealItem{{ID: "u10", Tool: "read_file", Path: "t.txt", Fingerprint: "sealed-fp"}}})
		if _, err := s.acquireBoundedPoll(context.Background(), "u10", "tampered-fp", 40); Code(err) != "fingerprint_mismatch" {
			t.Fatalf("post-seal fingerprint tamper was not rejected: %v", err)
		}
		_ = s.CancelCall("u10", "cancelled")
	})
}

type randomBatchSpec struct {
	id, tool, path, val, expectErr string
}

func runRandomReplayBatch(t *testing.T, s *Service, scope Scope, rng *rand.Rand, files []string, ref map[string]string, seed int64, batchIdx int) {
	t.Helper()
	batchID := fmt.Sprintf("rb-%d-%d", seed, batchIdx)
	count := 2 + rng.Intn(4)
	specs := make([]randomBatchSpec, count)
	sealItems := make([]SealItem, count)
	seenBatchWrite := map[string]bool{}
	for order := range count {
		id := fmt.Sprintf("c-%d-%d-%d", seed, batchIdx, order)
		path := files[rng.Intn(len(files))]
		isWrite := rng.Intn(2) == 0
		tool, val, expectErr := "read_file", "", ""
		if isWrite {
			tool = "write_file"
			val = fmt.Sprintf("val-s%d-b%d-o%d", seed, batchIdx, order)
			if seenBatchWrite[pathKey(path)] {
				expectErr = "conflicting_batch_write"
			} else {
				seenBatchWrite[pathKey(path)] = true
				ref[path] = val
			}
		}
		specs[order] = randomBatchSpec{id: id, tool: tool, path: path, val: val, expectErr: expectErr}
		if _, err := s.Register(Register{ID: id, Batch: batchID, Order: order, Scope: scope, Tool: tool, InputFingerprint: id}); err != nil {
			t.Fatal(err)
		}
		if isWrite {
			sealItems[order] = SealItem{ID: id, Tool: tool, Targets: []Target{{Path: path}}, NewBytes: int64(len(val)), Fingerprint: id}
		} else {
			sealItems[order] = SealItem{ID: id, Tool: tool, Path: path, Fingerprint: id}
		}
	}
	if err := s.SealBatch(SealBatch{Batch: batchID, Scope: scope, Items: sealItems}); err != nil {
		t.Fatal(err)
	}
	executeRandomReplaySpecs(t, s, rng, specs, seed, batchIdx)
}

func executeRandomReplaySpecs(t *testing.T, s *Service, rng *rand.Rand, specs []randomBatchSpec, seed int64, batchIdx int) {
	t.Helper()
	perm := rng.Perm(len(specs))
	var wg sync.WaitGroup
	errs := make(chan error, len(specs))
	for _, idx := range perm {
		sp := specs[idx]
		wg.Add(1)
		go func(spec randomBatchSpec) {
			defer wg.Done()
			time.Sleep(time.Duration(rng.Intn(5)) * time.Millisecond)
			_, err := s.Acquire(context.Background(), spec.id, spec.id)
			if spec.expectErr != "" {
				if Code(err) != spec.expectErr {
					errs <- fmt.Errorf("%s expected %s, got %v", spec.id, spec.expectErr, err)
				}
				return
			}
			if err != nil {
				errs <- err
				return
			}
			if spec.tool == "write_file" {
				if _, err := s.Write(spec.id, spec.path, []byte(spec.val), false); err != nil {
					_, _ = s.End(spec.id, "failed", Code(err))
					errs <- err
					return
				}
			} else if _, err := s.NativeRead(spec.id, spec.path); err != nil {
				_, _ = s.End(spec.id, "failed", Code(err))
				errs <- err
				return
			}
			_, err = s.End(spec.id, "completed", "")
			errs <- err
		}(sp)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("seed %d batch %d failed: %v", seed, batchIdx, err)
		}
	}
}

func TestDeterministicRandomScheduleReplay(t *testing.T) {
	files := []string{"d1/f0.txt", "d1/f1.txt", "d2/f2.txt", "d2/f3.txt", "memory/m0.md"}
	for _, seed := range []int64{101, 202, 303, 404, 505} {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			var initMuts []Mutation
			ref := map[string]string{}
			for i, name := range files {
				initVal := fmt.Sprintf("init-%d", i)
				initMuts = append(initMuts, Mutation{Action: "write", Path: name, Expected: MissingRevision, Content: initVal})
				ref[name] = initVal
			}
			s, scope := testTurnService(t, initMuts...)
			for batchIdx := range 8 {
				runRandomReplayBatch(t, s, scope, rng, files, ref, seed, batchIdx)
			}
			s.sched.mu.Lock()
			leakedBatches := len(s.sched.batches)
			s.sched.mu.Unlock()
			if leakedBatches != 0 {
				t.Fatalf("seed %d leaked %d scheduler batch records", seed, leakedBatches)
			}
			for name, want := range ref {
				got, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(name)))
				if err != nil || string(got) != want {
					t.Fatalf("seed %d file %s = %q (%v), want %q", seed, name, got, err, want)
				}
			}
		})
	}
}
