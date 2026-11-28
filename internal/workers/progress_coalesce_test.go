package workers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func setupDeepSeekTestService(t *testing.T, adapter Adapter, allowWrite bool) (*Service, harnesses.DelegatedWorkspace) {
	t.Helper()
	home := t.TempDir()
	wsDir := t.TempDir()
	hs, err := harnesses.Open(home, "")
	if err != nil {
		t.Fatal(err)
	}
	fakeExe := filepath.Join(home, "dsh.cmd")
	if err := os.WriteFile(fakeExe, []byte("@echo off\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tr := true
	in := harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessDeepSeek,
		CollabWorkerEnabled: &tr,
		ManualPath:          &fakeExe,
	}
	if allowWrite {
		in.AllowWorkspaceWrite = &tr
	}
	if _, err := hs.UpdatePolicy(in); err != nil {
		t.Fatal(err)
	}
	access := harnesses.AccessReadOnly
	if allowWrite {
		access = harnesses.AccessWorkspaceWrite
	}
	ws, _, err := hs.ResolveWorkspace(wsDir, access)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(home, hs, adapter)
	if err != nil {
		t.Fatal(err)
	}
	return svc, ws
}

func installTurnUpdateAuditTrigger(t *testing.T, st *Store) {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	_, err := st.db.Exec(`
		CREATE TABLE IF NOT EXISTS turn_update_audit (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			turn_id TEXT NOT NULL,
			state TEXT NOT NULL,
			progress TEXT NOT NULL
		);
		CREATE TRIGGER IF NOT EXISTS trg_turn_update_audit
		AFTER UPDATE ON turns
		BEGIN
			INSERT INTO turn_update_audit(turn_id, state, progress)
			VALUES (NEW.turn_id, NEW.state, COALESCE(json_extract(CAST(NEW.body AS TEXT), '$.progress'), ''));
		END;
	`)
	if err != nil {
		t.Fatalf("install turn audit trigger: %v", err)
	}
}

func readTurnUpdateAudit(t *testing.T, st *Store, turnID string) []struct{ state, progress string } {
	t.Helper()
	st.mu.Lock()
	defer st.mu.Unlock()
	rows, err := st.db.Query(`SELECT state, progress FROM turn_update_audit WHERE turn_id = ? ORDER BY id ASC`, turnID)
	if err != nil {
		t.Fatalf("query turn audit: %v", err)
	}
	defer rows.Close()
	var out []struct{ state, progress string }
	for rows.Next() {
		var s, p string
		if err := rows.Scan(&s, &p); err != nil {
			t.Fatalf("scan turn audit: %v", err)
		}
		out = append(out, struct{ state, progress string }{state: s, progress: p})
	}
	return out
}

func writeDeepSeekThoughtBurst(p *pipeFakeProc, prefix string, count int) int64 {
	var total int64
	for i := 0; i < count; i++ {
		params := map[string]any{
			"update": map[string]any{
				"sessionUpdate": "agent_thought_chunk",
				"content":       map[string]any{"type": "text", "text": fmt.Sprintf("%s #%03d", prefix, i)},
			},
		}
		rawParams, _ := json.Marshal(params)
		total += int64(len(rawParams))
		writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": params})
	}
	return total
}

func newThoughtBurstDeepSeekAdapter(chunkCount int, chunksFlushed chan struct{}, releaseFinal <-chan struct{}, expectedBytes *int64, mu *sync.Mutex) *DeepSeekAdapter {
	return &DeepSeekAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				for sc.Scan() {
					var msg struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg.Method {
					case "initialize":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
					case "session/new":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"sessionId": "dsh-thought-coalesce"}})
					case "session/prompt":
						bytesSent := writeDeepSeekThoughtBurst(p, "distinct reasoning fragment", chunkCount)
						mu.Lock()
						*expectedBytes += bytesSent
						mu.Unlock()
						close(chunksFlushed)
						<-releaseFinal
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "DONE"}}},
						})
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"stopReason": "end_turn"}})
					case "session/close":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
					}
				}
			}()
			return p, nil
		},
	}
}

