package app

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/operations"
)

func batchDeleteCommand(t *testing.T, a *App, name string) (commandDeleteItem, string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "script.ps1")
	if err := os.WriteFile(script, []byte("Start-Sleep -Seconds 30"), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := a.Commands.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	saved, err := a.Commands.Save(localcommands.Mutation{Revision: catalog.Revision, Command: localcommands.Command{Name: name, Graph: apiTestGraph(script)}})
	if err != nil {
		t.Fatal(err)
	}
	command, err := a.Commands.Get(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	return commandDeleteItem{ID: command.ID, Revision: command.Revision, Operation: operations.NewID(time.Now())}, script
}

func batchDeleteHTTP(t *testing.T, a *App) func(any, bool, bool, bool) *httptest.ResponseRecorder {
	t.Helper()
	login := httptest.NewRequest("POST", "/api/login", bytes.NewReader(compactResult(map[string]string{"token": a.loginToken})))
	login.Host = a.Address()
	login.Header.Set("Origin", "http://"+a.Address())
	recorder := httptest.NewRecorder()
	a.local(recorder, login)
	if recorder.Code != 200 {
		t.Fatal(recorder.Body.String())
	}
	var auth map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &auth); err != nil {
		t.Fatal(err)
	}
	cookie := recorder.Result().Cookies()[0]
	return func(body any, authenticated, csrf, origin bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/local-commands/delete-batch", bytes.NewReader(compactResult(body)))
		r.Host = a.Address()
		if origin {
			r.Header.Set("Origin", "http://"+a.Address())
		} else {
			r.Header.Set("Origin", "https://untrusted.invalid")
		}
		if authenticated {
			r.AddCookie(cookie)
		}
		if csrf {
			r.Header.Set("X-Asterhand-CSRF", auth["csrf"])
		}
		out := httptest.NewRecorder()
		a.local(out, r)
		return out
	}
}

func TestCommandBatchDeletePartialResultsReplayAndScripts(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	call := batchDeleteHTTP(t, n.app)
	first, script := batchDeleteCommand(t, n.app, "first")
	stale, _ := batchDeleteCommand(t, n.app, "stale")
	last, _ := batchDeleteCommand(t, n.app, "last")
	stale.Revision = "old-revision"
	body := map[string]any{"items": []commandDeleteItem{first, stale, last}}
	for range 2 {
		out := call(body, true, true, true)
		var result struct {
			Results []automation.Response `json:"results"`
		}
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &result) != nil || len(result.Results) != 3 {
			t.Fatal(out.Code, out.Body.String())
		}
		for _, index := range []int{0, 2} {
			if result.Results[index].Error != nil || result.Results[index].Receipt.State != "completed" {
				t.Fatal(result.Results[index])
			}
		}
		if result.Results[1].Error == nil || result.Results[1].Error.Code != "conflict" {
			t.Fatal(result.Results[1])
		}
	}
	catalog, _ := n.app.Commands.Snapshot()
	if len(catalog.Commands) != 1 || catalog.Commands[0].ID != stale.ID {
		t.Fatal(catalog)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatal("script removed", err)
	}
	// The public entry uses the identical object revision and domain deletion.
	_, token := grantTestAutomation(t, n.app, []string{"read", "edit"}, map[string][]string{"commands": {"*"}})
	bad := testAutomationCall(n.app, token, "commands_delete", operations.NewID(time.Now()), automation.CommandVersion{ID: stale.ID, Revision: stale.Revision})
	if bad.Error == nil || bad.Error.Code != "conflict" {
		t.Fatal(bad)
	}
	good := testAutomationCall(n.app, token, "commands_delete", operations.NewID(time.Now()), automation.CommandVersion{ID: stale.ID, Revision: catalog.Commands[0].Revision})
	if good.Error != nil || good.Receipt.State != "completed" {
		t.Fatal(good)
	}
}

func TestCommandBatchDeletePreflightAndAuthentication(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	call := batchDeleteHTTP(t, n.app)
	item, _ := batchDeleteCommand(t, n.app, "preserved")
	body := map[string]any{"items": []commandDeleteItem{item}}
	for _, access := range [][3]bool{{false, true, true}, {true, false, true}, {true, true, false}} {
		out := call(body, access[0], access[1], access[2])
		if out.Code != 401 && out.Code != 403 {
			t.Fatal(out.Code)
		}
	}
	other := item
	other.ID = "invalid"
	for _, items := range [][]commandDeleteItem{{}, {item, item}, {item, other}, make([]commandDeleteItem, 33)} {
		if out := call(map[string]any{"items": items}, true, true, true); out.Code != 400 {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	if out := call(map[string]any{"items": []commandDeleteItem{item}, "all": true}, true, true, true); out.Code != 400 {
		t.Fatal(out.Code)
	}
	if _, err := n.app.Commands.Get(item.ID); err != nil {
		t.Fatal("invalid batch had side effects", err)
	}
	_, token := grantTestAutomation(t, n.app, []string{"read", "edit"}, map[string][]string{"commands": {"unrelated"}})
	denied := testAutomationCall(n.app, token, "batches_submit", operations.NewID(time.Now()), automation.BatchSubmit{Items: []automation.BatchItem{{Operation: "commands_delete", Arguments: compactResult(automation.CommandVersion{ID: item.ID, Revision: item.Revision})}}})
	if denied.Error == nil || denied.Error.Code != "forbidden" {
		t.Fatal(denied)
	}
}
