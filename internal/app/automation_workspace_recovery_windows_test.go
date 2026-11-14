//go:build windows

package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

func TestWorkspaceReceiptRecoversOnlyDurableDomainEvidence(t *testing.T) {
	a := newNode(t, &fakeExecutor{}).app
	a.Chat.Close()
	root := t.TempDir()
	files, err := workspace.Open(filepath.Join(root, "files"), filepath.Join(root, "versions"), "workspace-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if err = files.Configure(true, "policy"); err != nil {
		t.Fatal(err)
	}
	a.Chat = conversation.New(a.Config.Get().NodeID, a.Store, &alternateWorkspaceRuntime{files: files})
	scope := workspace.Scope{Policy: "policy"}
	for _, name := range []string{"workspace_files.mutate", "workspace_versions.restore", "workspace_operations.resolve"} {
		t.Run(name, func(t *testing.T) {
			identity := operations.Identity{Actor: "recovery-test", Node: a.Config.Get().NodeID, ID: operations.NewID(time.Now()), Name: name, Digest: "fixture", Target: files.ID()}
			receipt, _, err := a.Store.Begin(identity, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			before, err := a.reconcileAutomationOperation(receipt)
			if err != nil || before.Terminal() {
				t.Fatal("absence was treated as success", before, err)
			}
			var op workspace.Operation
			switch name {
			case "workspace_files.mutate":
				op, err = files.Mutate(identity.NativeID(), scope, []workspace.Mutation{{Action: "write", Path: "file.txt", Expected: workspace.MissingRevision, Content: "saved"}})
			case "workspace_versions.restore":
				versions, _, e := files.Versions("file.txt", 0)
				if e != nil || len(versions) != 1 {
					t.Fatal(versions, e)
				}
				current, e := files.Read("file.txt", "", 0)
				if e != nil {
					t.Fatal(e)
				}
				op, err = files.Restore(identity.NativeID(), scope, versions[0].ID, "file.txt", current.Revision)
			case "workspace_operations.resolve":
				_, err = files.Begin(workspace.Begin{ID: "interrupted", Scope: scope, Tool: "write", Fingerprint: "fixture", Targets: []workspace.Target{{Path: "pending.txt"}}, NewBytes: 3})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = files.Write("interrupted", "pending.txt", []byte("new"), false); err != nil {
					t.Fatal(err)
				}
				if err = files.RuntimeStopped(); err != nil {
					t.Fatal(err)
				}
				op, err = files.ResolveRequest(identity.NativeID(), "interrupted", "Runtime stopped; inspected files")
			}
			if err != nil {
				t.Fatal(err)
			}
			initial, _ := files.Operations("", 0)
			for range 2 {
				recovered, e := a.reconcileAutomationOperation(receipt)
				if e != nil || recovered.State != "completed" || !recovered.Accepted {
					t.Fatal(recovered, e)
				}
				if string(recovered.Result) != string(compactResult(map[string]string{"activity_id": op.ID, "state": op.State})) {
					t.Fatal(recovered)
				}
			}
			final, _ := files.Operations("", 0)
			if len(final) != len(initial) {
				t.Fatal("reconciliation replayed a file operation")
			}
		})
	}
}
