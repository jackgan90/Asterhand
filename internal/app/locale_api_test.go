package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput"
)

func loginSessionForTest(t *testing.T, n *testNode) (*http.Cookie, string) {
	t.Helper()
	login := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewBufferString(`{"token":"`+n.app.loginToken+`"}`))
	login.Host = n.app.Address()
	login.Header.Set("Origin", "http://"+n.app.Address())
	resp := httptest.NewRecorder()
	n.app.local(resp, login)
	if resp.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", resp.Code, resp.Body.String())
	}
	var session map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	return resp.Result().Cookies()[0], session["csrf"]
}

func TestHealthLocaleDetectionAndDesktopLocaleAPI(t *testing.T) {
	prevPref := i18n.DefaultPreference()
	t.Cleanup(func() { i18n.SetDefaultPreference(prevPref) })
	restoreSys := i18n.SetSystemLocaleForTest([]string{"zh-CN"}, true)
	defer restoreSys()

	n := newNode(t, &fakeExecutor{})
	n.app.Input.Attach(&desktopTestRegistrar{}, func() (quickinput.Surface, error) { return &desktopTestSurface{}, nil })

	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthReq.Host = n.app.Address()
	healthResp := httptest.NewRecorder()
	n.app.local(healthResp, healthReq)
	var health map[string]any
	if healthResp.Code != 200 || json.Unmarshal(healthResp.Body.Bytes(), &health) != nil {
		t.Fatal(healthResp.Code, healthResp.Body.String())
	}
	if health["locale"] != "zh-CN" || health["locale_mode"] != "auto" || health["system_locale_detected"] != true {
		t.Fatalf("unexpected auto health locale: %+v", health)
	}

	// Pre-login 401 respects X-Asterhand-Locale header and emits stable message_key.
	for _, locale := range []string{"en-US", "zh-CN"} {
		req := httptest.NewRequest(http.MethodGet, "/api/info", nil)
		req.Host = n.app.Address()
		req.Header.Set("X-Asterhand-Locale", locale)
		out := httptest.NewRecorder()
		n.app.local(out, req)
		var payload map[string]any
		if out.Code != 401 || json.Unmarshal(out.Body.Bytes(), &payload) != nil {
			t.Fatal(out.Code, out.Body.String())
		}
		if payload["message_key"] != "err.http.login_link_required" || payload["error"] != i18n.T(locale, "err.http.login_link_required") {
			t.Fatalf("locale %s 401 mismatch: %+v", locale, payload)
		}
	}

	cookie, csrf := loginSessionForTest(t, n)
	rev0 := n.app.Input.Status().Desired.Revision
	body, _ := json.Marshal(quickinput.LocaleChange{Operation: "web-set-locale-en", Revision: rev0, Locale: "en-US"})
	post := httptest.NewRequest(http.MethodPost, "/api/desktop/locale", bytes.NewReader(body))
	post.Host = n.app.Address()
	post.AddCookie(cookie)
	post.Header.Set("Origin", "http://"+n.app.Address())
	post.Header.Set("X-Asterhand-CSRF", csrf)
	postResp := httptest.NewRecorder()
	n.app.local(postResp, post)
	if postResp.Code != 200 {
		t.Fatalf("POST /api/desktop/locale failed: %d %s", postResp.Code, postResp.Body.String())
	}

	// Stale revision on POST /api/desktop/locale returns 409 with authoritative desktop snapshot.
	staleBody, _ := json.Marshal(quickinput.LocaleChange{Operation: "web-set-locale-stale", Revision: rev0, Locale: "zh-CN"})
	staleReq := httptest.NewRequest(http.MethodPost, "/api/desktop/locale", bytes.NewReader(staleBody))
	staleReq.Host = n.app.Address()
	staleReq.AddCookie(cookie)
	staleReq.Header.Set("Origin", "http://"+n.app.Address())
	staleReq.Header.Set("X-Asterhand-CSRF", csrf)
	staleResp := httptest.NewRecorder()
	n.app.local(staleResp, staleReq)
	var conflict map[string]any
	if staleResp.Code != 409 || json.Unmarshal(staleResp.Body.Bytes(), &conflict) != nil {
		t.Fatalf("expected 409 conflict, got %d %s", staleResp.Code, staleResp.Body.String())
	}
	if conflict["message_key"] != "err.quickinput.revision_conflict" || conflict["desktop"] == nil {
		t.Fatalf("missing conflict key or authoritative desktop snapshot: %+v", conflict)
	}
}

