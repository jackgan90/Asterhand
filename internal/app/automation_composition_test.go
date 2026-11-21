package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/operations"
)

func TestBatchKeepsFixedIdentitiesAndNeverReplays(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	a := n.app
	p, token := grantTestAutomation(t, a, []string{"read", "edit"}, map[string][]string{"commands": {"*"}})
	v := automation.BatchSubmit{OnError: "continue"}
	for i := range 8 {
		v.Items = append(v.Items, automation.BatchItem{Operation: "commands_create", Arguments: compactResult(automation.CommandInput{Name: "batch_" + strconv.Itoa(i), Graph: apiTestGraph(filepath.Join(t.TempDir(), "missing.ps1"))})})
	}
	id := operations.NewID(time.Now())
	reply := testAutomationCall(a, token, "batches_submit", id, v)
	if reply.Error != nil || reply.Receipt == nil || !reply.Receipt.Accepted {
		t.Fatal(reply)
	}
	await(t, "batch finished", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.runningBatches == 0 })
	catalog, _ := a.Commands.Snapshot()
	if len(catalog.Commands) != 8 {
		t.Fatal("lost batch item", len(catalog.Commands))
	}
	for range 2 {
		reply = testAutomationCall(a, token, "batches_submit", id, v)
		if reply.Error != nil {
			t.Fatal(reply)
		}
	}
	catalog, _ = a.Commands.Snapshot()
	if len(catalog.Commands) != 8 {
		t.Fatal("batch replayed")
	}
	var refs []string
	cursor := ""
	for {
		reply = testAutomationCall(a, token, "batches_get", "", automation.BatchGet{ID: id, Cursor: cursor})
		if reply.Error != nil {
			t.Fatal(reply)
		}
		b := compactResult(reply.Data)
		var page struct {
			Items []struct {
				Receipt operations.Receipt `json:"receipt"`
			} `json:"items"`
			Next string `json:"next"`
		}
		if err := json.Unmarshal(b, &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.Receipt.State != "completed" {
				t.Fatal(item)
			}
			refs = append(refs, item.Receipt.ID)
		}
		cursor = page.Next
		if cursor == "" {
			break
		}
	}
	if len(refs) != 8 {
		t.Fatal(refs)
	}
	// A restarted batch with a planned item but no admission proof never dispatches.
	original, _ := a.Store.Lookup(p.ID, a.Config.Get().NodeID, id)
	state, _ := a.Store.AutomationState(original.Identity)
	var persisted batchState
	_ = json.Unmarshal(state.Data, &persisted)
	persisted.Items[0].ID = operations.NewID(time.Now())
	persisted.Items[0].State = "dispatching"
	state.Data = compactResult(persisted)
	state.Complete = false
	if err := a.Store.SaveAutomationState(state); err != nil {
		t.Fatal(err)
	}
	a.Automation.Registry.Instance = "replacement"
	reply = testAutomationCall(a, token, "batches_get", "", automation.BatchGet{ID: id})
	var page struct {
		Items []struct {
			State string `json:"state"`
		} `json:"items"`
	}
	_ = json.Unmarshal(compactResult(reply.Data), &page)
	if reply.Error != nil || page.Items[0].State != "not_dispatched" {
		t.Fatal(reply)
	}
}

func TestBatchPreflightRejectsForbiddenItemBeforeAnyWrite(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	a := n.app
	_, token := grantTestAutomation(t, a, []string{"read", "edit"}, map[string][]string{"commands": {"*"}})
	v := automation.BatchSubmit{Items: []automation.BatchItem{
		{Operation: "commands_create", Arguments: compactResult(automation.CommandInput{Name: "safe", Graph: apiTestGraph(filepath.Join(t.TempDir(), "missing.ps1"))})},
		{Operation: "node_stop", Arguments: compactResult(automation.NodeStop{Instance: a.Automation.Registry.Instance})},
	}}
	reply := testAutomationCall(a, token, "batches_submit", operations.NewID(time.Now()), v)
	catalog, _ := a.Commands.Snapshot()
	if reply.Error == nil || len(catalog.Commands) != 0 {
		t.Fatal(reply, catalog)
	}
}

func TestMultiReadIsBoundedAndNeverAcceptsWrites(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	a := n.app
	_, token := grantTestAutomation(t, a, []string{"read", "execute"}, map[string][]string{"node": {"*"}})
	v := automation.MultiRead{}
	for range 99 {
		v.Items = append(v.Items, automation.BatchItem{Operation: "node_version", Arguments: json.RawMessage(`{}`)})
	}
	v.Items = append(v.Items, automation.BatchItem{Operation: "node_stop", Arguments: compactResult(automation.NodeStop{Instance: a.Automation.Registry.Instance, StopActive: true})})
	reply := testAutomationCall(a, token, "read_many", "", v)
	if reply.Error != nil || a.Stopping() {
		t.Fatal(reply)
	}
	b, _ := json.Marshal(reply)
	if len(b) > automation.MaxResponse {
		t.Fatal(len(b))
	}
}

func TestDiagnosticAdmissionSurvivesLostJournalFinish(t *testing.T) {
	f := &fakeExecutor{}
	n := newNode(t, f)
	a := n.app
	p, token := grantTestAutomation(t, a, []string{"read", "execute"}, map[string][]string{"node": {"*"}})
	id := operations.NewID(time.Now())
	args := automation.Empty{}
	identity := operations.Identity{Actor: p.ID, Node: a.Config.Get().NodeID, ID: id, Name: "diagnostics_run", Digest: operations.Hash(args), Target: a.Config.Get().NodeID}
	if _, _, err := a.Store.Begin(identity, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.startProbe(&identity, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	await(t, "diagnostic finished", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return !a.probeRunning })
	reply := testAutomationCall(a, token, "diagnostics_run", id, args)
	if reply.Error != nil || !reply.Receipt.Accepted || f.probes.Load() != 1 {
		t.Fatal(reply, f.probes.Load())
	}
	reply = testAutomationCall(a, token, "diagnostics_inspect", "", map[string]string{"operation_id": id})
	if reply.Error != nil {
		t.Fatal(reply)
	}
	// Inspection and repeated discovery are never model calls.
	for range 3 {
		testAutomationCall(a, token, "capabilities", "", automation.Empty{})
	}
	if f.probes.Load() != 1 {
		t.Fatal("query ran model")
	}
	_ = context.Background()
}
