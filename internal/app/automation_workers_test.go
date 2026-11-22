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
	a.Harnesses.AntigravityConfigPath = filepath.Join(a.Config.Home, "ag_mcp.json")
	a.Harnesses.ClaudeConfigPath = filepath.Join(a.Config.Home, "claude.json")
	a.Harnesses.DisableVersionProbe = true
	a.Harnesses.CmdRunner = func(ctx context.Context, exe string, args ...string) (string, string, error) {
		return "", "No MCP server named 'asterhand' found.", os.ErrNotExist
	}
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
	createResp := testAutomationCall(a, tok, "workers_create", createID, workerCreateInput{
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath,
		Alias:         "codex-reviewer",
		AccessMode:    harnesses.AccessReadOnly,
	})
	if createResp.Error != nil {
		t.Fatalf("workers_create failed: %+v", createResp.Error)
	}
	w := createResp.Data.(workers.Worker)

	sendID := operations.NewID(time.Now())
	sendResp := testAutomationCall(a, tok, "workers_send", sendID, workerSendInput{
		WorkerID:    w.ID,
		WorkspaceID: ws.ID,
		Prompt:      "review plan",
	})
	if sendResp.Error != nil {
		t.Fatalf("workers_send failed: %+v", sendResp.Error)
	}
	turn := sendResp.Data.(workers.Turn)

	waitResp := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})
	if waitResp.Error != nil {
		t.Fatalf("workers_wait failed: %+v", waitResp.Error)
	}
	wv := waitResp.Data.(workerWaitView)
	if wv.State != workers.TurnCompleted || !strings.Contains(wv.Answer, "Finding: nil check missing") {
		t.Fatalf("unexpected wait view: %+v", wv)
	}

	// 3. Also create a general receipt (e.g. commands_create) to test general vs collab receipt filtering.
	genID := operations.NewID(time.Now())
	genResp := testAutomationCall(a, tok, "commands_create", genID, automation.CommandInput{
		Name:  "demo_cmd",
		Graph: apiTestGraph(filepath.Join(t.TempDir(), "s.ps1")),
	})
	if genResp.Error != nil {
		t.Fatalf("commands_create failed: %+v", genResp.Error)
	}

	// 4. Hot-switch Antigravity from general+collab -> general-only:
	//    - commands_list still works
	//    - workers_send rejected with toolset_disabled
	//    - operations_get rejects old collab receipt (sendID), still allows general receipt (genID)!
	fl := false
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		CollabCallerEnabled: &fl,
	}); err != nil {
		t.Fatal(err)
	}
	if r := testAutomationCall(a, tok, "commands_list", "", automation.CommandList{}); r.Error != nil {
		t.Fatalf("general op should still succeed: %+v", r.Error)
	}
	if r := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{WorkerID: w.ID, Prompt: "blocked"}); r.Error == nil || r.Error.Code != "toolset_disabled" {
		t.Fatalf("expected toolset_disabled on workers_send, got %+v", r.Error)
	}
	if r := testAutomationCall(a, tok, "operations_get", "", map[string]string{"operation_id": sendID}); r.Error == nil || r.Error.Code != "toolset_disabled" {
		t.Fatalf("expected toolset_disabled reading old collab receipt, got %+v", r.Error)
	}
	if r := testAutomationCall(a, tok, "operations_get", "", map[string]string{"operation_id": genID}); r.Error != nil {
		t.Fatalf("general receipt should still be readable: %+v", r.Error)
	}

	// 5. Mid-flight revocation during operations_get: re-enable collab, then wrap Authorize so
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
		// On the 3rd authorize inside operations_get (the pre-return check), simulate collab turned off mid-flight!
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
	midResp := testAutomationCall(a, tok, "operations_get", "", map[string]string{"operation_id": sendID})
	a.Automation.Registry.Authorize = origAuth
	if midResp.Error == nil || midResp.Error.Code != "toolset_disabled" {
		t.Fatalf("expected mid-flight revocation to reject collab receipt before returning, got %+v", midResp)
	}

	// 6. Crash window recovery: worker turn persisted, public receipt still prepared -> operations_get repairs receipt!
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
	ident := operations.Identity{Actor: pCur.ID, Node: a.Config.Get().NodeID, ID: crashOpID, Name: "workers_send", Target: w.ID, Digest: "d"}
	if _, _, err := a.Store.Begin(ident, time.Now()); err != nil {
		t.Fatal(err)
	}
	recResp := testAutomationCall(a, tok, "operations_get", "", map[string]string{"operation_id": crashOpID})
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

	// 1. read_many with a general read + collab read:
	//    both succeed when general+collab enabled; disabling collab rejects collab item with toolset_disabled!
	rmBoth := testAutomationCall(a, tok, "read_many", "", automation.MultiRead{
		Items: []automation.BatchItem{
			{Operation: "commands_list", Arguments: json.RawMessage(`{}`)},
			{Operation: "workers_list", Arguments: json.RawMessage(`{"workspace_id":"` + ws.ID + `"}`)},
		},
	})
	if rmBoth.Error != nil {
		t.Fatalf("read_many failed: %+v", rmBoth.Error)
	}
	fl := false
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		CollabCallerEnabled: &fl,
	}); err != nil {
		t.Fatal(err)
	}
	rmGenOnly := testAutomationCall(a, tok, "read_many", "", automation.MultiRead{
		Items: []automation.BatchItem{
			{Operation: "commands_list", Arguments: json.RawMessage(`{}`)},
			{Operation: "workers_list", Arguments: json.RawMessage(`{"workspace_id":"` + ws.ID + `"}`)},
		},
	})
	if rmGenOnly.Error != nil {
		t.Fatalf("read_many top-level error: %+v", rmGenOnly.Error)
	}
	rmItems := rmGenOnly.Data.(map[string]any)["items"].([]any)
	item0 := rmItems[0].(map[string]any)["response"].(automation.Response)
	item1 := rmItems[1].(map[string]any)["response"].(automation.Response)
	if item0.Error != nil || item1.Error == nil || item1.Error.Code != "toolset_disabled" {
		t.Fatalf("expected item0 ok and item1 toolset_disabled, got item0=%+v item1=%+v", item0.Error, item1.Error)
	}

	// 2. batches_submit (general write) + batches_get:
	//    when general_tools_enabled is turned off while collab_caller_enabled is turned on,
	//    batches_get masks the general item as state="forbidden" (and mid-flight revocation also masks it).
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
	subResp := testAutomationCall(a, tok, "batches_submit", batchID, automation.BatchSubmit{
		Items: []automation.BatchItem{
			{Operation: "commands_create", Arguments: cmdArgs},
		},
	})
	if subResp.Error != nil {
		t.Fatalf("batches_submit failed: %+v", subResp.Error)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		getResp := testAutomationCall(a, tok, "batches_get", "", automation.BatchGet{ID: batchID})
		if getResp.Error != nil {
			t.Fatalf("batches_get failed: %+v", getResp.Error)
		}
		if getResp.Data.(map[string]any)["complete"] == true {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Turn off general_tools_enabled while keeping collab_caller_enabled=true -> batches_get is paired with batches_submit in general toolset and immediately rejected!
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		GeneralToolsEnabled: &fl,
	}); err != nil {
		t.Fatal(err)
	}
	maskedResp := testAutomationCall(a, tok, "batches_get", "", automation.BatchGet{ID: batchID})
	if maskedResp.Error == nil || maskedResp.Error.Code != "toolset_disabled" {
		t.Fatalf("expected batches_get rejected with toolset_disabled when general is off, got %+v", maskedResp.Error)
	}

	// Mid-flight revocation during batches_get (right before returning).
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
	midBatch := testAutomationCall(a, tok, "batches_get", "", automation.BatchGet{ID: batchID})
	a.Automation.Registry.Authorize = origAuth
	if midBatch.Error == nil {
		midItems := midBatch.Data.(map[string]any)["items"].([]any)
		mid0 := midItems[0].(map[string]string)
		if mid0["state"] != "forbidden" {
			t.Fatalf("expected mid-flight revocation to mask or deny batch item before returning, got %+v", mid0)
		}
	} else if midBatch.Error.Code != "toolset_disabled" {
		t.Fatalf("mid-flight batches_get unexpected top error: %+v", midBatch.Error)
	}
}

