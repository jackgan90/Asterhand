package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func pickerSession(t *testing.T) (*App, func(context.Context, string, string, bool, string) *httptest.ResponseRecorder, string) {
	t.Helper()
	n := newNode(t, &fakeExecutor{})
	var cookie *http.Cookie
	call := func(ctx context.Context, method, body string, authenticated bool, csrf string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/local-commands/browse", bytes.NewBufferString(body)).WithContext(ctx)
		req.Host = n.app.Address()
		req.Header.Set("Origin", "http://"+n.app.Address())
		req.Header.Set("X-Asterhand-CSRF", csrf)
		if authenticated {
			req.AddCookie(cookie)
		}
		out := httptest.NewRecorder()
		n.app.local(out, req)
		return out
	}
	body, _ := json.Marshal(map[string]string{"token": n.app.loginToken})
	req := httptest.NewRequest("POST", "/api/login", bytes.NewReader(body))
	req.Host = n.app.Address()
	req.Header.Set("Origin", "http://"+n.app.Address())
	login := httptest.NewRecorder()
	n.app.local(login, req)
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	cookie = login.Result().Cookies()[0]
	var auth map[string]string
	if err := json.Unmarshal(login.Body.Bytes(), &auth); err != nil {
		t.Fatal(err)
	}
	return n.app, call, auth["csrf"]
}

func TestScriptPickerAPISelectionCancellationAndAuthentication(t *testing.T) {
	a, call, csrf := pickerSession(t)
	ctx := context.Background()
	chosen := filepath.Join(t.TempDir(), "中文 script.ps1")
	calls := 0
	a.pickScript = func(context.Context) (string, error) { calls++; return chosen, nil }
	for _, check := range []struct {
		method, body string
		auth         bool
		csrf         string
		code         int
	}{
		{"POST", "{}", false, "", 401}, {"POST", "{}", true, "", 403},
		{"GET", "", true, csrf, 404}, {"POST", `{"path":"injected"}`, true, csrf, 400},
	} {
		if got := call(ctx, check.method, check.body, check.auth, check.csrf); got.Code != check.code {
			t.Fatal(got.Code, got.Body.String())
		}
	}
	if calls != 0 {
		t.Fatal("unauthorized or malformed request opened picker")
	}
	for _, path := range []string{chosen, ""} {
		chosen = path
		out := call(ctx, "POST", "{}", true, csrf)
		var result struct {
			Path      string
			Cancelled bool
		}
		if out.Code != 200 || json.Unmarshal(out.Body.Bytes(), &result) != nil || result.Path != path || result.Cancelled != (path == "") {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	chosen = filepath.Join(t.TempDir(), "unsupported.exe")
	if got := call(ctx, "POST", "{}", true, csrf); got.Code != 400 {
		t.Fatal(got.Code)
	}
	catalog, err := a.Commands.Snapshot()
	if err != nil || len(catalog.Commands) != 0 {
		t.Fatal("choosing saved a command", err)
	}
}

func TestScriptPickerAPIRejectsOverlapAndCancelsWithRequest(t *testing.T) {
	a, call, csrf := pickerSession(t)
	entered, exited := make(chan struct{}), make(chan struct{})
	a.pickScript = func(ctx context.Context) (string, error) {
		close(entered)
		<-ctx.Done()
		close(exited)
		return "", ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { call(ctx, "POST", "{}", true, csrf); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("picker not entered")
	}
	if got := call(context.Background(), "POST", "{}", true, csrf); got.Code != 409 {
		t.Fatal(got.Code)
	}
	cancel()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not reach picker")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("picker request did not finish")
	}
	a.mu.Lock()
	busy := a.commandPicking
	a.mu.Unlock()
	if busy {
		t.Fatal("picker gate not released")
	}
}