func runThoughtBurstScenario(t *testing.T, chunkCount int) (thinkingWrites int, liveEvents, liveBytes, minExpectedBytes int64) {
	t.Helper()
	chunksFlushed := make(chan struct{})
	releaseFinal := make(chan struct{})
	var mu sync.Mutex
	var expectedBytes int64

	adapter := newThoughtBurstDeepSeekAdapter(chunkCount, chunksFlushed, releaseFinal, &expectedBytes, &mu)
	svc, ws := setupDeepSeekTestService(t, adapter, false)
	defer func() { _ = svc.Close(context.Background()) }()
	installTurnUpdateAuditTrigger(t, svc.store)

	w, err := svc.Create(context.Background(), CreateInput{
		HarnessID: harnesses.HarnessDeepSeek, WorkspaceRoot: ws.RootPath, Alias: fmt.Sprintf("dsh-think-%d", chunkCount), AccessMode: harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{
		OperationID: fmt.Sprintf("op_dsh_think_%d", chunkCount), WorkerID: w.ID, Prompt: "deep analysis",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-chunksFlushed:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for thought chunks to be written")
	}

	deadline := time.Now().Add(5 * time.Second)
	var live Turn
	for time.Now().Before(deadline) {
		live, err = svc.GetTurn(turn.ID, ws.ID)
		if err == nil && live.StreamEvents >= int64(chunkCount) && live.Progress == "Thinking..." {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if live.StreamEvents < int64(chunkCount) || live.Progress != "Thinking..." {
		close(releaseFinal)
		t.Fatalf("expected live stream_events>=%d and progress=Thinking..., got events=%d bytes=%d progress=%q", chunkCount, live.StreamEvents, live.StreamBytes, live.Progress)
	}
	svc.mu.Lock()
	var lastEv time.Time
	if rt := svc.activeTurns[turn.ID]; rt != nil {
		lastEv = rt.lastEventAt
	}
	svc.mu.Unlock()
	if lastEv.IsZero() || time.Since(lastEv) > 3*time.Second {
		close(releaseFinal)
		t.Fatalf("expected fresh in-memory lastEventAt after %d chunks, got %v", chunkCount, lastEv)
	}

	mu.Lock()
	minExpectedBytes = expectedBytes
	mu.Unlock()
	liveEvents, liveBytes = live.StreamEvents, live.StreamBytes

	close(releaseFinal)
	done, ans, _, err := svc.Wait(context.Background(), turn.ID, ws.ID, 5*time.Second)
	if err != nil || done.State != TurnCompleted || ans != "DONE" {
		t.Fatalf("unexpected completion: state=%s ans=%q err=%v", done.State, ans, err)
	}
	for _, rec := range readTurnUpdateAudit(t, svc.store, turn.ID) {
		if rec.progress == "Thinking..." {
			thinkingWrites++
		}
	}
	return thinkingWrites, liveEvents, liveBytes, minExpectedBytes
}

func TestDeepSeekThoughtChunksCoalesceDBWritesAndPreserveTelemetry(t *testing.T) {
	writes1, events1, bytes1, expBytes1 := runThoughtBurstScenario(t, 1)
	writes128, events128, bytes128, expBytes128 := runThoughtBurstScenario(t, 128)
	t.Logf("1 chunk: thinking_db_writes=%d stream_events=%d stream_bytes=%d; 128 chunks: thinking_db_writes=%d stream_events=%d stream_bytes=%d",
		writes1, events1, bytes1, writes128, events128, bytes128)

	if events1 < 1 || bytes1 < expBytes1 {
		t.Fatalf("1-chunk telemetry undercounted: events=%d bytes=%d want>=%d", events1, bytes1, expBytes1)
	}
	if events128 < 128 || bytes128 < expBytes128 {
		t.Fatalf("128-chunk telemetry undercounted: events=%d bytes=%d want>=%d", events128, bytes128, expBytes128)
	}
	if writes1 == 0 || writes1 > 3 {
		t.Fatalf("unexpected 1-chunk Thinking... DB writes: %d", writes1)
	}
	if writes128 > 3 || writes128-writes1 > 1 {
		t.Fatalf("expected coalesced Thinking... DB writes independent of chunk count (1 chunk=%d, 128 chunks=%d)", writes1, writes128)
	}
}

func newTransitionDeepSeekAdapter(phase3Started chan struct{}, releaseFinish <-chan struct{}) *DeepSeekAdapter {
	return &DeepSeekAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				var promptID json.RawMessage
				for sc.Scan() {
					var msg struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
						Result map[string]any  `json:"result"`
					}
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg.Method {
					case "initialize":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
					case "session/new":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"sessionId": "dsh-trans"}})
					case "session/prompt":
						promptID = msg.ID
						writeDeepSeekThoughtBurst(p, "p1", 32)
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Checking alpha.go now."}}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "tc1", "title": "read", "status": "in_progress", "rawInput": map[string]any{"file_path": "alpha.go"}}},
						})
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tc1", "status": "completed"}},
						})
						writeDeepSeekThoughtBurst(p, "p2", 32)
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "id": 77, "method": "session/request_permission",
							"params": map[string]any{
								"sessionId": "dsh-trans",
								"toolCall":  map[string]any{"toolCallId": "tc_edit", "title": "write", "kind": "edit", "rawInput": map[string]any{"file_path": "alpha.go"}},
								"options":   []map[string]any{{"optionId": "allow", "kind": "allow_once", "name": "Allow once"}},
							},
						})
						writeDeepSeekThoughtBurst(p, "during-approval", 1)
					case "session/close":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
					default:
						if msg.Method == "" && msg.Result != nil && len(promptID) > 0 {
							writeDeepSeekThoughtBurst(p, "p3", 32)
							close(phase3Started)
							<-releaseFinish
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "method": "session/update",
								"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "ALL_GOOD"}}},
							})
							writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]any{"stopReason": "end_turn"}})
						}
					}
				}
			}()
			return p, nil
		},
	}
}