func TestM2HarnessesDiscoveryOpsAndWorkspaceWritePolicy(t *testing.T) {
	n, ws, tok := setupM1WorkerNode(t, &stubWorkerAdapter{})
	a := n.app

	// 1. harnesses_list & harnesses_get succeed under collab toolset and never leak secrets.
	listResp := testAutomationCall(a, tok, "harnesses_list", "", harnessesListInput{})
	if listResp.Error != nil {
		t.Fatalf("harnesses_list failed: %+v", listResp.Error)
	}
	listData := listResp.Data.(harnessesListView)
	if len(listData.Items) != 4 {
		t.Fatalf("expected all 4 known harnesses, got %d", len(listData.Items))
	}
	rawJSON, _ := json.Marshal(listData)
	if strings.Contains(string(rawJSON), tok) || strings.Contains(string(rawJSON), "credential_hash") {
		t.Fatal("harnesses_list leaked credential secret or hash")
	}

	getResp := testAutomationCall(a, tok, "harnesses_get", "", harnessesGetInput{HarnessID: harnesses.HarnessCodex})
	if getResp.Error != nil {
		t.Fatalf("harnesses_get codex failed: %+v", getResp.Error)
	}
	codexView := getResp.Data.(harnesses.HarnessView)
	if codexView.ID != harnesses.HarnessCodex || !codexView.Capabilities.CallerManaged || !codexView.Capabilities.WorkerManaged {
		t.Fatalf("unexpected codex view: %+v", codexView)
	}
	if badResp := testAutomationCall(a, tok, "harnesses_get", "", harnessesGetInput{HarnessID: "unknown"}); badResp.Error == nil || badResp.Error.Code != "not_found" {
		t.Fatalf("expected not_found for unknown harness, got %+v", badResp.Error)
	}

	// 2. workspace_write via workers_create is blocked until allow_workspace_write=true on Codex.
	op1 := operations.NewIDFor("workers_create", time.Now())
	denied := testAutomationCall(a, tok, "workers_create", op1, workerCreateInput{
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath,
		Alias:         "codex-writer",
		AccessMode:    harnesses.AccessWorkspaceWrite,
	})
	if denied.Error == nil || denied.Error.Code != "forbidden" {
		t.Fatalf("expected workspace_write forbidden before opt-in, got %+v", denied.Error)
	}

	tr, fl := true, false
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessCodex,
		AllowWorkspaceWrite: &tr,
	}); err != nil {
		t.Fatal(err)
	}
	op2 := operations.NewIDFor("workers_create", time.Now())
	created := testAutomationCall(a, tok, "workers_create", op2, workerCreateInput{
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath,
		Alias:         "codex-writer",
		AccessMode:    harnesses.AccessWorkspaceWrite,
	})
	if created.Error != nil {
		t.Fatalf("expected workspace_write workers_create to succeed after opt-in: %+v", created.Error)
	}

	// 3. Turning off collab_caller_enabled immediately blocks harnesses_list & harnesses_get!
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		CollabCallerEnabled: &fl,
	}); err != nil {
		t.Fatal(err)
	}
	if r := testAutomationCall(a, tok, "harnesses_list", "", harnessesListInput{}); r.Error == nil || r.Error.Code != "toolset_disabled" {
		t.Fatalf("expected toolset_disabled on harnesses_list, got %+v", r.Error)
	}
	if r := testAutomationCall(a, tok, "harnesses_get", "", harnessesGetInput{HarnessID: harnesses.HarnessCodex}); r.Error == nil || r.Error.Code != "toolset_disabled" {
		t.Fatalf("expected toolset_disabled on harnesses_get, got %+v", r.Error)
	}

	// 4. workers_create schema explicitly exposes read_only & workspace_write enum and description.
	var createDef *automation.Definition
	for _, d := range AutomationDefinitions() {
		if d.Name == "workers_create" {
			dd := d
			createDef = &dd
			break
		}
	}
	if createDef == nil {
		t.Fatal("workers_create definition missing")
	}
	props := createDef.Schema["properties"].(map[string]any)
	accessProp := props["access_mode"].(map[string]any)
	desc, _ := accessProp["description"].(string)
	enumVals, _ := accessProp["enum"].([]string)
	if !strings.Contains(desc, "workspace_write") || len(enumVals) != 2 || enumVals[0] != "read_only" || enumVals[1] != "workspace_write" {
		t.Fatalf("expected access_mode schema enum & description with workspace_write, got desc=%q enum=%v", desc, enumVals)
	}

	// 5. capabilities.limits exposes worker execution budgets and collab-only hides batches_get.
	if _, err := a.Harnesses.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessAntigravity,
		GeneralToolsEnabled: &fl,
		CollabCallerEnabled: &tr,
	}); err != nil {
		t.Fatal(err)
	}
	capResp := testAutomationCall(a, tok, "capabilities", "", automation.Empty{})
	if capResp.Error != nil {
		t.Fatalf("capabilities failed: %+v", capResp.Error)
	}
	capData := capResp.Data.(map[string]any)
	limits := capData["limits"].(map[string]any)
	if limits["worker_wall_clock_seconds"] != 1800 || limits["worker_max_wall_clock_seconds"] != 3600 || limits["worker_stall_seconds"] != 600 || limits["worker_max_active_turns"] != 4 {
		t.Fatalf("unexpected worker budgets in capabilities.limits: %+v", limits)
	}
	for _, op := range capData["operations"].([]map[string]any) {
		name := op["name"].(string)
		if name == "batches_get" || name == "batches_submit" {
			t.Fatalf("collab-only capabilities must not advertise %s", name)
		}
		if strings.Contains(name, ".") {
			t.Fatalf("operation name must use underscore, got %s", name)
		}
	}

	// 6. Strict bounds validation on workers_wait.wait_seconds, workers_list.limit,
	//    workers_logs.limit, and workers_result.max_bytes (0 and negatives rejected).
	for _, badWait := range []int{0, -1, 31} {
		r := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: "turn_missing", WaitSeconds: automation.Int(badWait)})
		if r.Error == nil || r.Error.Code != "invalid_parameters" || !strings.Contains(r.Error.Message, "between 1 and 30") {
			t.Fatalf("expected range invalid_parameters for wait_seconds=%d, got %+v", badWait, r.Error)
		}
	}
	for _, badLimit := range []int{0, -1, 201} {
		r := testAutomationCall(a, tok, "workers_list", "", workerListInput{Limit: automation.Int(badLimit)})
		if r.Error == nil || r.Error.Code != "invalid_parameters" || !strings.Contains(r.Error.Message, "between 1 and 200") {
			t.Fatalf("expected range invalid_parameters for workers_list limit=%d, got %+v", badLimit, r.Error)
		}
		rl := testAutomationCall(a, tok, "workers_logs", "", workerLogsInput{TurnID: "turn_missing", Limit: automation.Int(badLimit)})
		if rl.Error == nil || rl.Error.Code != "invalid_parameters" || !strings.Contains(rl.Error.Message, "between 1 and 200") {
			t.Fatalf("expected range invalid_parameters for workers_logs limit=%d, got %+v", badLimit, rl.Error)
		}
	}
	for _, badMax := range []int{0, -1, 32769} {
		rr := testAutomationCall(a, tok, "workers_result", "", workerResultInput{TurnID: "turn_missing", MaxBytes: automation.Int(badMax)})
		if rr.Error == nil || rr.Error.Code != "invalid_parameters" || !strings.Contains(rr.Error.Message, "between 1 and 32768") {
			t.Fatalf("expected range invalid_parameters for workers_result max_bytes=%d, got %+v", badMax, rr.Error)
		}
	}
	// tail + after_seq conflict and workers_wait_any empty turn_ids rejected cleanly
	if rTail := testAutomationCall(a, tok, "workers_logs", "", workerLogsInput{TurnID: "turn_missing", AfterSeq: 5, Tail: true}); rTail.Error == nil || rTail.Error.Code != "invalid_parameters" {
		t.Fatalf("expected invalid_parameters when tail=true and after_seq>0, got %+v", rTail.Error)
	}
	if rAny := testAutomationCall(a, tok, "workers_wait_any", "", workerWaitAnyInput{TurnIDs: nil}); rAny.Error == nil || rAny.Error.Code != "invalid_parameters" {
		t.Fatalf("expected invalid_parameters for empty workers_wait_any turn_ids, got %+v", rAny.Error)
	}
	opWC := testAutomationCall(a, tok, "operations_new_id", "", map[string]any{"operation": "workers_send"}).Data.(map[string]string)["operation_id"]
	if rBadWC := testAutomationCall(a, tok, "workers_send", opWC, workerSendInput{WorkerID: "wkr_x", Prompt: "hi", WallClockSeconds: automation.Int(15)}); rBadWC.Error == nil || rBadWC.Error.Code != "invalid_parameters" {
		t.Fatalf("expected invalid_parameters for wall_clock_seconds=15, got %+v", rBadWC.Error)
	}

	// 7. Structured next_step helper on waiting_approval instructs caller not to cancel.
	waitStep := workerWaitNextStep(workers.Turn{ID: "turn_wait_1", State: workers.TurnWaitingApproval}, false)
	if !strings.Contains(waitStep, "do not cancel") || !strings.Contains(waitStep, "turn_wait_1") {
		t.Fatalf("expected waiting_approval next_step to advise not cancelling and include turn_id, got %q", waitStep)
	}
}
