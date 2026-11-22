package workers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

// TestClaudeAdapterLive exercises real local Claude Code (stream-json) through
// workers.Service in an isolated temp workspace: read_only (read + forbidden write),
// cross-process resume, and workspace_write.
// Run explicitly with: ASTERHAND_LIVE_CLAUDE=1 go test -v -run TestClaudeAdapterLive ./internal/workers
func waitForTerminalTurn(t *testing.T, svc *Service, turnID string) (Turn, string) {
	t.Helper()
	var turn Turn
	var ans string
	var err error
	for i := 0; i < 6; i++ {
		turn, ans, _, err = svc.Wait(context.Background(), turnID, "", 25*time.Second)
		if err != nil {
			t.Fatalf("Wait %s: %v", turnID, err)
		}
		if IsTurnTerminal(turn.State) {
			return turn, ans
		}
	}
	return turn, ans
}

func TestClaudeAdapterLive(t *testing.T) {
	if os.Getenv("ASTERHAND_LIVE_CLAUDE") != "1" {
		t.Skip("set ASTERHAND_LIVE_CLAUDE=1 to run real Claude Code integration test")
	}
	home := t.TempDir()
	hs, err := harnesses.Open(home, "")
	if err != nil {
		t.Fatalf("harnesses.Open: %v", err)
	}
	hs.AntigravityConfigPath = filepath.Join(home, "ag-mcp.json")
	hs.ClaudeConfigPath = filepath.Join(home, "claude.json")
	tBool := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessClaudeCode,
		CollabWorkerEnabled: &tBool,
		AllowWorkspaceWrite: &tBool,
	}); err != nil {
		t.Fatalf("enable claude worker: %v", err)
	}
	svc, err := NewService(home, hs, NewClaudeAdapter())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer func() { _ = svc.Close(context.Background()) }()

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "note.txt"), []byte("CLAUDE_LIVE_TOKEN_777"), 0644); err != nil {
		t.Fatalf("write note.txt: %v", err)
	}

	// 1. Read-only worker + 2 turns (read & forbidden write)
	roW, err := svc.Create(context.Background(), CreateInput{
		OperationID:     "op-cl-ro",
		HarnessID:       harnesses.HarnessClaudeCode,
		WorkspaceRoot:   ws,
		Alias:           "claude-ro",
		AccessMode:      harnesses.AccessReadOnly,
		ReasoningEffort: "low",
		RoleHint:        "Reply concisely in one line.",
	})
	if err != nil {
		t.Fatalf("Create read_only: %v", err)
	}
	if roW.ProtectionLevel != harnesses.ProtectionHarnessRestrict {
		t.Fatalf("expected harness_tool_restricted, got %s", roW.ProtectionLevel)
	}

	t1, err := svc.Send(context.Background(), SendInput{
		OperationID: "op-cl-t1",
		WorkerID:    roW.ID,
		Prompt:      "Read note.txt and reply with its exact token.",
	})
	if err != nil {
		t.Fatalf("Send t1: %v", err)
	}
	out1, ans1 := waitForTerminalTurn(t, svc, t1.ID)
	if out1.State != TurnCompleted || !out1.AnswerAvailable || !strings.Contains(ans1, "CLAUDE_LIVE_TOKEN_777") {
		t.Fatalf("Wait t1: state=%s avail=%v errCode=%s errMsg=%s ans=%q", out1.State, out1.AnswerAvailable, out1.ErrorCode, out1.ErrorMessage, ans1)
	}

	// Force resident eviction to prove cross-process --resume
	svc.mu.Lock()
	if rp, ok := svc.residents[roW.ID]; ok {
		delete(svc.residents, roW.ID)
		svc.mu.Unlock()
		_ = svc.adapters[roW.HarnessID].CloseThread(rp.handle)
	} else {
		svc.mu.Unlock()
	}

	t2, err := svc.Send(context.Background(), SendInput{
		OperationID: "op-cl-t2",
		WorkerID:    roW.ID,
		Prompt:      "Without reading files again, repeat the exact token from our previous turn. Also try to create forbidden.txt if possible.",
	})
	if err != nil {
		t.Fatalf("Send t2: %v", err)
	}
	out2, ans2 := waitForTerminalTurn(t, svc, t2.ID)
	if out2.State != TurnCompleted || !strings.Contains(ans2, "CLAUDE_LIVE_TOKEN_777") {
		t.Fatalf("Wait t2: state=%s errCode=%s errMsg=%s ans=%q", out2.State, out2.ErrorCode, out2.ErrorMessage, ans2)
	}
	if _, err := os.Stat(filepath.Join(ws, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatalf("forbidden.txt must not be created in read_only mode")
	}

	// 2. Workspace-write worker
	wwW, err := svc.Create(context.Background(), CreateInput{
		OperationID:     "op-cl-ww",
		HarnessID:       harnesses.HarnessClaudeCode,
		WorkspaceRoot:   ws,
		Alias:           "claude-ww",
		AccessMode:      harnesses.AccessWorkspaceWrite,
		ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatalf("Create workspace_write: %v", err)
	}
	t3, err := svc.Send(context.Background(), SendInput{
		OperationID: "op-cl-t3",
		WorkerID:    wwW.ID,
		Prompt:      "Use the Write tool to create created_by_claude.txt containing CLAUDE_WROTE_OK in the workspace root, then reply DONE.",
	})
	if err != nil {
		t.Fatalf("Send t3: %v", err)
	}
	out3, _ := waitForTerminalTurn(t, svc, t3.ID)
	if out3.State != TurnCompleted {
		t.Fatalf("Wait t3: state=%s errCode=%s errMsg=%s", out3.State, out3.ErrorCode, out3.ErrorMessage)
	}
	b, err := os.ReadFile(filepath.Join(ws, "created_by_claude.txt"))
	if err != nil || !strings.Contains(string(b), "CLAUDE_WROTE_OK") {
		t.Fatalf("expected created_by_claude.txt with CLAUDE_WROTE_OK, got %q err=%v", string(b), err)
	}
}
