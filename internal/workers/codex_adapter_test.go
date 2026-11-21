package workers

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

func TestCodexSessionServerRequestRejection(t *testing.T) {
	runCase := func(t *testing.T, method, wantState string, wantDecisionCancel bool) {
		t.Helper()
		inR, inW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer inR.Close()
		defer inW.Close()

		sess := &codexSession{
			proc:     &managedProc{stdin: inW},
			threadID: "thr_test",
			pending:  map[int64]chan rpcReply{},
			active: &codexActiveTurn{
				turnID: "nt_1",
				deltas: map[string]*strings.Builder{},
				done:   make(chan TurnOutcome, 1),
			},
		}

		// Simulate server -> client approval or user-input request.
		reqBytes, _ := json.Marshal(map[string]any{
			"id":     10,
			"method": method,
			"params": map[string]any{"turnId": "nt_1"},
		})
		sess.handleMessage(reqBytes)

		// Read the immediate response written to stdin.
		scanner := bufio.NewScanner(inR)
		if !scanner.Scan() {
			t.Fatalf("expected immediate reply for %s", method)
		}
		var reply map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil {
			t.Fatalf("invalid json reply: %v (%s)", err, scanner.Text())
		}
		if reply["id"] != float64(10) {
			t.Fatalf("expected reply id=10, got %+v", reply)
		}
		if wantDecisionCancel {
			resMap, ok := reply["result"].(map[string]any)
			if !ok || resMap["decision"] != "cancel" {
				t.Fatalf("expected result.decision=cancel, got %+v", reply)
			}
		} else {
			errMap, ok := reply["error"].(map[string]any)
			if !ok || errMap["code"] != float64(-32001) {
				t.Fatalf("expected json-rpc error -32001, got %+v", reply)
			}
		}

		doneCh := sess.active.done
		// Simulate turn/completed arriving afterwards: outcome must stay blocked_* with no final answer!
		doneNotif, _ := json.Marshal(map[string]any{
			"method": "turn/completed",
			"params": map[string]any{
				"turn": map[string]any{"id": "nt_1", "status": "interrupted"},
			},
		})
		sess.handleMessage(doneNotif)

		select {
		case out := <-doneCh:
			if out.State != wantState || out.ErrorCode != wantState {
				t.Fatalf("expected state=%s, got %+v", wantState, out)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for turn outcome")
		}
	}

	t.Run("commandExecution_approval", func(t *testing.T) {
		runCase(t, "item/commandExecution/requestApproval", TurnBlockedApproval, true)
	})
	t.Run("tool_requestUserInput", func(t *testing.T) {
		runCase(t, "item/tool/requestUserInput", TurnBlockedUserInput, false)
	})
}

func TestWindow1PreStartAndWindow3RunningRevocation(t *testing.T) {
	// Window 1: revoke workspace while OpenThread is in progress -> StartTurn MUST NEVER be called.
	inOpen := make(chan struct{})
	releaseOpen := make(chan struct{})
	var startCalled int
	var mu sync.Mutex
	fa1 := &fakeAdapter{
		openFn: func(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error) {
			close(inOpen)
			<-releaseOpen
			return ThreadHandle{Key: spec.WorkerID + ":thr1", NativeSessionID: "thr1"}, nil
		},
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			mu.Lock()
			startCalled++
			mu.Unlock()
			return "never", make(chan TurnOutcome, 1), nil
		},
	}
	svc1, hs1, ws1 := setupTestService(t, fa1)
	w1, err := svc1.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws1.RootPath, Alias: "win1"})
	if err != nil {
		t.Fatal(err)
	}
	t1, err := svc1.Send(context.Background(), SendInput{OperationID: "op_w1", WorkerID: w1.ID, Prompt: "check"})
	if err != nil {
		t.Fatal(err)
	}
	<-inOpen
	fl1 := false
	if _, err := hs1.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessCodex,
		CollabWorkerEnabled: &fl1,
	}); err != nil {
		t.Fatal(err)
	}
	close(releaseOpen)
	r1, _, _, err := svc1.Wait(context.Background(), t1.ID, ws1.ID, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	sc := startCalled
	mu.Unlock()
	if sc != 0 || r1.State != TurnCancelledRevoked || r1.AnswerAvailable {
		t.Fatalf("Window 1 expected 0 StartTurn calls and cancelled_authorization_revoked, got calls=%d turn=%+v", sc, r1)
	}

	// Window 3: running turn revoked via disabling worker harness -> InterruptTurn invoked!
	running := make(chan struct{})
	interrupted := make(chan struct{})
	fa3 := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			close(running)
			ch := make(chan TurnOutcome, 1)
			go func() {
				<-interrupted
				ch <- TurnOutcome{State: TurnInterrupted, NativeTurnID: "nt_w3"}
			}()
			return "nt_w3", ch, nil
		},
		interruptFn: func(ctx context.Context, handle ThreadHandle, nativeTurnID string) error {
			select {
			case <-interrupted:
			default:
				close(interrupted)
			}
			return nil
		},
	}
	svc3, hs3, ws3 := setupTestService(t, fa3)
	w3, err := svc3.Create(context.Background(), CreateInput{HarnessID: harnesses.HarnessCodex, WorkspaceRoot: ws3.RootPath, Alias: "win3"})
	if err != nil {
		t.Fatal(err)
	}
	t3, err := svc3.Send(context.Background(), SendInput{OperationID: "op_w3", WorkerID: w3.ID, Prompt: "long run"})
	if err != nil {
		t.Fatal(err)
	}
	<-running
	fl := false
	if _, err := hs3.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessCodex,
		CollabWorkerEnabled: &fl,
	}); err != nil {
		t.Fatal(err)
	}
	r3, _, _, err := svc3.Wait(context.Background(), t3.ID, ws3.ID, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if r3.State != TurnCancelledRevoked || r3.AnswerAvailable || len(fa3.interruptedIDs) == 0 {
		t.Fatalf("Window 3 expected InterruptTurn and cancelled_authorization_revoked, got interrupted=%v turn=%+v", fa3.interruptedIDs, r3)
	}
}

