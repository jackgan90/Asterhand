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
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

	// 6. Crash window recovery: worker turn persisted with NativeID, public receipt still prepared -> operations_get repairs receipt!
	crashOpID := operations.NewID(time.Now())
	pCur, _, _ := a.Harnesses.AuthenticateCaller(tok)
	ident := operations.Identity{Actor: pCur.ID, Node: a.Config.Get().NodeID, ID: crashOpID, Name: "workers_send", Target: w.ID, Digest: "d"}
	crashTurn, err := a.Workers.Send(context.Background(), workers.SendInput{
		OperationID:  crashOpID,
		OperationKey: ident.NativeID(),
		ActorID:      pCur.ID,
		CallerID:     pCur.CallerID,
		WorkerID:     w.ID,
		Prompt:       "hold for stop test",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
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

	// 7. Structured next_step helper on waiting_approval instructs caller not to cancel,
	//    and waiting_input advertises defer_seconds + note escalation.
	waitStep := workerWaitNextStep(workers.Turn{ID: "turn_wait_1", State: workers.TurnWaitingApproval}, false, "", 0)
	if !strings.Contains(waitStep, "do not cancel") || !strings.Contains(waitStep, "turn_wait_1") {
		t.Fatalf("expected waiting_approval next_step to advise not cancelling and include turn_id, got %q", waitStep)
	}
	inStep := workerWaitNextStep(workers.Turn{ID: "turn_wait_2", State: workers.TurnWaitingInput}, false, "", 0)
	if !strings.Contains(inStep, "defer_seconds") || !strings.Contains(inStep, "note=") {
		t.Fatalf("expected waiting_input next_step to mention defer_seconds and note, got %q", inStep)
	}

	// 8. workers_reply defer_seconds range validation & workers_wait negative after_commentary_seq rejected.
	for _, badDef := range []int{10, 301} {
		opRep := testAutomationCall(a, tok, "operations_new_id", "", map[string]any{"operation": "workers_reply"}).Data.(map[string]string)["operation_id"]
		rr := testAutomationCall(a, tok, "workers_reply", opRep, workerReplyInput{TurnID: "turn_missing", DeferSeconds: automation.Int(badDef)})
		if rr.Error == nil || rr.Error.Code != "invalid_parameters" || !strings.Contains(rr.Error.Message, "between 30 and 300") {
			t.Fatalf("expected range invalid_parameters for defer_seconds=%d, got %+v", badDef, rr.Error)
		}
	}
	if rNeg := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: "turn_missing", AfterCommentarySeq: -1}); rNeg.Error == nil || rNeg.Error.Code != "invalid_parameters" {
		t.Fatalf("expected invalid_parameters for negative after_commentary_seq, got %+v", rNeg.Error)
	}
	opSteer := testAutomationCall(a, tok, "operations_new_id", "", map[string]any{"operation": "workers_steer"}).Data.(map[string]string)["operation_id"]
	if rSteer := testAutomationCall(a, tok, "workers_steer", opSteer, workerSteerInput{TurnID: "turn_missing", Text: ""}); rSteer.Error == nil || rSteer.Error.Code != "invalid_parameters" {
		t.Fatalf("expected invalid_parameters for empty workers_steer text, got %+v", rSteer.Error)
	}

	// 9. harnesses_list advertises supports_steer accurately (codex/claude=true, deepseek/antigravity=false).
	hl := testAutomationCall(a, tok, "harnesses_list", "", automation.Empty{})
	if hl.Error != nil {
		t.Fatalf("harnesses_list failed: %+v", hl.Error)
	}
	hlView := hl.Data.(harnessesListView)
	for _, hView := range hlView.Items {
		wantSteer := hView.ID == harnesses.HarnessCodex || hView.ID == harnesses.HarnessClaudeCode
		if hView.Capabilities.SupportsSteer != wantSteer {
			t.Fatalf("harness %s supports_steer=%v, want %v", hView.ID, hView.Capabilities.SupportsSteer, wantSteer)
		}
	}
}

