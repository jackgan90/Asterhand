package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackgan90/Asterhand/internal/quickinput"
)

type desktopTestRegistrar struct{ next int }

func (r *desktopTestRegistrar) Register(quickinput.Binding) (int, error) {
	r.next++
	return r.next, nil
}
func (*desktopTestRegistrar) Unregister(int) error { return nil }

type desktopTestSurface struct{}

func (*desktopTestSurface) Toggle()                        {}
func (*desktopTestSurface) Update(string) error            { return nil }
func (*desktopTestSurface) Snapshot(context.Context) error { return nil }
func (*desktopTestSurface) Close() error                   { return nil }

func TestDesktopAPIInheritsAuthenticationAndKeepsNodeSettings(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	n.app.Input.Attach(&desktopTestRegistrar{}, func() (quickinput.Surface, error) { return &desktopTestSurface{}, nil })
	if !n.app.Input.Status().Supported {
		t.Skip("Windows integration; unsupported stub is checked by platform builds")
	}
	await(t, "desktop ready", func() bool { return n.app.Input.Status().Registration == "registered" })
	before := n.cfg.Get()
	request := httptest.NewRequest("GET", "/api/desktop/settings", nil)
	request.Host = n.app.Address()
	response := httptest.NewRecorder()
	n.app.local(response, request)
	if response.Code != 401 {
		t.Fatal("anonymous desktop settings", response.Code)
	}
	login := httptest.NewRequest("POST", "/api/login", bytes.NewBufferString(`{"token":"`+n.app.loginToken+`"}`))
	login.Host = n.app.Address()
	login.Header.Set("Origin", "http://"+n.app.Address())
	response = httptest.NewRecorder()
	n.app.local(response, login)
	var session map[string]string
	_ = json.Unmarshal(response.Body.Bytes(), &session)
	cookie := response.Result().Cookies()[0]
	status := n.app.Input.Status()
	change := quickinput.Change{Operation: "desktop-settings-test", Revision: status.Desired.Revision, Enabled: true, Binding: status.Desired.Binding, Theme: "frost"}
	body, _ := json.Marshal(change)
	request = httptest.NewRequest("POST", "/api/desktop/settings", bytes.NewReader(body))
	request.Host = n.app.Address()
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	n.app.local(response, request)
	if response.Code != 403 {
		t.Fatal("missing CSRF accepted", response.Code)
	}
	request = httptest.NewRequest("POST", "/api/desktop/settings", bytes.NewReader(body))
	request.Host = n.app.Address()
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://"+n.app.Address())
	request.Header.Set("X-Asterhand-CSRF", session["csrf"])
	response = httptest.NewRecorder()
	n.app.local(response, request)
	if response.Code != http.StatusOK || n.app.Input.Status().Desired.Theme != "frost" {
		t.Fatal(response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(before, n.cfg.Get()) {
		t.Fatal("desktop settings mutated model or peer configuration")
	}
}