func TestDeepSeekThoughtCoalescingPublishesToolCommentaryApprovalAndResumedThinking(t *testing.T) {
	phase3Started := make(chan struct{})
	releaseFinish := make(chan struct{})
	svc, ws := setupDeepSeekTestService(t, newTransitionDeepSeekAdapter(phase3Started, releaseFinish), true)
	defer func() { _ = svc.Close(context.Background()) }()
	installTurnUpdateAuditTrigger(t, svc.store)

	w, err := svc.Create(context.Background(), CreateInput{
		HarnessID: harnesses.HarnessDeepSeek, WorkspaceRoot: ws.RootPath, Alias: "dsh-trans",
		AccessMode: harnesses.AccessWorkspaceWrite, ApprovalMode: ApprovalModeAskHuman,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{OperationID: "op_dsh_trans", WorkerID: w.ID, Prompt: "inspect and edit"})
	if err != nil {
		t.Fatal(err)
	}
	if wt, _, _, comms, err := svc.WaitWithOptions(context.Background(), turn.ID, ws.ID, WaitOptions{WaitDuration: 5 * time.Second, WakeOnCommentary: true}); err != nil || len(comms) == 0 || !strings.Contains(comms[0].Text, "Checking alpha.go now.") {
		t.Fatalf("expected immediate commentary wake, got state=%s comms=%+v err=%v", wt.State, comms, err)
	}

	var reqID string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if items := svc.ListHumanInteractions(); len(items) > 0 && items[0].TurnID == turn.ID {
			reqID = items[0].RequestID
			break
		}
	}
	if reqID == "" {
		t.Fatal("timed out waiting for human approval interaction")
	}
	svc.mu.Lock()
	activeRT := svc.activeTurns[turn.ID]
	svc.mu.Unlock()
	beforeWaitAudit := len(readTurnUpdateAudit(t, svc.store, turn.ID))
	svc.handleTurnAdapterEvent(activeRT, &w, &turn, AdapterEvent{Kind: "progress", Summary: "Thinking...", Bytes: 42})
	if afterWaitAudit := len(readTurnUpdateAudit(t, svc.store, turn.ID)); afterWaitAudit != beforeWaitAudit {
		t.Fatalf("expected 0 DB writes for progress event during waiting_approval, got %d -> %d", beforeWaitAudit, afterWaitAudit)
	}
	if waitSnap, err := svc.GetTurn(turn.ID, ws.ID); err != nil || waitSnap.State != TurnWaitingApproval || !strings.HasPrefix(waitSnap.Progress, "Waiting for human approval:") {
		t.Fatalf("expected approval progress preserved during waiting_approval, got state=%s progress=%q err=%v", waitSnap.State, waitSnap.Progress, err)
	}
	if _, err := svc.DecideInteractionHuman(reqID, InteractionDecision{Decision: DecisionAllowOnce}); err != nil {
		t.Fatalf("DecideInteractionHuman: %v", err)
	}

	select {
	case <-phase3Started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for phase 3 thinking after approval")
	}
	var live Turn
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if live, err = svc.GetTurn(turn.ID, ws.ID); err == nil && live.Progress == "Thinking..." && live.StreamEvents >= 96 {
			break
		}
	}
	if live.Progress != "Thinking..." || live.StreamEvents < 96 {
		close(releaseFinish)
		t.Fatalf("expected resumed Thinking... and >=96 stream events, got progress=%q events=%d", live.Progress, live.StreamEvents)
	}
	close(releaseFinish)
	if done, ans, _, err := svc.Wait(context.Background(), turn.ID, ws.ID, 5*time.Second); err != nil || done.State != TurnCompleted || ans != "ALL_GOOD" {
		t.Fatalf("unexpected final turn: state=%s ans=%q err=%v", done.State, ans, err)
	}

	audit := readTurnUpdateAudit(t, svc.store, turn.ID)
	thinkingWrites, sawTool, sawApproval, sawResumed := 0, false, false, false
	for _, rec := range audit {
		if rec.progress == "Thinking..." {
			thinkingWrites++
		}
		if strings.Contains(rec.progress, "read alpha.go") {
			sawTool = true
		}
		if rec.state == TurnWaitingApproval {
			sawApproval = true
		}
		if strings.Contains(rec.progress, "Approval resolved") {
			sawResumed = true
		}
	}
	if !sawTool || !sawApproval || !sawResumed {
		t.Fatalf("missing expected visible transitions in audit: tool=%v approval=%v resumed=%v audit=%+v", sawTool, sawApproval, sawResumed, audit)
	}
	if thinkingWrites < 3 || thinkingWrites > 5 {
		t.Fatalf("expected 3..5 Thinking... writes across 3 phases (96 chunks), got %d (audit=%+v)", thinkingWrites, audit)
	}
}