func TestPhase1ACompactWaitPaginationAndCapabilitiesCLIPath(t *testing.T) {
	longText := strings.Repeat("审", 10000) // 30,000 UTF-8 bytes (> 24 KiB workers_wait preview limit)
	failNext := false
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			if onEvent != nil {
				onEvent(workers.AdapterEvent{Kind: "progress", Summary: "Inspecting parser"})
			}
			ch := make(chan workers.TurnOutcome, 1)
			if failNext {
				ch <- workers.TurnOutcome{
					State:        workers.TurnFailed,
					NativeTurnID: "nt_fail",
					ErrorCode:    "native_turn_error",
					ErrorMessage: "synthetic failure",
				}
				return "nt_fail", ch, nil
			}
			ch <- workers.TurnOutcome{
				State:        workers.TurnCompleted,
				NativeTurnID: "nt_long",
				Items:        []workers.NativeItem{{ItemID: "f1", Type: "agentMessage", Phase: "final_answer", Text: longText}},
			}
			return "nt_long", ch, nil
		},
	}
	n, ws, tok := setupM1WorkerNode(t, adapter)
	a := n.app

	// 1. capabilities advertises non-empty cli_path.
	capResp := testAutomationCall(a, tok, "capabilities", "", automation.Empty{})
	if capResp.Error != nil {
		t.Fatalf("capabilities failed: %+v", capResp.Error)
	}
	capMap := capResp.Data.(map[string]any)
	cliPath, _ := capMap["cli_path"].(string)
	if strings.TrimSpace(cliPath) == "" {
		t.Fatalf("expected non-empty cli_path in capabilities, got %+v", capMap)
	}

	// 2. Create worker and dispatch long UTF-8 output turn.
	cr := testAutomationCall(a, tok, "workers_create", operations.NewID(time.Now()), workerCreateInput{
		HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath, Alias: "compact-wait", AccessMode: harnesses.AccessReadOnly,
	})
	if cr.Error != nil {
		t.Fatalf("workers_create: %+v", cr.Error)
	}
	w := cr.Data.(workers.Worker)

	sr := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "long output",
	})
	if sr.Error != nil {
		t.Fatalf("workers_send: %+v", sr.Error)
	}
	turn := sr.Data.(workers.Turn)

	// 3. Default compact wait view omits nested Turn, provides aligned next_cursor, and exposes terminal after_vcs.
	wr := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})
	if wr.Error != nil {
		t.Fatalf("workers_wait compact: %+v", wr.Error)
	}
	wv := wr.Data.(workerWaitView)
	if wv.Turn != nil {
		t.Fatalf("expected compact workers_wait to omit nested turn, got %+v", wv.Turn)
	}
	if !wv.Truncated || wv.NextCursor == "" || !strings.Contains(wv.NextStep, wv.NextCursor) {
		t.Fatalf("expected truncated=true with next_cursor and cursor in next_step, got cursor=%q next_step=%q", wv.NextCursor, wv.NextStep)
	}
	if wv.AfterVCS == nil || wv.Progress == "" {
		t.Fatalf("expected terminal after_vcs and progress promoted to top level, got %+v", wv)
	}
	compactBytes, _ := json.Marshal(wv)

	// Continue pagination cleanly from wv.NextCursor without UTF-8 boundary errors.
	rr := testAutomationCall(a, tok, "workers_result", "", workerResultInput{TurnID: turn.ID, WorkspaceID: ws.ID, Cursor: wv.NextCursor})
	if rr.Error != nil {
		t.Fatalf("workers_result with next_cursor failed: %+v", rr.Error)
	}
	page := rr.Data.(workers.ResultPage)
	if !page.Complete || wv.Answer+page.Text != longText {
		t.Fatalf("reconstructed paginated text mismatch: complete=%v len=%d want=%d", page.Complete, len(wv.Answer+page.Text), len(longText))
	}

	// 4. verbose=true includes full Turn and is measurably larger than compact view.
	wrVerbose := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: turn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5), Verbose: true})
	if wrVerbose.Error != nil {
		t.Fatalf("workers_wait verbose: %+v", wrVerbose.Error)
	}
	wvVerbose := wrVerbose.Data.(workerWaitView)
	if wvVerbose.Turn == nil || wvVerbose.Turn.BaselineVCS == nil {
		t.Fatalf("expected verbose=true to include full Turn with BaselineVCS, got %+v", wvVerbose.Turn)
	}
	verboseBytes, _ := json.Marshal(wvVerbose)
	if len(compactBytes) >= len(verboseBytes) {
		t.Fatalf("expected compact wait payload (%d bytes) smaller than verbose (%d bytes)", len(compactBytes), len(verboseBytes))
	}

	// 5. Failed turn promotes error_code and error_message to top level in compact mode.
	failNext = true
	srFail := testAutomationCall(a, tok, "workers_send", operations.NewID(time.Now()), workerSendInput{
		WorkerID: w.ID, WorkspaceID: ws.ID, Prompt: "fail turn",
	})
	if srFail.Error != nil {
		t.Fatalf("workers_send fail: %+v", srFail.Error)
	}
	failTurn := srFail.Data.(workers.Turn)
	wrFail := testAutomationCall(a, tok, "workers_wait", "", workerWaitInput{TurnID: failTurn.ID, WorkspaceID: ws.ID, WaitSeconds: automation.Int(5)})
	if wrFail.Error != nil {
		t.Fatalf("workers_wait failed turn: %+v", wrFail.Error)
	}
	wvFail := wrFail.Data.(workerWaitView)
	if wvFail.Turn != nil || wvFail.ErrorCode != "native_turn_error" || wvFail.ErrorMessage != "synthetic failure" {
		t.Fatalf("expected top-level error fields without Turn, got %+v", wvFail)
	}
}