func TestDefaultAndLegacyNodeAndWorkspaceNamesOnSave(t *testing.T) {
	// Fresh config created via config.Open has NameDefault: true.
	freshHome := t.TempDir()
	freshStore, err := config.Open(freshHome)
	if err != nil {
		t.Fatal(err)
	}
	if !freshStore.Get().NameDefault {
		t.Fatalf("expected fresh config NameDefault=true, got %+v", freshStore.Get())
	}

	n := newNode(t, &fakeExecutor{})
	c := n.cfg.Get()
	c.Name = "我的节点"
	c.NameDefault = true
	if err := n.cfg.Save(c); err != nil {
		t.Fatal(err)
	}

	// Saving from English UI with translated display name and name_edited=false preserves raw Name and NameDefault.
	resp := saveNodeSettings(t, n, func(v map[string]any) {
		v["name"] = "My Node"
		v["name_edited"] = false
		v["root"] = filepath.Join(t.TempDir(), "ws-updated")
	})
	if resp.Code != 200 || n.cfg.Get().Name != "我的节点" || !n.cfg.Get().NameDefault {
		t.Fatalf("unedited translated name mutated default node name: %d %+v", resp.Code, n.cfg.Get())
	}

	// Explicit rename clears NameDefault.
	resp = saveNodeSettings(t, n, func(v map[string]any) {
		v["name"] = "Studio Workstation"
		v["name_edited"] = true
	})
	if resp.Code != 200 || n.cfg.Get().Name != "Studio Workstation" || n.cfg.Get().NameDefault {
		t.Fatalf("explicit rename did not clear NameDefault: %d %+v", resp.Code, n.cfg.Get())
	}

	// Legacy config.json without name_default or name_custom preserves custom project name and literal node name.
	legacyHome := t.TempDir()
	legacyRoot := filepath.Join(t.TempDir(), "legacy-ws")
	rawLegacy, _ := json.Marshal(map[string]any{
		"node_id":        "legacy-node-1",
		"name":           "我的节点",
		"listen":         "127.0.0.1:0",
		"peer_listen":    "127.0.0.1:0",
		"projects":       []map[string]any{{"id": "workspace", "name": "核心仓库", "root": legacyRoot}},
		"llm":            c.LLM,
		"agent_runtime":  c.AgentRuntime,
		"configured":     true,
		"model_verified": true,
	})
	if err := os.WriteFile(filepath.Join(legacyHome, "config.json"), rawLegacy, 0600); err != nil {
		t.Fatal(err)
	}
	legacyCfg, err := config.Open(legacyHome)
	if err != nil {
		t.Fatal(err)
	}
	loaded := legacyCfg.Get()
	if loaded.NameDefault {
		t.Fatal("legacy config without name_default must not set NameDefault=true")
	}
	if len(loaded.Projects) != 1 || loaded.Projects[0].Name != "核心仓库" || !loaded.Projects[0].NameCustom {
		t.Fatalf("legacy custom workspace name was not preserved as NameCustom: %+v", loaded.Projects)
	}
	nextRoot := filepath.Join(t.TempDir(), "legacy-ws-2")
	afterSave := applySettingsNames(loaded, loaded, loaded.Name, nil, nextRoot)
	if len(afterSave.Projects) != 1 || afterSave.Projects[0].Name != "核心仓库" || !afterSave.Projects[0].NameCustom || afterSave.Projects[0].Root != nextRoot {
		t.Fatalf("POST /api/settings project rebuild lost migrated custom workspace name: %+v", afterSave.Projects)
	}
}

