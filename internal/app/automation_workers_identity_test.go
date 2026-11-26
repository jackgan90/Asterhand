package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/workers"
)

func TestWorkerOperationIDIsolatedAcrossCallersAndIdempotentPerCaller(t *testing.T) {
	var sentCount atomic.Int32
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			n := sentCount.Add(1)
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_" + spec.ClientCorrelationID,
				Items:        []workers.NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: spec.Prompt}},
			}
			_ = n
			return "nt_" + spec.ClientCorrelationID, ch, nil
		},
	}

	n, ws, tokA := setupM1WorkerNode(t, adapter)
	a := n.app

	// Enable a second managed caller (claude-code) with collab access.
	tr := true
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessClaudeCode,
		CollabCallerEnabled: &tr,
	}); err != nil {
		t.Fatal(err)
	}
	tokBBytes, err := os.ReadFile(filepath.Join(a.Config.Home, "harness-callers", harnesses.HarnessClaudeCode+".token"))
	if err != nil {
		t.Fatal(err)
	}
	tokB := strings.TrimSpace(string(tokBBytes))

	// 1. Two callers create workers using the EXACT same explicit operation_id.
	sharedCreateOpID := operations.NewID(time.Now())
	respCreateA := testAutomationCall(a, tokA, "workers_create", sharedCreateOpID, map[string]any{
		"harness_id":     harnesses.HarnessCodex,
		"workspace_root": ws.RootPath,
		"alias":          "caller-a-worker",
	})
	if respCreateA.Error != nil {
		t.Fatalf("caller A workers_create failed: %+v", respCreateA.Error)
	}
	wA := respCreateA.Data.(workers.Worker)

	// Same caller retry with the same operation_id must be idempotent and return the same worker.
	respCreateARetry := testAutomationCall(a, tokA, "workers_create", sharedCreateOpID, map[string]any{
		"harness_id":     harnesses.HarnessCodex,
		"workspace_root": ws.RootPath,
		"alias":          "caller-a-worker",
	})
	if respCreateARetry.Error != nil || respCreateARetry.Receipt == nil || !strings.Contains(string(respCreateARetry.Receipt.Result), wA.ID) {
		t.Fatalf("caller A workers_create retry failed: %+v", respCreateARetry)
	}

	// Caller B uses the SAME bare operation_id -> MUST create its own distinct worker!
	respCreateB := testAutomationCall(a, tokB, "workers_create", sharedCreateOpID, map[string]any{
		"harness_id":     harnesses.HarnessCodex,
		"workspace_root": ws.RootPath,
		"alias":          "caller-b-worker",
	})
	if respCreateB.Error != nil {
		t.Fatalf("caller B workers_create with same operation_id failed: %+v", respCreateB.Error)
	}
	wB := respCreateB.Data.(workers.Worker)
	if wB.ID == wA.ID {
		t.Fatalf("cross-caller workers_create collided on same worker_id %s", wA.ID)
	}
	if wA.ActorID != "harness:antigravity" || wB.ActorID != "harness:claude-code" {
		t.Fatalf("expected distinct ActorIDs recorded, got A=%q B=%q", wA.ActorID, wB.ActorID)
	}
	if wA.OperationID != sharedCreateOpID || wB.OperationID != sharedCreateOpID {
		t.Fatalf("expected public operation_id preserved on workers_create, got A=%q B=%q want=%q", wA.OperationID, wB.OperationID, sharedCreateOpID)
	}

	// 2. Two callers send turns using the EXACT same explicit operation_id.
	sharedSendOpID := operations.NewID(time.Now())
	respSendA := testAutomationCall(a, tokA, "workers_send", sharedSendOpID, map[string]any{
		"worker_id": wA.ID,
		"prompt":    "work from caller A",
	})
	if respSendA.Error != nil {
		t.Fatalf("caller A workers_send failed: %+v", respSendA.Error)
	}
	tA := respSendA.Data.(workers.Turn)
	if _, _, _, err := a.Workers.Wait(context.Background(), tA.ID, ws.ID, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	// Same caller retry executes only once.
	beforeRetry := sentCount.Load()
	respSendARetry := testAutomationCall(a, tokA, "workers_send", sharedSendOpID, map[string]any{
		"worker_id": wA.ID,
		"prompt":    "work from caller A",
	})
	if respSendARetry.Error != nil || respSendARetry.Receipt == nil || !strings.Contains(string(respSendARetry.Receipt.Result), tA.ID) {
		t.Fatalf("caller A workers_send retry failed: %+v", respSendARetry)
	}
	if sentCount.Load() != beforeRetry {
		t.Fatalf("same-caller retry executed adapter again: before=%d after=%d", beforeRetry, sentCount.Load())
	}

	// Caller B uses the SAME bare operation_id -> MUST dispatch its own turn!
	respSendB := testAutomationCall(a, tokB, "workers_send", sharedSendOpID, map[string]any{
		"worker_id": wB.ID,
		"prompt":    "work from caller B",
	})
	if respSendB.Error != nil {
		t.Fatalf("caller B workers_send with same operation_id failed: %+v", respSendB.Error)
	}
	tB := respSendB.Data.(workers.Turn)
	if tB.ID == tA.ID {
		t.Fatalf("cross-caller workers_send collided on same turn_id %s", tA.ID)
	}
	if _, _, _, err := a.Workers.Wait(context.Background(), tB.ID, ws.ID, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if sentCount.Load() != 2 {
		t.Fatalf("expected exactly 2 native turns executed (one per caller), got %d", sentCount.Load())
	}
	if tA.OperationID != sharedSendOpID || tB.OperationID != sharedSendOpID {
		t.Fatalf("expected public operation_id preserved on workers_send, got A=%q B=%q want=%q", tA.OperationID, tB.OperationID, sharedSendOpID)
	}

	// 2b. workers_get MUST return public operation_ids directly usable by operations_get,
	// while keeping the internal SQL dedup key intact across subsequent worker updates.
	getA := testAutomationCall(a, tokA, "workers_get", "", map[string]any{"worker_id": wA.ID, "workspace_id": ws.ID})
	if getA.Error != nil {
		t.Fatalf("workers_get failed: %+v", getA.Error)
	}
	viewA := getA.Data.(workerGetView)
	if viewA.Worker.OperationID != sharedCreateOpID {
		t.Fatalf("workers_get returned non-public worker operation_id %q (want %q)", viewA.Worker.OperationID, sharedCreateOpID)
	}
	if len(viewA.Turns) == 0 || viewA.Turns[0].OperationID != sharedSendOpID {
		t.Fatalf("workers_get returned non-public turn operation_id %+v (want %q)", viewA.Turns, sharedSendOpID)
	}
	if rcW := testAutomationCall(a, tokA, "operations_get", "", map[string]string{"operation_id": viewA.Worker.OperationID}); rcW.Error != nil {
		t.Fatalf("operations_get using workers_get worker.operation_id failed: %+v", rcW.Error)
	}
	if rcT := testAutomationCall(a, tokA, "operations_get", "", map[string]string{"operation_id": viewA.Turns[0].OperationID}); rcT.Error != nil {
		t.Fatalf("operations_get using workers_get turn.operation_id failed: %+v", rcT.Error)
	}

	// 3. Crash recovery with SAME bare operation_id across two callers recovers each caller's own turn.
	pA, _, _ := a.Harnesses.AuthenticateCaller(tokA)
	pB, _, _ := a.Harnesses.AuthenticateCaller(tokB)
	nodeID := a.Config.Get().NodeID
	createIdentA := operations.Identity{Actor: pA.ID, Node: nodeID, ID: sharedCreateOpID, Name: "workers_create", Target: harnesses.HarnessCodex}
	if _, found, err := a.Workers.OperationLookup("workers_create", createIdentA.NativeID()); err != nil || !found {
		t.Fatalf("expected worker SQL dedup key preserved after turn updates, found=%v err=%v", found, err)
	}
	sharedCrashOpID := operations.NewID(time.Now())
	idA := operations.Identity{Actor: pA.ID, Node: nodeID, ID: sharedCrashOpID, Name: "workers_send", Target: wA.ID, Digest: "dA"}
	idB := operations.Identity{Actor: pB.ID, Node: nodeID, ID: sharedCrashOpID, Name: "workers_send", Target: wB.ID, Digest: "dB"}
	if _, _, err := a.Store.Begin(idA, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Store.Begin(idB, time.Now()); err != nil {
		t.Fatal(err)
	}
	crashTurnA, err := a.Workers.Send(context.Background(), workers.SendInput{
		OperationID: sharedCrashOpID, OperationKey: idA.NativeID(), ActorID: pA.ID, CallerID: pA.CallerID, WorkerID: wA.ID, Prompt: "crash A",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = a.Workers.Wait(context.Background(), crashTurnA.ID, ws.ID, 3*time.Second)

	// Caller B's prepared receipt MUST stay prepared (not steal Caller A's turn!).
	recB := testAutomationCall(a, tokB, "operations_get", "", map[string]string{"operation_id": sharedCrashOpID})
	if recB.Error != nil {
		t.Fatalf("caller B operations_get error: %+v", recB.Error)
	}
	if gotB := recB.Data.(operations.Receipt); gotB.State != "prepared" {
		t.Fatalf("caller B falsely recovered caller A's turn: %+v", gotB)
	}

	// Caller A recovers its own turn.
	recA := testAutomationCall(a, tokA, "operations_get", "", map[string]string{"operation_id": sharedCrashOpID})
	if recA.Error != nil {
		t.Fatalf("caller A operations_get error: %+v", recA.Error)
	}
	if gotA := recA.Data.(operations.Receipt); gotA.State != "completed" || !strings.Contains(string(gotA.Result), crashTurnA.ID) {
		t.Fatalf("caller A failed to recover own turn %s: %+v", crashTurnA.ID, gotA)
	}

	// 4. Legacy bare-ID fallback: verified owner recovers, non-owner is rejected.
	legacyOpID := operations.NewID(time.Now())
	legacyTurnA, err := a.Workers.Send(context.Background(), workers.SendInput{
		OperationID: legacyOpID,
		CallerID:    harnesses.HarnessAntigravity,
		WorkerID:    wA.ID,
		Prompt:      "legacy bare op_id turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = a.Workers.Wait(context.Background(), legacyTurnA.ID, ws.ID, 3*time.Second)

	legA := operations.Identity{Actor: pA.ID, Node: nodeID, ID: legacyOpID, Name: "workers_send", Target: wA.ID, Digest: "legA"}
	legB := operations.Identity{Actor: pB.ID, Node: nodeID, ID: legacyOpID, Name: "workers_send", Target: wA.ID, Digest: "legB"}
	if _, _, err := a.Store.Begin(legA, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Store.Begin(legB, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Caller B must NOT claim Caller A's legacy bare-ID turn even with matching worker_id.
	legRespB := testAutomationCall(a, tokB, "operations_get", "", map[string]string{"operation_id": legacyOpID})
	if legRespB.Error != nil {
		t.Fatalf("legacy caller B operations_get error: %+v", legRespB.Error)
	}
	if gotLegB := legRespB.Data.(operations.Receipt); gotLegB.State != "prepared" {
		t.Fatalf("caller B falsely claimed caller A's legacy turn: %+v", gotLegB)
	}

	// Caller A has matching CallerID == "antigravity" -> safely recovers!
	legRespA := testAutomationCall(a, tokA, "operations_get", "", map[string]string{"operation_id": legacyOpID})
	if legRespA.Error != nil {
		t.Fatalf("legacy caller A operations_get error: %+v", legRespA.Error)
	}
	if gotLegA := legRespA.Data.(operations.Receipt); gotLegA.State != "completed" || !strings.Contains(string(gotLegA.Result), legacyTurnA.ID) {
		t.Fatalf("caller A failed to recover own verified legacy turn: %+v", gotLegA)
	}

	// Unattributed legacy workers_create (ActorID == "") and unattributed legacy
	// workers_send (ActorID == "" && CallerID == "") MUST stay prepared even after
	// 31-day receipt retention pruning removes another actor's terminal receipt.
	retCreateID := operations.NewID(time.Now())
	if _, err := a.Workers.Create(context.Background(), workers.CreateInput{
		OperationID:   retCreateID,
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath,
		Alias:         "legacy-unattributed",
	}); err != nil {
		t.Fatal(err)
	}
	baseNow := time.Now()
	rcA, _, err := a.Store.Begin(operations.Identity{Actor: pA.ID, Node: nodeID, ID: retCreateID, Name: "workers_create", Target: harnesses.HarnessCodex, Digest: "cA"}, baseNow)
	if err != nil {
		t.Fatal(err)
	}
	rcA.State, rcA.Accepted = "completed", true
	if err := a.Store.Finish(rcA, baseNow); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Store.Begin(operations.Identity{Actor: pB.ID, Node: nodeID, ID: retCreateID, Name: "workers_create", Target: harnesses.HarnessCodex, Digest: "cB"}, baseNow); err != nil {
		t.Fatal(err)
	}
	// Advance clock by 31 days and trigger normal terminal receipt pruning.
	futureNow := baseNow.Add(31 * 24 * time.Hour)
	if _, _, err := a.Store.Begin(operations.Identity{Actor: pA.ID, Node: nodeID, ID: operations.NewID(futureNow), Name: "workers_create", Target: harnesses.HarnessCodex, Digest: "prune"}, futureNow); err != nil {
		t.Fatal(err)
	}
	afterPruneResp := testAutomationCall(a, tokB, "operations_get", "", map[string]string{"operation_id": retCreateID})
	if afterPruneResp.Error != nil {
		t.Fatalf("post-retention legacy create lookup error: %+v", afterPruneResp.Error)
	}
	if got := afterPruneResp.Data.(operations.Receipt); got.State != "prepared" {
		t.Fatalf("unattributed legacy worker must stay prepared even after 31-day retention pruning, got %+v", got)
	}

	// Unattributed legacy turn (empty ActorID and empty CallerID) must also stay prepared.
	unattrTurnID := operations.NewID(time.Now())
	unattrTurn, err := a.Workers.Send(context.Background(), workers.SendInput{
		OperationID: unattrTurnID,
		WorkerID:    wA.ID,
		Prompt:      "unattributed legacy turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = a.Workers.Wait(context.Background(), unattrTurn.ID, ws.ID, 3*time.Second)
	localActorID := "asterhand-local-user"
	if _, _, err := a.Store.Begin(operations.Identity{Actor: localActorID, Node: nodeID, ID: unattrTurnID, Name: "workers_send", Target: wA.ID, Digest: "uT"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	unattrRec, err := a.Store.Lookup(localActorID, nodeID, unattrTurnID)
	if err != nil {
		t.Fatal(err)
	}
	reconciledUnattr, err := a.reconcileAutomationOperation(unattrRec)
	if err != nil {
		t.Fatal(err)
	}
	if reconciledUnattr.State != "prepared" {
		t.Fatalf("unattributed legacy turn without ActorID/CallerID must stay prepared, got %+v", reconciledUnattr)
	}
	_ = json.RawMessage(nil)
}
