package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/launch"
	"github.com/jackgan90/Asterhand/internal/operations"
)

func loginTestSession(t *testing.T, a *App) (*http.Cookie, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/login", bytes.NewBufferString(`{"token":"`+a.loginToken+`"}`))
	req.Host = a.Address()
	req.Header.Set("Origin", "http://"+a.Address())
	rec := httptest.NewRecorder()
	a.local(rec, req)
	if rec.Code != 200 {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Result().Cookies()[0], body["csrf"]
}

func stageInstalledShortcutManager(t *testing.T, a *App) (desktopDir, lnkPath string) {
	t.Helper()
	base := t.TempDir()
	installRoot := filepath.Join(base, "Program Files")
	exeDir := filepath.Join(installRoot, "versions", "v1")
	desktopDir = filepath.Join(base, "Desktop")
	if err := os.MkdirAll(exeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(desktopDir, 0755); err != nil {
		t.Fatal(err)
	}
	exePath := filepath.Join(exeDir, "asterhand.exe")
	launcher := filepath.Join(installRoot, "AsterhandLauncher.exe")
	if err := os.WriteFile(exePath, []byte("exe"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("launcher"), 0755); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(launch.Installation{Schema: 1, Executable: exePath, Data: a.Config.Home})
	if err := os.WriteFile(filepath.Join(installRoot, "installation.json"), meta, 0600); err != nil {
		t.Fatal(err)
	}
	a.Shortcut = launch.NewShortcutManagerWith(launch.ShortcutOptions{
		Home:       a.Config.Home,
		Executable: func() (string, error) { return exePath, nil },
		DesktopDir: func() (string, error) { return desktopDir, nil },
	})
	return desktopDir, filepath.Join(desktopDir, "Asterhand.lnk")
}

func TestDesktopShortcutHTTPAndAutomationEquivalence(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows shortcut COM integration")
	}
	n := newNode(t, &fakeExecutor{})
	a := n.app
	_, lnkPath := stageInstalledShortcutManager(t, a)
	cookie, csrf := loginTestSession(t, a)

	// Unauthenticated GET is rejected.
	anon := httptest.NewRequest("GET", "/api/desktop/shortcut", nil)
	anon.Host = a.Address()
	anonRec := httptest.NewRecorder()
	a.local(anonRec, anon)
	if anonRec.Code != 401 {
		t.Fatalf("expected 401 for anonymous GET, got %d", anonRec.Code)
	}

	// Authenticated GET returns initial disabled status.
	req := httptest.NewRequest("GET", "/api/desktop/shortcut", nil)
	req.Host = a.Address()
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	a.local(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET /api/desktop/shortcut failed: %d %s", rec.Code, rec.Body.String())
	}
	var st launch.ShortcutStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Supported || !st.Available || st.Enabled || st.Conflict {
		t.Fatalf("unexpected initial GET status: %+v", st)
	}

	// HTTP POST without CSRF fails with 403.
	body, _ := json.Marshal(launch.ShortcutChange{Operation: "web-op-enable-1", Enabled: true})
	noCSRF := httptest.NewRequest("POST", "/api/desktop/shortcut", bytes.NewReader(body))
	noCSRF.Host = a.Address()
	noCSRF.AddCookie(cookie)
	noCSRFRec := httptest.NewRecorder()
	a.local(noCSRFRec, noCSRF)
	if noCSRFRec.Code != 403 {
		t.Fatalf("expected 403 without CSRF, got %d", noCSRFRec.Code)
	}

	// HTTP POST enables shortcut on disk and records last_operation.
	postReq := httptest.NewRequest("POST", "/api/desktop/shortcut", bytes.NewReader(body))
	postReq.Host = a.Address()
	postReq.AddCookie(cookie)
	postReq.Header.Set("Origin", "http://"+a.Address())
	postReq.Header.Set("X-Asterhand-CSRF", csrf)
	postRec := httptest.NewRecorder()
	a.local(postRec, postReq)
	if postRec.Code != 200 {
		t.Fatalf("POST /api/desktop/shortcut failed: %d %s", postRec.Code, postRec.Body.String())
	}
	if _, err := os.Stat(lnkPath); err != nil {
		t.Fatalf("expected .lnk created on disk: %v", err)
	}

	// Automation settings_get reads the same desktop_shortcut state.
	_, token := grantTestAutomation(t, a, []string{"read", "edit"}, map[string][]string{"node": {"*"}})
	getReply := testAutomationCall(a, token, "settings_get", "", automation.Empty{})
	if getReply.Error != nil {
		t.Fatalf("settings_get failed: %+v", getReply.Error)
	}
	var settingsData struct {
		Shortcut launch.ShortcutStatus `json:"desktop_shortcut"`
	}
	_ = json.Unmarshal(compactResult(getReply.Data), &settingsData)
	if !settingsData.Shortcut.Enabled || settingsData.Shortcut.LastOperation != "web-op-enable-1" {
		t.Fatalf("settings_get mismatch: %+v", settingsData.Shortcut)
	}

	// Automation settings_desktop_shortcut disables the shortcut with durable receipt.
	offID := operations.NewID(time.Now())
	offReply := testAutomationCall(a, token, "settings_desktop_shortcut", offID, automation.DesktopShortcut{Enabled: false})
	if offReply.Error != nil || offReply.Receipt == nil || offReply.Receipt.State != "completed" || !offReply.Receipt.Accepted {
		t.Fatalf("settings_desktop_shortcut disable failed: %+v", offReply)
	}
	if _, err := os.Stat(lnkPath); !os.IsNotExist(err) {
		t.Fatalf("expected .lnk removed via automation, err=%v", err)
	}
	// Duplicate operation_id replays cached completed receipt cleanly.
	replayReply := testAutomationCall(a, token, "settings_desktop_shortcut", offID, automation.DesktopShortcut{Enabled: false})
	if replayReply.Error != nil || replayReply.Receipt == nil || replayReply.Receipt.State != "completed" {
		t.Fatalf("idempotent replay failed: %+v", replayReply)
	}

	// External unowned Asterhand.lnk causes 409 in HTTP and "conflict" rejection in automation.
	if err := os.WriteFile(lnkPath, []byte("not-a-valid-lnk"), 0644); err != nil {
		t.Fatal(err)
	}
	conflictBody, _ := json.Marshal(launch.ShortcutChange{Operation: "web-op-conflict", Enabled: true})
	cReq := httptest.NewRequest("POST", "/api/desktop/shortcut", bytes.NewReader(conflictBody))
	cReq.Host = a.Address()
	cReq.AddCookie(cookie)
	cReq.Header.Set("Origin", "http://"+a.Address())
	cReq.Header.Set("X-Asterhand-CSRF", csrf)
	cReq.Header.Set("X-Asterhand-Locale", "en-US")
	cRec := httptest.NewRecorder()
	a.local(cRec, cReq)
	if cRec.Code != 409 {
		t.Fatalf("expected 409 HTTP conflict, got %d %s", cRec.Code, cRec.Body.String())
	}
	var errPayload struct {
		MessageKey string                `json:"message_key"`
		Shortcut   launch.ShortcutStatus `json:"shortcut"`
	}
	_ = json.Unmarshal(cRec.Body.Bytes(), &errPayload)
	if errPayload.MessageKey != "err.launch.shortcut_conflict" || !errPayload.Shortcut.Conflict || errPayload.Shortcut.Enabled {
		t.Fatalf("unexpected conflict payload: %+v", errPayload)
	}

	autoConflictID := operations.NewID(time.Now())
	cReply := testAutomationCall(a, token, "settings_desktop_shortcut", autoConflictID, automation.DesktopShortcut{Enabled: true})
	if cReply.Error == nil || cReply.Error.Code != "conflict" || cReply.Receipt == nil || cReply.Receipt.State != "rejected" {
		t.Fatalf("expected automation conflict rejection, got: %+v", cReply)
	}

	// Stopping node returns 503 for HTTP POST.
	if !a.RequestStop(true) {
		t.Fatal("failed to transition node to stopping")
	}
	stopBody, _ := json.Marshal(launch.ShortcutChange{Operation: "web-op-stopping", Enabled: false})
	sReq := httptest.NewRequest("POST", "/api/desktop/shortcut", bytes.NewReader(stopBody))
	sReq.Host = a.Address()
	sReq.AddCookie(cookie)
	sReq.Header.Set("Origin", "http://"+a.Address())
	sReq.Header.Set("X-Asterhand-CSRF", csrf)
	sRec := httptest.NewRecorder()
	a.local(sRec, sReq)
	if sRec.Code != 503 {
		t.Fatalf("expected 503 while stopping, got %d %s", sRec.Code, sRec.Body.String())
	}
}

func TestDesktopShortcutAutomationReconcilesInterruptedReceipt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows shortcut COM integration")
	}
	n := newNode(t, &fakeExecutor{})
	a := n.app
	stageInstalledShortcutManager(t, a)
	p, token := grantTestAutomation(t, a, []string{"read", "edit"}, map[string][]string{"node": {"*"}})
	nodeID := a.Config.Get().NodeID

	// Create owned shortcut first, simulating side effect completed before journal Finish crashed.
	opID := operations.NewID(time.Now())
	identity := operations.Identity{
		Actor:  p.ID,
		Node:   nodeID,
		ID:     opID,
		Name:   "settings_desktop_shortcut",
		Digest: operations.Hash(automation.DesktopShortcut{Enabled: true}),
		Target: nodeID,
	}
	if _, _, err := a.Store.Begin(identity, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.SaveAutomationState(operations.State{
		Identity: identity,
		Instance: a.Automation.Registry.Instance,
		Updated:  time.Now().UnixMilli(),
		Data:     compactResult(map[string]bool{"enabled": true}),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Shortcut.Set(t.Context(), launch.ShortcutChange{Operation: identity.NativeID(), Enabled: true}); err != nil {
		t.Fatal(err)
	}

	reply := testAutomationCall(a, token, "operations_get", "", struct {
		ID string `json:"operation_id"`
	}{ID: opID})
	if reply.Error != nil {
		t.Fatalf("operations_get reconcile failed: %+v", reply.Error)
	}
	var receipt operations.Receipt
	_ = json.Unmarshal(compactResult(reply.Data), &receipt)
	if receipt.State != "completed" || !receipt.Accepted {
		t.Fatalf("expected reconciled completed receipt, got: %+v", receipt)
	}
}
