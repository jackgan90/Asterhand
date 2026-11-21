//go:build windows

package app

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/operations"
)

// Keep an admitted execution alive without depending on process privileges.
type batchDeleteBackend struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *batchDeleteBackend) Available() bool                { return true }
func (b *batchDeleteBackend) Reconcile(string) (bool, error) { return true, nil }
func (b *batchDeleteBackend) Start(localcommands.ScriptSpec, string) (localcommands.Execution, error) {
	close(b.entered)
	return b, nil
}
func (b *batchDeleteBackend) Stdout() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (b *batchDeleteBackend) Stderr() io.ReadCloser { return io.NopCloser(strings.NewReader("")) }
func (b *batchDeleteBackend) Wait(context.Context) (localcommands.ExitEvidence, error) {
	<-b.release
	return localcommands.ExitEvidence{Kind: "unknown"}, nil
}
func (b *batchDeleteBackend) ForceStop() error { b.once.Do(func() { close(b.release) }); return nil }
func (b *batchDeleteBackend) Close() error     { return nil }

func TestCommandBatchDeleteProtectsRunningCommandAcrossEntrypoints(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	if err := n.app.Commands.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	backend := &batchDeleteBackend{entered: make(chan struct{}), release: make(chan struct{})}
	defer backend.ForceStop()
	if err := n.app.Commands.ConfigureRuns(localcommands.NewRunManager(t.TempDir(), backend, nil)); err != nil {
		t.Fatal(err)
	}
	if err := n.app.Commands.SetJournal(n.app.Store); err != nil {
		t.Fatal(err)
	}
	call := batchDeleteHTTP(t, n.app)
	busy, _ := batchDeleteCommand(t, n.app, "running")
	idle, _ := batchDeleteCommand(t, n.app, "idle")
	run := n.app.commandCall(context.Background(), wire.Request{Action: "launch", ID: busy.ID, Revision: busy.Revision, Sequence: 1})
	if !run.Started {
		t.Fatal(run.Error)
	}
	select {
	case <-backend.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not reach the controlled backend")
	}
	out := call(map[string]any{"items": []commandDeleteItem{busy, idle}}, true, true, true)
	var result struct {
		Results []automation.Response `json:"results"`
	}
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &result) != nil || len(result.Results) != 2 {
		t.Fatal(out.Code, out.Body.String())
	}
	if result.Results[0].Error == nil || result.Results[1].Error != nil {
		t.Fatal(result)
	}
	_, token := grantTestAutomation(t, n.app, []string{"read", "edit"}, map[string][]string{"commands": {"*"}})
	public := testAutomationCall(n.app, token, "commands_delete", operations.NewID(time.Now()), automation.CommandVersion{ID: busy.ID, Revision: busy.Revision})
	if public.Error == nil || public.Error.Code != result.Results[0].Error.Code {
		t.Fatal(public, result)
	}
	if _, err := n.app.Commands.Get(busy.ID); err != nil {
		t.Fatal("running command removed", err)
	}
}
