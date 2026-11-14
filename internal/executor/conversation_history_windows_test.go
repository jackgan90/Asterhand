package executor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/conversation/wire"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

func historyWorkspace(t *testing.T, scope workspace.Scope, progress []workspace.Step) *workspace.Service {
	t.Helper()
	root := t.TempDir()
	files, err := workspace.Open(filepath.Join(root, "files"), filepath.Join(root, "journal"), "history-workspace")
	if err != nil {
		t.Fatal(err)
	}
	if err := files.Configure(true, scope.Policy); err != nil {
		t.Fatal(err)
	}
	if err := files.AcquireTurnScope(scope); err != nil {
		t.Fatal(err)
	}
	_, err = files.Begin(workspace.Begin{ID: "history-call", Scope: scope, Tool: "progress_card", Fingerprint: "history-fixture", Progress: progress})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { files.RuntimeStopped() })
	return files
}

func TestHistoryToolEnrichmentRequiresMatchingScopeAndPreservesNativeFailure(t *testing.T) {
	scope := workspace.Scope{Conversation: "history-conversation", Turn: "history-turn", Policy: "history-policy"}
	files := historyWorkspace(t, scope, []workspace.Step{{Text: "Prepare files", Status: "completed"}})
	if _, err := files.End("history-call", "completed", "private callback detail"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, conversation, turn, tool, initial, expected string
	}{
		{"matching", scope.Conversation, scope.Turn, "progress_card", "unknown", "completed"},
		{"native failure", scope.Conversation, scope.Turn, "progress_card", "failed", "failed"},
		{"other conversation", "other", scope.Turn, "progress_card", "unknown", "unknown"},
		{"other turn", scope.Conversation, "other", "progress_card", "unknown", "unknown"},
		{"other tool", scope.Conversation, scope.Turn, "write", "unknown", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := conversation.Page{Messages: []conversation.Message{{ID: "message", Turn: tc.turn,
				Blocks: []conversation.Block{{Type: "text", Text: "before"}, {Type: "tool", Tool: &wire.ToolActivity{
					ID: "history-call", Turn: tc.turn, Tool: tc.tool, State: tc.initial}}, {Type: "text", Text: "after"}}}}}
			enrichHistoryTools(&page, tc.conversation, files)
			blocks := page.Messages[0].Blocks
			if len(blocks) != 3 || blocks[0].Text != "before" || blocks[2].Text != "after" || blocks[1].Tool.State != tc.expected {
				t.Fatalf("wrong placement or state: %+v", blocks)
			}
			matched := tc.expected != "unknown"
			if (len(blocks[1].Tool.Progress) == 1) != matched || (blocks[1].Tool.Sequence > 0) != matched {
				t.Fatal("journal metadata did not follow the exact call scope")
			}
			data, _ := json.Marshal(page)
			if strings.Contains(string(data), "private callback detail") {
				t.Fatal("private journal detail exposed")
			}
		})
	}
}

func TestHistoryToolProgressCannotExceedPageBudget(t *testing.T) {
	scope := workspace.Scope{Conversation: "history-conversation", Turn: "history-turn", Policy: "history-policy"}
	steps := make([]workspace.Step, 20)
	for i := range steps {
		steps[i] = workspace.Step{Text: strings.Repeat("x", 200), Status: "pending"}
	}
	files := historyWorkspace(t, scope, steps)
	page := conversation.Page{Messages: []conversation.Message{{ID: "message", Turn: scope.Turn,
		Blocks: []conversation.Block{{Type: "text", Text: strings.Repeat("x", 57*1024)}, {Type: "tool", Tool: &wire.ToolActivity{
			ID: "history-call", Turn: scope.Turn, Tool: "progress_card", State: "unknown"}}}}}}
	enrichHistoryTools(&page, scope.Conversation, files)
	data, _ := json.Marshal(page)
	activity := page.Messages[0].Blocks[1].Tool
	if len(data) > conversation.PageBytes || len(activity.Progress) != 0 || activity.State != "started" {
		t.Fatalf("unbounded tool progress or lost state: bytes=%d state=%s steps=%d", len(data), activity.State, len(activity.Progress))
	}
}