func TestDeepSeekNonCompleteStopReasonsPreservePartialOutput(t *testing.T) {
	const partialText = "Review stopped after item 1."
	cases := []struct {
		reason      string
		wantState   string
		wantCode    string
		wantAvail   bool
		wantAnswer  string
		wantPartial string
	}{
		{reason: "end_turn", wantState: TurnCompleted, wantCode: "", wantAvail: true, wantAnswer: partialText, wantPartial: ""},
		{reason: "cancelled", wantState: TurnInterrupted, wantCode: "cancelled", wantAvail: false, wantAnswer: "", wantPartial: partialText},
		{reason: "max_tokens", wantState: TurnInterrupted, wantCode: "max_tokens", wantAvail: false, wantAnswer: "", wantPartial: partialText},
		{reason: "max_turn_requests", wantState: TurnInterrupted, wantCode: "max_turn_requests", wantAvail: false, wantAnswer: "", wantPartial: partialText},
		{reason: "refusal", wantState: TurnFailed, wantCode: "refusal", wantAvail: false, wantAnswer: "", wantPartial: partialText},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			adapter := &DeepSeekAdapter{
				spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
					p := newPipeFakeProc()
					go func() {
						defer p.finish()
						sc := bufio.NewScanner(p.inR)
						for sc.Scan() {
							var msg struct {
								ID     json.RawMessage `json:"id"`
								Method string          `json:"method"`
							}
							if json.Unmarshal(sc.Bytes(), &msg) != nil {
								continue
							}
							switch msg.Method {
							case "initialize":
								writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
							case "session/new":
								writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"sessionId": "sess_stop"}})
							case "session/prompt":
								writeJSONLine(p.outW, map[string]any{
									"jsonrpc": "2.0", "method": "session/update",
									"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": partialText}}},
								})
								writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"stopReason": tc.reason}})
							case "session/close":
								writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
							}
						}
					}()
					return p, nil
				},
			}
			svc, ws := setupDeepSeekTestService(t, adapter, false)
			defer func() { _ = svc.Close(context.Background()) }()

			w, err := svc.Create(context.Background(), CreateInput{
				HarnessID: harnesses.HarnessDeepSeek, WorkspaceRoot: ws.RootPath, Alias: "dsh-stop-" + tc.reason, AccessMode: harnesses.AccessReadOnly,
			})
			if err != nil {
				t.Fatal(err)
			}
			turn, err := svc.Send(context.Background(), SendInput{
				OperationID: "op_dsh_stop_" + tc.reason, WorkerID: w.ID, ExpectedWorkspaceID: ws.ID, Prompt: "review",
			})
			if err != nil {
				t.Fatal(err)
			}
			done, preview, _, err := svc.Wait(context.Background(), turn.ID, ws.ID, 5*time.Second)
			if err != nil {
				t.Fatalf("Wait failed: %v", err)
			}
			if done.State != tc.wantState || done.ErrorCode != tc.wantCode || done.AnswerAvailable != tc.wantAvail || preview != tc.wantAnswer || done.PartialOutput != tc.wantPartial {
				t.Fatalf("stopReason=%s got state=%s code=%q avail=%v preview=%q partial=%q; want state=%s code=%q avail=%v preview=%q partial=%q",
					tc.reason, done.State, done.ErrorCode, done.AnswerAvailable, preview, done.PartialOutput,
					tc.wantState, tc.wantCode, tc.wantAvail, tc.wantAnswer, tc.wantPartial)
			}
		})
	}
}

