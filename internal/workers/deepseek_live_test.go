package workers

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/harnesses"
)

// TestDeepSeekAdapterLive exercises real local DeepSeek Harness (dsh --profile acp)
// through workers.Service in an isolated temp workspace: read_only, cross-process
// session/resume, and workspace_write.
// Run explicitly with: ASTERHAND_LIVE_DEEPSEEK=1 go test -v -run TestDeepSeekAdapterLive ./internal/workers
func TestDeepSeekAdapterLive(t *testing.T) {
	if os.Getenv("ASTERHAND_LIVE_DEEPSEEK") != "1" {
		t.Skip("set ASTERHAND_LIVE_DEEPSEEK=1 to run real DeepSeek Harness ACP integration test")
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
		HarnessID:           harnesses.HarnessDeepSeek,
		CollabWorkerEnabled: &tBool,
		AllowWorkspaceWrite: &tBool,
	}); err != nil {
		t.Fatalf("enable deepseek worker: %v", err)
	}
	svc, err := NewService(home, hs, NewDeepSeekAdapter())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer func() { _ = svc.Close(context.Background()) }()

	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "note.txt"), []byte("DEEPSEEK_LIVE_TOKEN_888"), 0644); err != nil {
		t.Fatalf("write note.txt: %v", err)
	}

	// 1. Read-only worker + cross-process resume
	roW, err := svc.Create(context.Background(), CreateInput{
		OperationID:     "op-dsh-ro",
		HarnessID:       harnesses.HarnessDeepSeek,
		WorkspaceRoot:   ws,
		Alias:           "dsh-ro",
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
		OperationID: "op-dsh-t1",
		WorkerID:    roW.ID,
		Prompt:      "Read note.txt and reply with its exact token.",
	})
	if err != nil {
		t.Fatalf("Send t1: %v", err)
	}
	out1, ans1 := waitForTerminalTurn(t, svc, t1.ID)
	if out1.State != TurnCompleted || !out1.AnswerAvailable || !strings.Contains(ans1, "DEEPSEEK_LIVE_TOKEN_888") {
		t.Fatalf("Wait t1: state=%s avail=%v errCode=%s errMsg=%s ans=%q", out1.State, out1.AnswerAvailable, out1.ErrorCode, out1.ErrorMessage, ans1)
	}

	// Force resident close to verify cross-process session/resume
	svc.mu.Lock()
	if rp, ok := svc.residents[roW.ID]; ok {
		delete(svc.residents, roW.ID)
		svc.mu.Unlock()
		_ = svc.adapters[roW.HarnessID].CloseThread(rp.handle)
	} else {
		svc.mu.Unlock()
	}

	t2, err := svc.Send(context.Background(), SendInput{
		OperationID: "op-dsh-t2",
		WorkerID:    roW.ID,
		Prompt:      "Without reading files again, repeat the exact token from our previous turn.",
	})
	if err != nil {
		t.Fatalf("Send t2: %v", err)
	}
	out2, ans2 := waitForTerminalTurn(t, svc, t2.ID)
	if out2.State != TurnCompleted || !strings.Contains(ans2, "DEEPSEEK_LIVE_TOKEN_888") {
		t.Fatalf("Wait t2: state=%s errCode=%s errMsg=%s ans=%q", out2.State, out2.ErrorCode, out2.ErrorMessage, ans2)
	}

	// 2. Workspace-write worker
	wwW, err := svc.Create(context.Background(), CreateInput{
		OperationID:     "op-dsh-ww",
		HarnessID:       harnesses.HarnessDeepSeek,
		WorkspaceRoot:   ws,
		Alias:           "dsh-ww",
		AccessMode:      harnesses.AccessWorkspaceWrite,
		ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatalf("Create workspace_write: %v", err)
	}
	t3, err := svc.Send(context.Background(), SendInput{
		OperationID: "op-dsh-t3",
		WorkerID:    wwW.ID,
		Prompt:      "Create a file named created_by_dsh.txt containing DEEPSEEK_WROTE_OK in the workspace root, then reply DONE.",
	})
	if err != nil {
		t.Fatalf("Send t3: %v", err)
	}
	out3, _ := waitForTerminalTurn(t, svc, t3.ID)
	if out3.State != TurnCompleted {
		t.Fatalf("Wait t3: state=%s errCode=%s errMsg=%s", out3.State, out3.ErrorCode, out3.ErrorMessage)
	}
	b, err := os.ReadFile(filepath.Join(ws, "created_by_dsh.txt"))
	if err != nil || !strings.Contains(string(b), "DEEPSEEK_WROTE_OK") {
		t.Fatalf("expected created_by_dsh.txt with DEEPSEEK_WROTE_OK, got %q err=%v", string(b), err)
	}
}
