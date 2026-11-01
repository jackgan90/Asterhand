package localcommands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
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
	return Command{Name: name, Script: script, Arguments: []string{}, Window: "background"}
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

func TestRegistryMergeRenameDeleteAndRestart(t *testing.T) {
	home := t.TempDir()
	r := Open(home, nil)
	c := testCommand(t, "backup")
	c.Aliases = []string{"备份"}
	c.Description = "原描述"
	first := saveCommand(t, r, c)
	c.Name, c.Aliases, c.Description = "save", []string{"保存"}, "后来的描述"
	merged := saveCommand(t, r, c)
	if !merged.Merged || merged.ID != first.ID || len(merged.Catalog.Commands) != 1 {
		t.Fatalf("merge: %+v", merged)
	}
	got := merged.Catalog.Commands[0]
	if !reflect.DeepEqual(got.Aliases, []string{"备份", "save", "保存"}) || got.Description != "原描述" {
		t.Fatalf("aliases: %+v", got)
	}
	got.Aliases = []string{"归档"}
	updated := saveCommand(t, r, got)
	if updated.Merged || updated.Catalog.Commands[0].Revision == got.Revision {
		t.Fatal("edit failed to replace aliases/revision")
	}
	reopened := Open(home, nil)
	snapshot, err := reopened.Snapshot()
	if err != nil || !reflect.DeepEqual(snapshot, updated.Catalog) {
		t.Fatalf("restart: %+v %v", snapshot, err)
	}
	snapshot.Commands[0].Aliases[0] = "external mutation"
	again, _ := reopened.Snapshot()
	if again.Commands[0].Aliases[0] != "归档" {
		t.Fatal("snapshot aliases shared")
	}
	deleted, err := reopened.Delete(first.ID, updated.Catalog.Revision)
	if err != nil || len(deleted.Commands) != 0 {
		t.Fatal(deleted, err)
	}
	if _, err := os.Stat(c.Script); err != nil {
		t.Fatal("delete touched script", err)
	}
}

func TestRegistryConflictsDifferentProfilesAndFailedSave(t *testing.T) {
	home := t.TempDir()
	r := Open(home, nil)
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
	c.Name, c.Arguments = "second", []string{""}
	saveCommand(t, r, c)
	c.Name, c.Directory = "third", t.TempDir()
	saveCommand(t, r, c)
	c.Name, c.Window = "fourth", "terminal"
	saveCommand(t, r, c)
	snapshot, _ := r.Snapshot()
	if len(snapshot.Commands) != 4 {
		t.Fatal("different profiles merged")
	}
	r.path = filepath.Join(home, "missing-parent", "commands.json")
	c.Name = "fifth"
	c.Arguments = []string{"different"}
	if _, err := r.Save(Mutation{Revision: snapshot.Revision, Command: c}); err == nil {
		t.Fatal("write should fail")
	}
	after, _ := r.Snapshot()
	if !reflect.DeepEqual(snapshot, after) {
		t.Fatal("failed save published configuration")
	}
}

func TestLaunchRechecksMissingRevisionAndSupportsIndependentConcurrentStarts(t *testing.T) {
	var starts atomic.Int32
	r := Open(t.TempDir(), func(Command) error { starts.Add(1); return nil })
	c := testCommand(t, "run")
	saved := saveCommand(t, r, c)
	c = saved.Catalog.Commands[0]
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if reply := r.Launch(ctx, c.ID, c.Revision); !reply.Started {
				t.Error(reply.Error)
			}
		}()
	}
	wg.Wait()
	if starts.Load() != 20 {
		t.Fatal(starts.Load())
	}
	updated := c
	updated.Description = "changed"
	saveCommand(t, r, updated)
	if reply := r.Launch(context.Background(), c.ID, c.Revision); reply.Error.Code != "conflict" {
		t.Fatal(reply)
	}
	current, _ := r.Snapshot()
	c = current.Commands[0]
	if err := os.Remove(c.Script); err != nil {
		t.Fatal(err)
	}
	if reply := r.Launch(context.Background(), c.ID, c.Revision); reply.Error.Code != "missing" {
		t.Fatal(reply)
	}
	if starts.Load() != 20 {
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
	r := Open(home, nil)
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
	r := Open(t.TempDir(), nil)
	c := testCommand(t, "quoted")
	c.Arguments = []string{`a" b & echo unexpected`}
	for _, ext := range []string{".bat", ".cmd"} {
		c.Script = filepath.Join(filepath.Dir(c.Script), "run"+ext)
		catalog, _ := r.Snapshot()
		if _, err := r.Save(Mutation{Revision: catalog.Revision, Command: c}); err == nil {
			t.Fatal("ambiguous batch argument accepted")
		}
	}
	c.Script = filepath.Join(filepath.Dir(c.Script), "run.ps1")
	saveCommand(t, r, c)
}

func TestPagedCatalogWireBudgetAndStalePagination(t *testing.T) {
	r := Open(t.TempDir(), nil)
	c := testCommand(t, "a")
	for i := range 35 {
		c.Name = "command" + string(rune('A'+i))
		c.Arguments = []string{c.Name}
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
	c.Arguments = []string{"added"}
	saveCommand(t, r, c)
	if reply := r.List(wire.Request{Action: "list", Offset: 32, Catalog: first.Catalog}); reply.Error.Code != "conflict" {
		t.Fatal(reply)
	}
}