func TestDeepSeekMaxTokensLongPartialOutputPaginatesFullTail(t *testing.T) {
	const tailMarker = "TAIL_VERDICT_LINE: preserve full partial output past 4 KiB."
	const chunkText = "审查分段说明A1234567890\n"
	batch := strings.Repeat(chunkText, 20)
	fullPartial := strings.Repeat(batch, 20) + tailMarker
	adapter := &DeepSeekAdapter{
		spawn: func(exe string, args []string, workdir string, env []string) (workerProcess, error) {
			p := newPipeFakeProc()
			go func() {
				defer p.finish()
				sc := bufio.NewScanner(p.inR)
				for sc.Scan() {
					var msg struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(sc.Bytes(), &msg) != nil {
						continue
					}
					switch msg.Method {
					case "initialize":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"protocolVersion": 1}})
					case "session/new":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"sessionId": "sess_long_partial"}})
					case "session/prompt":
						for i := 0; i < 20; i++ {
							writeJSONLine(p.outW, map[string]any{
								"jsonrpc": "2.0", "method": "session/update",
								"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": batch}}},
							})
						}
						writeJSONLine(p.outW, map[string]any{
							"jsonrpc": "2.0", "method": "session/update",
							"params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": tailMarker}}},
						})
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{"stopReason": "max_tokens"}})
					case "session/close":
						writeJSONLine(p.outW, map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": map[string]any{}})
					}
				}
			}()
			return p, nil
		},
	}
	svc, ws := setupDeepSeekTestService(t, adapter, false)
	defer func() { _ = svc.Close(context.Background()) }()
	w, err := svc.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessDeepSeek, WorkspaceRoot: ws.RootPath, Alias: "dsh-long-partial", AccessMode: harnesses.AccessReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := svc.Send(context.Background(), SendInput{OperationID: "op_dsh_long_partial", WorkerID: w.ID, ExpectedWorkspaceID: ws.ID, Prompt: "long review"})
	if err != nil {
		t.Fatal(err)
	}
	done, preview, _, err := svc.Wait(context.Background(), turn.ID, ws.ID, 5*time.Second)
	if err != nil || done.State != TurnInterrupted || done.ErrorCode != "max_tokens" || done.AnswerAvailable || preview != "" || done.ResultBytes != len(fullPartial) {
		t.Fatalf("unexpected interrupted turn: state=%s code=%q avail=%v preview=%q result_bytes=%d want=%d err=%v",
			done.State, done.ErrorCode, done.AnswerAvailable, preview, done.ResultBytes, len(fullPartial), err)
	}
	if len(done.PartialOutput) > 4099 || strings.Contains(done.PartialOutput, tailMarker) {
		t.Fatalf("expected 4 KiB preview without tail, got len=%d", len(done.PartialOutput))
	}

	var rebuilt strings.Builder
	offset, pages := 0, 0
	for {
		page, err := svc.ReadResult(turn.ID, ws.ID, offset, 4096)
		if err != nil || !page.Partial || page.AnswerAvailable || page.AnswerSource != AnswerSourceNone || page.Expired || page.TotalBytes != len(fullPartial) {
			t.Fatalf("unexpected partial page at offset=%d: %+v err=%v", offset, page, err)
		}
		rebuilt.WriteString(page.Text)
		pages++
		if page.Complete {
			break
		}
		next, err := strconv.Atoi(page.NextCursor)
		if err != nil || next <= offset {
			t.Fatalf("invalid next_cursor %q at offset=%d", page.NextCursor, offset)
		}
		offset = next
	}
	if pages < 3 || rebuilt.String() != fullPartial {
		t.Fatalf("rebuilt partial mismatch: pages=%d got=%d bytes want=%d bytes", pages, rebuilt.Len(), len(fullPartial))
	}
	svc.PruneRetention(time.Now().Add(DetailRetention + 24*time.Hour))
	expiredPage, err := svc.ReadResult(turn.ID, ws.ID, 0, 4096)
	if err != nil || !expiredPage.Expired || !expiredPage.Partial || expiredPage.AnswerAvailable || !expiredPage.Complete || expiredPage.PartialOutput == "" {
		t.Fatalf("expected expired partial page retaining 4 KiB summary, got %+v err=%v", expiredPage, err)
	}
	expiredWait, err := svc.WaitSnapshot(context.Background(), turn.ID, ws.ID, WaitOptions{WaitDuration: time.Second})
	if err != nil || !expiredWait.Expired || expiredWait.Turn.AnswerAvailable || expiredWait.Turn.PartialOutput == "" {
		t.Fatalf("expected WaitSnapshot to mark expired partial output while keeping summary: %+v err=%v", expiredWait, err)
	}
	expiredItems, _, err := svc.WaitAny(context.Background(), []string{turn.ID}, ws.ID, time.Second)
	if err != nil || len(expiredItems) != 1 || !expiredItems[0].Expired || expiredItems[0].AnswerAvailable || expiredItems[0].ResultBytes != len(fullPartial) {
		t.Fatalf("expected WaitAny to mark expired partial output: %+v err=%v", expiredItems, err)
	}
}
