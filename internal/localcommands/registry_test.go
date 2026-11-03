package localcommands

import (
	"context"
	"encoding/json"
	"github.com/jackgan90/Asterhand/internal/model"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

func testCommand(t *testing.T, name string) Command {
	t.Helper()
	script := filepath.Join(t.TempDir(), "脚本 space.ps1")
	if err := os.WriteFile(script, []byte("exit 0"), 0600); err != nil {
		t.Fatal(err)
	}
	return Command{Name: name, Graph: singleScriptGraph(script)}
}

func singleScriptGraph(script string) *ScriptGraph {
	start, step, end := model.ID(), model.ID(), model.ID()
	return &ScriptGraph{Parallelism: 1, Nodes: []GraphNode{{ID: start, Kind: "start"}, {ID: step, Kind: "script", Name: "step", Script: script, Arguments: []string{}, FailurePolicy: AbortGraph}, {ID: end, Kind: "end"}}, Edges: []GraphEdge{{start, step}, {step, end}}}
}

func saveCommand(t *testing.T, r *Registry, c Command) SaveResult {
	t.Helper()
	catalog, err := r.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Save(Mutation{Revision: catalog.Revision, Command: c})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRegistryIndependentRenameDeleteAndRestart(t *testing.T) {
	home := t.TempDir()
	r := Open(home)
	c := testCommand(t, "backup")
	c.Aliases = []string{"备份"}
	c.Description = "原描述"
	first := saveCommand(t, r, c)
	c.Name, c.Aliases, c.Description = "save", []string{"保存"}, "后来的描述"
	second := saveCommand(t, r, c)
	if second.ID == first.ID || len(second.Catalog.Commands) != 2 {
		t.Fatal("equal graphs merged", second)
	}
	got := second.Catalog.Commands[0]
	got.Aliases = []string{"归档"}
	updated := saveCommand(t, r, got)
	if updated.Catalog.Commands[0].Revision == got.Revision {
		t.Fatal("edit retained revision")
	}
	reopened := Open(home)
	snapshot, err := reopened.Snapshot()
	if err != nil || !reflect.DeepEqual(snapshot, updated.Catalog) {
		t.Fatal(snapshot, err)
	}
	snapshot.Commands[1].Aliases[0] = "external mutation"
	snapshot.Commands[1].Graph.Nodes[1].Script = "mutated"
	again, _ := reopened.Snapshot()
	if again.Commands[1].Aliases[0] != "归档" || again.Commands[1].Graph.Nodes[1].Script == "mutated" {
		t.Fatal("shared snapshot")
	}
	if err = reopened.ConfigureRuns(NewRunManager(home, &fakeBackend{}, nil)); err != nil {
		t.Fatal(err)
	}
	deleted, err := reopened.Delete(first.ID, updated.Catalog.Revision)
	if err != nil || len(deleted.Commands) != 1 {
		t.Fatal(deleted, err)
	}
	if _, err = os.Stat(c.Graph.Nodes[1].Script); err != nil {
		t.Fatal("delete touched script", err)
	}
}

func TestRegistryConflictsDifferentProfilesAndFailedSave(t *testing.T) {
	home := t.TempDir()
	r := Open(home)
	c := testCommand(t, "first")
	initial, _ := r.Snapshot()
	first := saveCommand(t, r, c)
	if _, err := r.Save(Mutation{Revision: initial.Revision, Command: c}); Failure(err).Error.Code != "conflict" {
		t.Fatal(err)
	}
	other := testCommand(t, "other")
	other.Aliases = []string{"FIRST"}
	if _, err := r.Save(Mutation{Revision: first.Catalog.Revision, Command: other}); Failure(err).Error.Code != "name_conflict" {
		t.Fatal(err)
	}
	c.Name, c.Graph.Nodes[1].Arguments = "second", []string{""}
	saveCommand(t, r, c)
	c.Name, c.Graph.Nodes[1].Directory = "third", t.TempDir()
	saveCommand(t, r, c)
	c.Name, c.AllowConcurrent = "fourth", true
	saveCommand(t, r, c)
	snapshot, _ := r.Snapshot()
	if len(snapshot.Commands) != 4 {
		t.Fatal("different profiles merged")
	}
	r.path = filepath.Join(home, "missing-parent", "commands.json")
	c.Name = "fifth"
	c.Graph.Nodes[1].Arguments = []string{"different"}
	if _, err := r.Save(Mutation{Revision: snapshot.Revision, Command: c}); err == nil {
		t.Fatal("write should fail")
	}
	after, _ := r.Snapshot()
	if !reflect.DeepEqual(snapshot, after) {
		t.Fatal("failed save published configuration")
	}
}

func TestLaunchRechecksMissingRevision(t *testing.T) {
	r, _, b, _, c := managedFixture(t)
	updated := c
	updated.Description = "changed"
	saveCommand(t, r, updated)
	if reply := r.Launch(context.Background(), c.ID, c.Revision); reply.Error.Code != "conflict" {
		t.Fatal(reply)
	}
	c = mustSnapshot(t, r).Commands[0]
	if err := os.Remove(c.Graph.Nodes[1].Script); err != nil {
		t.Fatal(err)
	}
	if reply := r.Launch(context.Background(), c.ID, c.Revision); reply.Error.Code != "missing" {
		t.Fatal(reply)
	}
	if b.count.Load() != 0 {
		t.Fatal("invalid launch reached executor")
	}
	deadline := time.Now().Add(time.Second)
	for {
		reply := r.List(wire.Request{Action: "list"})
		if len(reply.Entries) != 1 {
			t.Fatal("missing command filtered out")
		}
		if reply.Entries[0].State == "missing" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing status not refreshed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCorruptConfigurationStaysUntouched(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "local-commands.json")
	invalid := []byte(`{"schema_version":900}`)
	if err := os.WriteFile(path, invalid, 0600); err != nil {
		t.Fatal(err)
	}
	r := Open(home)
	if _, err := r.Snapshot(); err == nil {
		t.Fatal("invalid catalog accepted")
	}
	if _, err := r.Save(Mutation{Command: testCommand(t, "recover")}); err == nil {
		t.Fatal("invalid catalog overwritten")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(invalid) {
		t.Fatal("invalid data lost")
	}
}

func TestBatchQuotesRejectedBeforeSave(t *testing.T) {
	r := Open(t.TempDir())
	c := testCommand(t, "quoted")
	c.Graph.Nodes[1].Arguments = []string{`a" b & echo unexpected`}
	for _, ext := range []string{".bat", ".cmd"} {
		c.Graph.Nodes[1].Script = filepath.Join(filepath.Dir(c.Graph.Nodes[1].Script), "run"+ext)
		catalog, _ := r.Snapshot()
		if _, err := r.Save(Mutation{Revision: catalog.Revision, Command: c}); err == nil {
			t.Fatal("ambiguous batch argument accepted")
		}
	}
	c.Graph.Nodes[1].Script = filepath.Join(filepath.Dir(c.Graph.Nodes[1].Script), "run.ps1")
	saveCommand(t, r, c)
}

func TestPagedCatalogWireBudgetAndStalePagination(t *testing.T) {
	r := Open(t.TempDir())
	c := testCommand(t, "a")
	for i := range 35 {
		c.Name = "command" + string(rune('A'+i))
		c.Graph.Nodes[1].Arguments = []string{c.Name}
		if i >= 26 {
			c.Name = "more" + string(rune('A'+i-26))
		}
		saveCommand(t, r, c)
	}
	first := r.List(wire.Request{Action: "list"})
	if len(first.Entries) != 32 || first.Next != 32 {
		t.Fatal(first)
	}
	encoded, _ := json.Marshal(first)
	if len(encoded) >= 128*1024 {
		t.Fatal("IPC oversized")
	}
	second := r.List(wire.Request{Action: "list", Offset: first.Next, Catalog: first.Catalog})
	if len(second.Entries) != 3 || second.Next != 0 {
		t.Fatal(second)
	}
	c.Name = "added"
	c.Graph.Nodes[1].Arguments = []string{"added"}
	saveCommand(t, r, c)
	if reply := r.List(wire.Request{Action: "list", Offset: 32, Catalog: first.Catalog}); reply.Error.Code != "conflict" {
		t.Fatal(reply)
	}
}
