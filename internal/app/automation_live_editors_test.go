package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/editors"
	"github.com/jackgan90/Asterhand/internal/operations"
)

func TestLocalCLIReadsAndWritesOpenEditorWithoutProfile(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	a := n.app
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	page := "test-window-123456789"
	channel := a.Editors.Commands(ctx, "web", page)
	go func() {
		text, revision := "unfinished", "initial"
		for q := range channel {
			target := map[string]any{"kind": "task_goal", "node_id": a.Config.Get().NodeID, "project_id": "workspace"}
			meta := map[string]any{"draft_id": "field", "kind": "task_goal", "target": target}
			var result any
			switch q.Action {
			case "list":
				result = []any{meta}
			case "get":
				meta["content"] = map[string]string{"text": text}
				meta["revision"] = revision
				result = meta
			case "update":
				var v struct{ Text string }
				_ = json.Unmarshal(q.Content, &v)
				text = v.Text
				revision = "updated"
				result = map[string]string{"state": "editable", "revision": revision}
			case "commit":
				result = map[string]string{"state": "accepted", "revision": revision}
			}
			_ = a.Editors.Respond("web", editors.Poll{Page: page, Reply: &editors.Reply{ID: q.ID, Data: compactResult(result)}})
		}
	}()
	deadline := time.Now().Add(time.Second)
	for len(a.Editors.Pages()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	run := func(name string, args any, write bool) automation.Response {
		t.Helper()
		var out, diag bytes.Buffer
		argv := []string{name, "--home", a.Config.Home, "--input", "-"}
		if write {
			argv = append(argv, "--operation-id", operations.NewID(time.Now()))
		}
		err := automation.RunCLI(ctx, argv, bytes.NewReader(compactResult(args)), &out, &diag, AutomationDefinitions())
		var exit *automation.ExitError
		if err != nil && !errors.As(err, &exit) {
			t.Fatal(err)
		}
		var resp automation.Response
		if err = json.Unmarshal(out.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	id := page + ":field"
	if listed := run("drafts_list", map[string]any{}, false); listed.Error != nil || !strings.Contains(string(compactResult(listed.Data)), id) {
		t.Fatal(listed)
	}
	if got := run("drafts_get", map[string]string{"draft_id": id}, false); got.Error != nil || !strings.Contains(string(compactResult(got.Data)), "unfinished") {
		t.Fatal(got)
	}
	if updated := run("drafts_update", map[string]any{"draft_id": id, "expected_revision": "initial", "content": map[string]string{"text": "finished"}}, true); updated.Error != nil {
		t.Fatal(updated.Error)
	}
	if stale := run("drafts_commit", map[string]string{"draft_id": id, "expected_revision": "initial"}, true); stale.Error == nil || stale.Error.Code != "conflict" {
		t.Fatal(stale)
	}
	if done := run("drafts_commit", map[string]string{"draft_id": id, "expected_revision": "updated"}, true); done.Error != nil || !strings.Contains(string(compactResult(done.Data)), "accepted") {
		t.Fatal(done)
	}
}
