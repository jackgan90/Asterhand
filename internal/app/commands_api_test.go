package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/model"
)

func TestLocalCommandsAPIAuthenticationCRUDAndNoExecutionRoute(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	call := func(method, path string, value any, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(value)
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Host = n.app.Address()
		req.Header.Set("Origin", "http://"+n.app.Address())
		req.Header.Set("X-Asterhand-CSRF", csrf)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		n.app.local(out, req)
		return out
	}
	if got := call("GET", "/api/local-commands", nil, nil, ""); got.Code != 401 {
		t.Fatal(got.Code)
	}
	login := call("POST", "/api/login", map[string]string{"token": n.app.loginToken}, nil, "")
	var auth map[string]string
	json.Unmarshal(login.Body.Bytes(), &auth)
	cookie := login.Result().Cookies()[0]
	read := call("GET", "/api/local-commands", nil, cookie, "")
	var catalog localcommands.Catalog
	if read.Code != 200 || json.Unmarshal(read.Body.Bytes(), &catalog) != nil {
		t.Fatal(read.Body.String())
	}
	input := localcommands.Mutation{Revision: catalog.Revision, Command: localcommands.Command{Name: "sample", Aliases: []string{"示例"}, Graph: apiTestGraph(filepath.Join(t.TempDir(), "missing.ps1"))}}
	if out := call("POST", "/api/local-commands", input, cookie, ""); out.Code != 403 {
		t.Fatal("missing CSRF", out.Code)
	}
	out := call("POST", "/api/local-commands", input, cookie, auth["csrf"])
	var saved localcommands.SaveResult
	if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &saved) != nil {
		t.Fatal(out.Code, out.Body.String())
	}
	if out = call("POST", "/api/local-commands", input, cookie, auth["csrf"]); out.Code != 409 {
		t.Fatal("stale write", out.Code)
	}
	if out = call("POST", "/api/local-commands/launch", map[string]string{"id": saved.ID}, cookie, auth["csrf"]); out.Code != 404 {
		t.Fatal("HTTP exposed launch", out.Code)
	}
	if out = call("POST", "/api/local-commands/delete", map[string]string{"id": saved.ID, "revision": saved.Catalog.Revision}, cookie, auth["csrf"]); out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	if n.app.Chat == nil || n.app.Engine == nil {
		t.Fatal("feature altered runtime wiring")
	}
}

func apiTestGraph(script string) *localcommands.ScriptGraph {
	start, step, end := model.ID(), model.ID(), model.ID()
	return &localcommands.ScriptGraph{Parallelism: 1,
		Nodes: []localcommands.GraphNode{{ID: start, Kind: "start"}, {ID: step, Kind: "script", Name: "step", Script: script, FailurePolicy: localcommands.AbortGraph}, {ID: end, Kind: "end"}},
		Edges: []localcommands.GraphEdge{{From: start, To: step}, {From: step, To: end}},
	}
}
