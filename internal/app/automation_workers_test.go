package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/harnesses"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/workers"
)

type stubWorkerAdapter struct {
	mu      sync.Mutex
	startFn func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error)
}

func (s *stubWorkerAdapter) HarnessID() string { return harnesses.HarnessCodex }
func (s *stubWorkerAdapter) OpenThread(ctx context.Context, spec workers.ThreadOpenSpec) (workers.ThreadHandle, error) {
	sid := spec.NativeSessionID
	if sid == "" {
		sid = "thr_" + spec.WorkerID
	}
	return workers.ThreadHandle{Key: spec.WorkerID + ":" + sid, NativeSessionID: sid}, nil
}
func (s *stubWorkerAdapter) StartTurn(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
	if s.startFn != nil {
		return s.startFn(ctx, handle, spec, onEvent)
	}
	ch := make(chan workers.TurnOutcome, 1)
	ch <- workers.TurnOutcome{
		State:        workers.TurnCompleted,
		NativeTurnID: "nt_1",
		Items: []workers.NativeItem{
			{ItemID: "c1", Type: "agentMessage", Phase: "commentary", Text: "Reading code"},
			{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: "Finding: add context cancellation check"},
		},
	}
	return "nt_1", ch, nil
}
func (s *stubWorkerAdapter) InterruptTurn(ctx context.Context, handle workers.ThreadHandle, nativeTurnID string) error {
	return nil
}
func (s *stubWorkerAdapter) ForceKill(handle workers.ThreadHandle) error { return nil }
func (s *stubWorkerAdapter) InspectThreadTurns(ctx context.Context, handle workers.ThreadHandle) ([]workers.NativeTurnRecord, error) {
	return nil, nil
}
func (s *stubWorkerAdapter) CloseThread(handle workers.ThreadHandle) error { return nil }

func setupM1WorkerNode(t *testing.T, adapter workers.Adapter) (*testNode, harnesses.DelegatedWorkspace, string) {
	t.Helper()
	n := newNode(t, &fakeExecutor{})
	a := n.app
	// Replace workers service with stub adapter for deterministic app-level integration tests.
	if a.Workers != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = a.Workers.Close(ctx)
		cancel()
	}
	wsSvc, err := workers.NewService(a.Config.Home, a.Harnesses, adapter)
	if err != nil {
		t.Fatal(err)
	}
	a.Workers = wsSvc

	wsDir := t.TempDir()
	ws, _, err := a.Harnesses.ResolveWorkspace(wsDir, harnesses.AccessReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	fakeCodex := filepath.Join(a.Config.Home, "codex.exe")
	if err := os.WriteFile(fakeCodex, []byte("stub"), 0600); err != nil {
		t.Fatal(err)
	}
	tr := true
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessCodex,
		CollabWorkerEnabled: &tr,
		ManualPath:          &fakeCodex,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		GeneralToolsEnabled: &tr,
		CollabCallerEnabled: &tr,
	}); err != nil {
		t.Fatal(err)
	}
	tok, err := a.Harnesses.CallerTokenForTest(harnesses.HarnessAntigravity)
	if err != nil {
		t.Fatal(err)
	}
	return n, ws, tok
}

