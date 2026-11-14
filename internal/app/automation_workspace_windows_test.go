//go:build windows

package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

type alternateWorkspaceRuntime struct {
	automationChatRuntime
	files *workspace.Service
}

func (r *alternateWorkspaceRuntime) Workspace() (*workspace.Service, error) { return r.files, nil }
func (r *alternateWorkspaceRuntime) SetWorkspacePolicy(_ context.Context, enabled bool, revision string) (workspace.Status, error) {
	err := r.files.Configure(enabled, revision)
	return r.files.Status(), err
}

func TestAutomationWorkspaceSharesVersionsScopeAndReceipts(t *testing.T) {
	a := newNode(t, &fakeExecutor{}).app
	a.Chat.Close()
	root := t.TempDir()
	files, err := workspace.Open(filepath.Join(root, "files"), filepath.Join(root, "versions"), "workspace-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err = files.Configure(true, "policy"); err != nil {
		t.Fatal(err)
	}
	a.Chat = conversation.New(a.Config.Get().NodeID, a.Store, &alternateWorkspaceRuntime{files: files})
	_, token := grantTestAutomation(t, a, []string{"read", "edit"}, map[string][]string{"workspaces": {files.ID()}, "conversations": {"*"}})
	args := automation.WorkspaceMutate{Workspace: files.ID(), Policy: "policy", Mutations: []workspace.Mutation{{Action: "write", Path: "data.txt", Expected: workspace.MissingRevision, Content: strings.Repeat("中<", 20000)}}}
	id := operations.NewID(time.Now())
	reply := testAutomationCall(a, token, "workspace_files.mutate", id, args)
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	var created workspace.Operation
	json.Unmarshal(compactResult(reply.Data), &created)
	if created.State != "completed" {
		t.Fatal(created)
	}
	for range 2 {
		if result := testAutomationCall(a, token, "workspace_files.mutate", id, args); result.Error != nil {
			t.Fatal(result.Error)
		}
	}
	items, _ := files.Operations("", 0)
	if len(items) != 1 {
		t.Fatalf("replayed mutation: %d", len(items))
	}
	read := testAutomationCall(a, token, "workspace_files.read", "", automation.WorkspaceRead{Workspace: files.ID(), Path: "data.txt"})
	if read.Error != nil || len(compactResult(read)) > automation.MaxResponse {
		t.Fatal("unbounded or failed read", read.Error)
	}
	var fragment workspace.Fragment
	json.Unmarshal(compactResult(read.Data), &fragment)
	if !fragment.More {
		t.Fatal("fixture did not span pages")
	}
	if _, err = files.Mutate("human-edit", workspace.Scope{Policy: "policy"}, []workspace.Mutation{{Action: "write", Path: "data.txt", Expected: fragment.Revision, Content: "human"}}); err != nil {
		t.Fatal(err)
	}
	stale := testAutomationCall(a, token, "workspace_versions.restore", operations.NewID(time.Now()), automation.WorkspaceRestore{Workspace: files.ID(), Policy: "policy", Version: created.ID, Path: "data.txt", Revision: fragment.Revision})
	if stale.Error == nil || stale.Error.Code != "revision_conflict" {
		t.Fatalf("stale restore %+v", stale)
	}
	if err = files.AcquireTurn("native-turn", "policy"); err != nil {
		t.Fatal(err)
	}
	busy := testAutomationCall(a, token, "workspace_files.mutate", operations.NewID(time.Now()), args)
	if busy.Error == nil || busy.Error.Code != "busy" {
		t.Fatal("native lease was bypassed", busy)
	}
	files.ReleaseTurn("native-turn", true)
	_, other := grantTestAutomation(t, a, []string{"read", "edit"}, map[string][]string{"workspaces": {"different"}})
	if testAutomationCall(a, other, "workspace_files.read", "", automation.WorkspaceRead{Workspace: files.ID(), Path: "data.txt"}).Error == nil {
		t.Fatal("scope leaked")
	}
	if testAutomationCall(a, token, "workspace_files.read", "", automation.WorkspaceRead{Workspace: files.ID(), Path: "../outside"}).Error == nil {
		t.Fatal("path escaped")
	}
	data, err := os.ReadFile(filepath.Join(files.Root(), "data.txt"))
	if err != nil || string(data) != "human" {
		t.Fatal("file changed", err)
	}
}