func TestJobAndOutboxDynamicErrorLocalizationAndRejectionDigestInvariance(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	keyedJob := model.Job{
		ID:       "job-keyed",
		Owner:    n.cfg.Get().NodeID,
		Origin:   n.cfg.Get().NodeID,
		Request:  model.Request{RequestID: "req-1", Goal: "check"},
		State:    "failed",
		Error:    i18n.T("zh-CN", "err.tasks.expired_before_start"),
		ErrorKey: "err.tasks.expired_before_start",
	}
	legacyJob := model.Job{
		ID:      "job-legacy",
		Owner:   n.cfg.Get().NodeID,
		Origin:  n.cfg.Get().NodeID,
		Request: model.Request{RequestID: "req-2", Goal: "legacy"},
		State:   "failed",
		Error:   "legacy raw error without key",
	}
	outboxItem := model.Outbox{
		ID:       "out-keyed",
		PeerID:   "peer-b",
		Request:  model.Request{RequestID: "req-3", Goal: "remote"},
		State:    "pending",
		Error:    i18n.T("zh-CN", "err.tasks.expired_before_start"),
		ErrorKey: "err.tasks.expired_before_start",
	}
	if err := n.app.Store.Put("job", keyedJob.ID, keyedJob); err != nil {
		t.Fatal(err)
	}
	if err := n.app.Store.Put("job", legacyJob.ID, legacyJob); err != nil {
		t.Fatal(err)
	}
	if err := n.app.Store.Put("outbox", outboxItem.ID, outboxItem); err != nil {
		t.Fatal(err)
	}

	fetchJobs := func(locale string) map[string]string {
		req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
		req.Header.Set("X-Asterhand-Locale", locale)
		rec := httptest.NewRecorder()
		n.app.api(rec, req)
		var list []model.Job
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
			t.Fatal(rec.Code, rec.Body.String())
		}
		res := map[string]string{}
		for _, item := range list {
			res[item.ID] = item.Error
		}
		return res
	}
	enJobs := fetchJobs("en-US")
	zhJobs := fetchJobs("zh-CN")
	wantEN := i18n.T("en-US", "err.tasks.expired_before_start")
	wantZH := i18n.T("zh-CN", "err.tasks.expired_before_start")
	if enJobs["job-keyed"] != wantEN || zhJobs["job-keyed"] != wantZH || wantEN == wantZH {
		t.Fatalf("job error_key did not dynamically localize: en=%q zh=%q", enJobs["job-keyed"], zhJobs["job-keyed"])
	}
	if enJobs["job-legacy"] != "legacy raw error without key" || zhJobs["job-legacy"] != "legacy raw error without key" {
		t.Fatalf("legacy unkeyed job error was altered: en=%q zh=%q", enJobs["job-legacy"], zhJobs["job-legacy"])
	}

	outReq := httptest.NewRequest(http.MethodGet, "/api/outbox", nil)
	outReq.Header.Set("X-Asterhand-Locale", "en-US")
	outRec := httptest.NewRecorder()
	n.app.api(outRec, outReq)
	var outList []model.Outbox
	if outRec.Code != 200 || json.Unmarshal(outRec.Body.Bytes(), &outList) != nil || len(outList) != 1 || outList[0].Error != wantEN {
		t.Fatalf("outbox error_key did not dynamically localize: %d %+v", outRec.Code, outList)
	}

	// TaskRejection RequestDigest is strictly invariant across locales.
	sampleReq := model.Request{RequestID: "req-digest-check", ProjectID: "workspace", Goal: "verify digest"}
	prevPref := i18n.DefaultPreference()
	defer i18n.SetDefaultPreference(prevPref)
	i18n.SetDefaultPreference("zh-CN")
	digestZH := model.Hash(sampleReq)
	i18n.SetDefaultPreference("en-US")
	digestEN := model.Hash(sampleReq)
	if digestZH != digestEN {
		t.Fatalf("RequestDigest changed across locales: %s vs %s", digestZH, digestEN)
	}
}
