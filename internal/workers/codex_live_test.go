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

// TestCodexAdapterLiveTwoTurnReview exercises the real local codex app-server
// binary over Windows OrdinaryToken + kill-on-close JobObject across two turns
// on the same persistent worker identity. Opt-in via ASTERHAND_LIVE_CODEX=1.
func TestCodexAdapterLiveTwoTurnReview(t *testing.T) {
	if os.Getenv("ASTERHAND_LIVE_CODEX") != "1" {
		t.Skip("set ASTERHAND_LIVE_CODEX=1 to run live Codex app-server integration test")
	}
	home := t.TempDir()
	wsDir := t.TempDir()
	srcFile := filepath.Join(wsDir, "calc.go")
	if err := os.WriteFile(srcFile, []byte("package calc\n\nfunc Divide(a, b int) int {\n\treturn a / b\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	hs, err := harnesses.Open(home, `C:\Asterhand\asterhand.exe`)
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err := hs.ResolveWorkspace(wsDir, harnesses.AccessReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	tr := true
	if _, err := hs.UpdatePolicy(harnesses.UpdatePolicyInput{
		HarnessID:           harnesses.HarnessCodex,
		CollabWorkerEnabled: &tr,
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(home, hs, NewCodexAdapter())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = svc.Close(ctx)
	}()

	w, err := svc.Create(context.Background(), CreateInput{
		OperationID:   "live_create_1",
		HarnessID:     harnesses.HarnessCodex,
		WorkspaceRoot: ws.RootPath,
		Alias:         "codex-reviewer",
		AccessMode:    harnesses.AccessReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Turn 1: find the divide-by-zero bug.
	t1, err := svc.Send(context.Background(), SendInput{
		OperationID:         "live_send_1",
		WorkerID:            w.ID,
		ExpectedWorkspaceID: ws.ID,
		Prompt:              "Review calc.go in the current workspace. Identify the primary bug in 1-2 short sentences and include the marker BUG_ZERO.",
		ArtifactPaths:       []string{"calc.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var done1 Turn
	var ans1 string
	for i := 0; i < 6; i++ {
		done1, ans1, _, err = svc.Wait(context.Background(), t1.ID, ws.ID, 25*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if IsTurnTerminal(done1.State) {
			break
		}
	}
	if done1.State != TurnCompleted || !done1.AnswerAvailable || !strings.Contains(ans1, "BUG_ZERO") {
		t.Fatalf("Turn 1 failed: state=%s avail=%v src=%s err=%s ans=%q", done1.State, done1.AnswerAvailable, done1.AnswerSource, done1.ErrorMessage, ans1)
	}
	t.Logf("Turn 1 OK (thread=%s, turn=%s, source=%s): %s", done1.NativeSessionID, done1.NativeTurnID, done1.AnswerSource, ans1)

	// Fix calc.go and send Turn 2 to the EXACT SAME persistent worker!
	if err := os.WriteFile(srcFile, []byte("package calc\n\nfunc Divide(a, b int) (int, bool) {\n\tif b == 0 {\n\t\treturn 0, false\n\t}\n\treturn a / b, true\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t2, err := svc.Send(context.Background(), SendInput{
		OperationID:         "live_send_2",
		WorkerID:            w.ID,
		ExpectedWorkspaceID: ws.ID,
		Prompt:              "Re-review calc.go now that I fixed the bug from your previous review. Confirm in 1 sentence whether the previous issue is fixed and include FIXED_OK.",
		ArtifactPaths:       []string{"calc.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var done2 Turn
	var ans2 string
	for i := 0; i < 6; i++ {
		done2, ans2, _, err = svc.Wait(context.Background(), t2.ID, ws.ID, 25*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if IsTurnTerminal(done2.State) {
			break
		}
	}
	if done2.State != TurnCompleted || !done2.AnswerAvailable || !strings.Contains(ans2, "FIXED_OK") {
		t.Fatalf("Turn 2 failed: state=%s avail=%v src=%s err=%s ans=%q", done2.State, done2.AnswerAvailable, done2.AnswerSource, done2.ErrorMessage, ans2)
	}
	if done2.NativeSessionID != done1.NativeSessionID {
		t.Fatalf("expected same native thread across turns: %s vs %s", done1.NativeSessionID, done2.NativeSessionID)
	}
	t.Logf("Turn 2 OK (same thread=%s, turn=%s): %s", done2.NativeSessionID, done2.NativeTurnID, ans2)
}