type realDefsRestartCaller struct {
	instanceID string
	defs       []automation.Definition
}

func (r *realDefsRestartCaller) Call(ctx context.Context, op, id string, args json.RawMessage) (automation.Response, error) {
	return automation.Response{
		Version:  automation.APIVersion,
		Instance: r.instanceID,
		Data: map[string]any{
			"instance_id":     r.instanceID,
			"policy_revision": "rev1",
			"roles":           []string{"read", "edit", "execute"},
			"toolsets":        map[string]bool{automation.ToolsetGeneral: true, automation.ToolsetCollab: true},
		},
	}, nil
}

func (r *realDefsRestartCaller) FetchDefinitions(ctx context.Context) (string, []automation.Definition, error) {
	return r.instanceID, r.defs, nil
}

func TestAllAutomationDefinitionsJSONRoundTripZeroSpuriousListChanged(t *testing.T) {
	liveDefs := AutomationDefinitions()
	raw, err := json.Marshal(liveDefs)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []automation.Definition
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	caller := &realDefsRestartCaller{instanceID: "inst_a", defs: decoded}
	pAll := automation.Principal{
		ID:       "harness:claude-code",
		CallerID: "claude-code",
		Roles:    []string{"read", "edit", "execute"},
		Toolsets: map[string]bool{automation.ToolsetGeneral: true, automation.ToolsetCollab: true},
	}
	host := automation.NewMCPHostWithCLIPath(caller, pAll, liveDefs, "asterhand-cli.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t1, t2 := mcp.NewInMemoryTransports()
	ss, err := host.Server.Connect(ctx, t1, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	changed := make(chan struct{}, 4)
	client := mcp.NewClient(&mcp.Implementation{Name: "full-defs-check", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})
	cs, err := client.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	caller.instanceID = "inst_b"
	host.SyncFromDaemon(ctx, "claude-code")
	select {
	case <-changed:
		t.Fatal("same-schema daemon restart with full AutomationDefinitions emitted spurious tools/list_changed")
	case <-time.After(60 * time.Millisecond):
	}
}

// Deferring a non-head waiting_input question through the public automation
// registry must persist both request_id and escalate_at in the durable receipt
// summary so same-operation-ID replays and operations_get recover the exact
// deadline.
func TestDurableReplyReceiptPreservesEscalateAt(t *testing.T) {
	bothPending := make(chan struct{})
	release := make(chan struct{})
	adapter := &stubWorkerAdapter{
		startFn: func(ctx context.Context, handle workers.ThreadHandle, spec workers.TurnStartSpec, onEvent func(workers.AdapterEvent)) (string, <-chan workers.TurnOutcome, error) {
			ch := make(chan workers.TurnOutcome, 1)
			go func() {
				var wg sync.WaitGroup
				wg.Add(2)
				go func() {
					defer wg.Done()
					_, _ = spec.OnInteraction(context.Background(), workers.InteractionRequest{
						Kind:      workers.InteractionInput,
						Summary:   "Question 1",
						Questions: []workers.InteractionQuestion{{ID: "q1", Question: "First?"}},
					})
				}()
				go func() {
					defer wg.Done()
					_, _ = spec.OnInteraction(context.Background(), workers.InteractionRequest{
						Kind:      workers.InteractionInput,
						Summary:   "Question 2",
						Questions: []workers.InteractionQuestion{{ID: "q2", Question: "Second?"}},
					})
				}()
				<-release
				ch <- workers.TurnOutcome{
					State:        workers.TurnCancelled,
					NativeTurnID: "nt_defer_rcpt",
				}
			}()
			close(bothPending)
			return "nt_defer_rcpt", ch, nil
		},
	}

	n, ws, tok := setupM1WorkerNode(t, adapter)
	defer close(release)
	a := n.app
	a.Workers.SetBudgets(workers.Budgets{
		InputCallerGrace:  10 * time.Second,
		InputClaimedGrace: 30 * time.Second,
	})

	w, err := a.Workers.Create(context.Background(), workers.CreateInput{
		OperationID: "op_defer_rcpt_w", HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws.RootPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := a.Workers.Send(context.Background(), workers.SendInput{
		OperationID: "op_defer_rcpt_s", WorkerID: w.ID, Prompt: "ask two questions",
	})
	if err != nil {
		t.Fatal(err)
	}
	<-bothPending

	var req2 string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		list := a.Workers.ListAllPendingInteractions()
		if len(list) == 2 {
			for _, it := range list {
				if it.Summary == "Question 2" {
					req2 = it.RequestID
				}
			}
			if req2 != "" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if req2 == "" {
		t.Fatal("expected two pending questions")
	}

	opID := operations.NewID(time.Now())
	replyInput := map[string]any{
		"turn_id":       turn.ID,
		"request_id":    req2,
		"workspace_id":  ws.ID,
		"defer_seconds": 180,
	}

	// 1. Direct execution through public automation registry.
	directResp := testAutomationCall(a, tok, "workers_reply", opID, replyInput)
	if directResp.Error != nil {
		t.Fatalf("direct workers_reply failed: %+v", directResp.Error)
	}
	directBytes, _ := json.Marshal(directResp.Data)
	var directData struct {
		RequestID  string     `json:"request_id"`
		Deferred   bool       `json:"deferred"`
		EscalateAt *time.Time `json:"escalate_at"`
	}
	if err := json.Unmarshal(directBytes, &directData); err != nil {
		t.Fatalf("unmarshal direct data: %v", err)
	}
	if directData.RequestID != req2 || !directData.Deferred || directData.EscalateAt == nil {
		t.Fatalf("direct response missing target request_id or escalate_at: %+v", directData)
	}

	assertReceipt := func(label string, rc *operations.Receipt) {
		t.Helper()
		if rc == nil || len(rc.Result) == 0 {
			t.Fatalf("%s: expected non-empty receipt result", label)
		}
		var summary struct {
			RequestID  string     `json:"request_id"`
			Deferred   bool       `json:"deferred"`
			EscalateAt *time.Time `json:"escalate_at"`
		}
		if err := json.Unmarshal(rc.Result, &summary); err != nil {
			t.Fatalf("%s: unmarshal receipt result: %v (%s)", label, err, string(rc.Result))
		}
		if summary.RequestID != req2 || !summary.Deferred || summary.EscalateAt == nil {
			t.Fatalf("%s: receipt missing request_id/escalate_at: %+v (raw=%s)", label, summary, string(rc.Result))
		}
		if !summary.EscalateAt.Equal(*directData.EscalateAt) {
			t.Fatalf("%s: escalate_at mismatch: got %v want %v", label, summary.EscalateAt, directData.EscalateAt)
		}
	}

	assertReceipt("direct_receipt", directResp.Receipt)

	// 2. Same operation_id replay.
	replayResp := testAutomationCall(a, tok, "workers_reply", opID, replyInput)
	if replayResp.Error != nil {
		t.Fatalf("replay workers_reply failed: %+v", replayResp.Error)
	}
	assertReceipt("replay_receipt", replayResp.Receipt)

	// 3. Durable recovery via operations_get.
	getResp := testAutomationCall(a, tok, "operations_get", "", struct {
		OperationID string `json:"operation_id"`
	}{OperationID: opID})
	if getResp.Error != nil {
		t.Fatalf("operations_get failed: %+v", getResp.Error)
	}
	getBytes, _ := json.Marshal(getResp.Data)
	var recovered operations.Receipt
	if err := json.Unmarshal(getBytes, &recovered); err != nil {
		t.Fatalf("unmarshal operations_get data: %v (%s)", err, string(getBytes))
	}
	assertReceipt("operations_get", &recovered)
}