func TestM1EndToEndWorkerFlowHotSwitchMidFlightReceiptRevocationAndStopChain(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{}, 2)
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			if strings.Contains(spec.Prompt, "hold") {
				started <- struct{}{}
				ch := make(chan workers.TurnOutcome, 1)
				go func() {
					<-gate
					ch <- workers.TurnOutcome{
						State:        workers.TurnCompleted,
						NativeTurnID: "nt_hold",
						Items:        []workers.NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: "Hold complete"}},
					}
				}()
				return "nt_hold", ch, nil
			}
			ch := make(chan workers.TurnOutcome, 1)
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_fast",
				Items:        []workers.NativeItem{{ItemID: "f", Type: "agentMessage", Phase: "final_answer", Text: "Finding: nil check missing"}},
			}
			return "nt_fast", ch, nil
		},
	}
	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app

	// 1. Verify restricted Principal recognized on capabilities.
	capResp := testAutomationCall(a, tok, "capabilities", "", automation.Empty{})
	if capResp.Error != nil {
		t.Fatalf("capabilities failed: %+v", capResp.Error)
	}
	capMap := capResp.Data.(map[string]any)
	if capMap["caller_id"] != harnesses.HarnessAntigravity {
		t.Fatalf("expected caller_id=antigravity, got %+v", capMap["caller_id"])
	}

	// 2. Create worker + send turn + wait + result + logs.
	createID := operations.NewID(time.Now())
	createResp := testAutomationCall(a, tok, "workers.create", createID, workerCreateInput{
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath,
		Alias:         "codex-reviewer",
		AccessMode:    harnesses.AccessReadOnly,
	})
	if createResp.Error != nil {
		t.Fatalf("workers.create failed: %+v", createResp.Error)
	}
	w := createResp.Data.(workers.Worker)

	sendID := operations.NewID(time.Now())
	sendResp := testAutomationCall(a, tok, "workers.send", sendID, workerSendInput{
		WorkerID:    w.ID,
		WorkspaceID: ws.ID,
		Prompt:      "review plan",
	})
	if sendResp.Error != nil {
		t.Fatalf("workers.send failed: %+v", sendResp.Error)
	}
	turn := sendResp.Data.(workers.Turn)

	waitResp := testAutomationCall(a, tok, "workers.wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: 5})
	if waitResp.Error != nil {
		t.Fatalf("workers.wait failed: %+v", waitResp.Error)
	}
	wv := waitResp.Data.(workerWaitView)
	if wv.State != workers.TurnCompleted || !strings.Contains(wv.Answer, "Finding: nil check missing") {
		t.Fatalf("unexpected wait view: %+v", wv)
	}

	// 3. Also create a general receipt (e.g. commands.create) to test general vs collab receipt filtering.
	genID := operations.NewID(time.Now())
	genResp := testAutomationCall(a, tok, "commands.create", genID, automation.CommandInput{
		Name:  "demo_cmd",
		Graph: apiTestGraph(filepath.Join(t.TempDir(), "s.ps1")),
	})
	if genResp.Error != nil {
		t.Fatalf("commands.create failed: %+v", genResp.Error)
	}

	// 4. Hot-switch Antigravity from general+collab -> general-only:
	//    - commands.list still works
	//    - workers.send rejected with toolset_disabled
	//    - operations.get rejects old collab receipt (sendID), still allows general receipt (genID)!
	fl := false
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		CollabCallerEnabled: &fl,
	}); err != nil {
		t.Fatal(err)
	}
	if r := testAutomationCall(a, tok, "commands.list", "", automation.CommandList{}); r.Error != nil {
		t.Fatalf("general op should still succeed: %+v", r.Error)
	}
	if r := testAutomationCall(a, tok, "workers.send", operations.NewID(time.Now()), workerSendInput{WorkerID: w.ID, Prompt: "blocked"}); r.Error == nil || r.Error.Code != "toolset_disabled" {
		t.Fatalf("expected toolset_disabled on workers.send, got %+v", r.Error)
	}
	if r := testAutomationCall(a, tok, "operations.get", "", map[string]string{"operation_id": sendID}); r.Error == nil || r.Error.Code != "toolset_disabled" {
		t.Fatalf("expected toolset_disabled reading old collab receipt, got %+v", r.Error)
	}
	if r := testAutomationCall(a, tok, "operations.get", "", map[string]string{"operation_id": genID}); r.Error != nil {
		t.Fatalf("general receipt should still be readable: %+v", r.Error)
	}

	// 5. Mid-flight revocation during operations.get: re-enable collab, then wrap Authorize so
	//    the second Authorize() call (right before returning!) sees collab turned off -> rejected!
	tr := true
	_, _ = a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		CollabCallerEnabled: &tr,
	})
	origAuth := a.Automation.Registry.Authorize
	calls := 0
	a.Automation.Registry.Authorize = func(tkn string) (automation.Principal, error) {
		p, err := origAuth(tkn)
		if err != nil {
			return p, err
		}
		calls++
		// On the 3rd authorize inside operations.get (the pre-return check), simulate collab turned off mid-flight!
		if calls >= 3 && p.Toolsets != nil {
			clone := map[string]bool{}
			for k, v := range p.Toolsets {
				if k != automation.ToolsetCollab {
					clone[k] = v
				}
			}
			p.Toolsets = clone
		}
		return p, nil
	}
	midResp := testAutomationCall(a, tok, "operations.get", "", map[string]string{"operation_id": sendID})
	a.Automation.Registry.Authorize = origAuth
	if midResp.Error == nil || midResp.Error.Code != "toolset_disabled" {
		t.Fatalf("expected mid-flight revocation to reject collab receipt before returning, got %+v", midResp)
	}

	// 6. Crash window recovery: worker turn persisted, public receipt still prepared -> operations.get repairs receipt!
	crashOpID := operations.NewID(time.Now())
	crashTurn, err := a.Workers.Send(context.Background(), workers.SendInput{
		OperationID: crashOpID,
		WorkerID:    w.ID,
		Prompt:      "hold for stop test",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	pCur, _, _ := a.Harnesses.AuthenticateCaller(tok)
	ident := operations.Identity{Actor: pCur.ID, Node: a.Config.Get().NodeID, ID: crashOpID, Name: "workers.send", Target: w.ID, Digest: "d"}
	if _, _, err := a.Store.Begin(ident, time.Now()); err != nil {
		t.Fatal(err)
	}
	recResp := testAutomationCall(a, tok, "operations.get", "", map[string]string{"operation_id": crashOpID})
	if recResp.Error != nil {
		t.Fatalf("expected receipt repaired from workers domain, got %+v", recResp.Error)
	}
	repaired := recResp.Data.(operations.Receipt)
	if repaired.State != "completed" || !strings.Contains(string(repaired.Result), crashTurn.ID) {
		t.Fatalf("expected repaired receipt containing %s, got %+v (%s)", crashTurn.ID, repaired, string(repaired.Result))
	}

	// 7. Stop chain: while active worker turn exists, RequestStop(false) MUST return false!
	if a.RequestStop(false) {
		t.Fatal("expected RequestStop(false) to report busy while active worker turn is running")
	}

	// Finish held turn, revoke entire caller credential -> old token immediately fails!
	close(gate)
	_, _, _, _ = a.Workers.Wait(context.Background(), crashTurn.ID, ws.ID, 5*time.Second)

	if _, err := a.Harnesses.RevokeCallerCredential(harnesses.HarnessAntigravity); err != nil {
		t.Fatal(err)
	}
	if r := testAutomationCall(a, tok, "capabilities", "", automation.Empty{}); r.Error == nil || r.Error.Code != "forbidden" {
		t.Fatalf("expected revoked credential forbidden, got %+v", r.Error)
	}
	_ = json.RawMessage(nil)
}

func TestM1BatchRevocationAndRestrictedStartBlock(t *testing.T) {
	n, ws, tok := setupM1WorkerNode(t, &stubWorkerAdapter{})
	a := n.app

	// 1. read.many with a general read + collab read:
	//    both succeed when general+collab enabled; disabling collab rejects collab item with toolset_disabled!
	rmBoth := testAutomationCall(a, tok, "read.many", "", automation.MultiRead{
		Items: []automation.BatchItem{
			{Operation: "commands.list", Arguments: json.RawMessage(`{}`)},
			{Operation: "workers.list", Arguments: json.RawMessage(`{"workspace_id":"` + ws.ID + `"}`)},
		},
	})
	if rmBoth.Error != nil {
		t.Fatalf("read.many failed: %+v", rmBoth.Error)
	}
	fl := false
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		CollabCallerEnabled: &fl,
	}); err != nil {
		t.Fatal(err)
	}
	rmGenOnly := testAutomationCall(a, tok, "read.many", "", automation.MultiRead{
		Items: []automation.BatchItem{
			{Operation: "commands.list", Arguments: json.RawMessage(`{}`)},
			{Operation: "workers.list", Arguments: json.RawMessage(`{"workspace_id":"` + ws.ID + `"}`)},
		},
	})
	if rmGenOnly.Error != nil {
		t.Fatalf("read.many top-level error: %+v", rmGenOnly.Error)
	}
	rmItems := rmGenOnly.Data.(map[string]any)["items"].([]any)
	item0 := rmItems[0].(map[string]any)["response"].(automation.Response)
	item1 := rmItems[1].(map[string]any)["response"].(automation.Response)
	if item0.Error != nil || item1.Error == nil || item1.Error.Code != "toolset_disabled" {
		t.Fatalf("expected item0 ok and item1 toolset_disabled, got item0=%+v item1=%+v", item0.Error, item1.Error)
	}

	// 2. batches.submit (general write) + batches.get:
	//    when general_tools_enabled is turned off while collab_caller_enabled is turned on,
	//    batches.get masks the general item as state="forbidden" (and mid-flight revocation also masks it).
	tr := true
	_, _ = a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		GeneralToolsEnabled: &tr,
		CollabCallerEnabled: &tr,
	})
	cmdArgs, _ := json.Marshal(automation.CommandInput{
		Name:  "batch_demo_cmd",
		Graph: apiTestGraph(filepath.Join(t.TempDir(), "b.ps1")),
	})
	batchID := operations.NewID(time.Now())
	subResp := testAutomationCall(a, tok, "batches.submit", batchID, automation.BatchSubmit{
		Items: []automation.BatchItem{
			{Operation: "commands.create", Arguments: cmdArgs},
		},
	})
	if subResp.Error != nil {
		t.Fatalf("batches.submit failed: %+v", subResp.Error)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		getResp := testAutomationCall(a, tok, "batches.get", "", automation.BatchGet{ID: batchID})
		if getResp.Error != nil {
			t.Fatalf("batches.get failed: %+v", getResp.Error)
		}
		if getResp.Data.(map[string]any)["complete"] == true {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Turn off general_tools_enabled while keeping collab_caller_enabled=true -> batch item must be masked as forbidden!
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		GeneralToolsEnabled: &fl,
	}); err != nil {
		t.Fatal(err)
	}
	maskedResp := testAutomationCall(a, tok, "batches.get", "", automation.BatchGet{ID: batchID})
	if maskedResp.Error != nil {
		t.Fatalf("batches.get should succeed with masked item, got %+v", maskedResp.Error)
	}
	maskedItems := maskedResp.Data.(map[string]any)["items"].([]any)
	m0 := maskedItems[0].(map[string]string)
	if m0["state"] != "forbidden" {
		t.Fatalf("expected general batch item masked as forbidden, got %+v", m0)
	}

	// Mid-flight revocation during batches.get (right before returning).
	_, _ = a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		GeneralToolsEnabled: &tr,
	})
	origAuth := a.Automation.Registry.Authorize
	calls := 0
	a.Automation.Registry.Authorize = func(tkn string) (automation.Principal, error) {
		p, err := origAuth(tkn)
		if err != nil {
			return p, err
		}
		calls++
		if calls >= 3 && p.Toolsets != nil {
			clone := map[string]bool{}
			for k, v := range p.Toolsets {
				if k != automation.ToolsetGeneral {
					clone[k] = v
				}
			}
			p.Toolsets = clone
		}
		return p, nil
	}
	midBatch := testAutomationCall(a, tok, "batches.get", "", automation.BatchGet{ID: batchID})
	a.Automation.Registry.Authorize = origAuth
	if midBatch.Error != nil {
		t.Fatalf("mid-flight batches.get unexpected top error: %+v", midBatch.Error)
	}
	midItems := midBatch.Data.(map[string]any)["items"].([]any)
	mid0 := midItems[0].(map[string]string)
	if mid0["state"] != "forbidden" {
		t.Fatalf("expected mid-flight revocation to mask batch item before returning, got %+v", mid0)
	}
}