func TestModelAndReasoningEffortDefaultsAndPerTurnOverride(t *testing.T) {
	var seen []TurnStartSpec
	var mu sync.Mutex
	fa := &fakeAdapter{
		startFn: func(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error) {
			mu.Lock()
			seen = append(seen, spec)
			mu.Unlock()
			ch := make(chan TurnOutcome, 1)
			ch <- TurnOutcome{
				State:        TurnCompleted,
				NativeTurnID: "nt_eff",
				Items:        []NativeItem{{ItemID: "a", Type: "agentMessage", Phase: "final_answer", Text: "ok"}},
			}
			return "nt_eff", ch, nil
		},
	}
	svc, _, ws := setupTestService(t, fa)

	if _, err := svc.Create(context.Background(), CreateInput{
		HarnessID:       harnesses.HarnessCodex,
		WorkspaceRoot:   ws.RootPath,
		Alias:           "bad-effort",
		ReasoningEffort: "high;rm -rf",
	}); err == nil {
		t.Fatal("expected invalid reasoning_effort rejected")
	}

	w, err := svc.Create(context.Background(), CreateInput{
		HarnessID:       harnesses.HarnessCodex,
		WorkspaceRoot:   ws.RootPath,
		Alias:           "eff-reviewer",
		Model:           "gpt-5.4",
		ReasoningEffort: "HIGH",
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.Model != "gpt-5.4" || w.ReasoningEffort != "high" {
		t.Fatalf("unexpected worker model/effort: %+v", w)
	}

	// Turn 1 inherits worker defaults.
	t1, err := svc.Send(context.Background(), SendInput{OperationID: "op_e1", WorkerID: w.ID, Prompt: "p1"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = svc.Wait(context.Background(), t1.ID, ws.ID, 5*time.Second)

	// Turn 2 overrides model + reasoning_effort for this turn.
	t2, err := svc.Send(context.Background(), SendInput{
		OperationID:     "op_e2",
		WorkerID:        w.ID,
		Prompt:          "p2",
		Model:           "gpt-5.3-codex",
		ReasoningEffort: "xhigh",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = svc.Wait(context.Background(), t2.ID, ws.ID, 5*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("expected 2 StartTurn calls, got %d", len(seen))
	}
	if seen[0].Model != "gpt-5.4" || seen[0].ReasoningEffort != "high" {
		t.Fatalf("turn 1 did not inherit defaults: %+v", seen[0])
	}
	if seen[1].Model != "gpt-5.3-codex" || seen[1].ReasoningEffort != "xhigh" {
		t.Fatalf("turn 2 did not apply overrides: %+v", seen[1])
	}
}
