package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/peer"
)

func clearDiagnostic(t *testing.T, n *testNode) {
	t.Helper()
	c := n.cfg.Get()
	c.ModelVerified, c.ModelDiagnostic = false, config.DiagnosticNotRun
	c.VerifiedBundle, c.VerifiedExecution = "", ""
	if err := n.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
}

func postTask(t *testing.T, n *testNode, request model.Request) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	n.app.jobsAPI(response, httptest.NewRequest(http.MethodPost, "/api/jobs", bytes.NewReader(body)))
	return response
}

func TestNewNodeStartsAndServesWithoutModelConfiguration(t *testing.T) {
	c, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.Get()
	cfg.WebListen, cfg.PeerListen = "127.0.0.1:0", "127.0.0.1:0"
	if err := c.Save(cfg); err != nil {
		t.Fatal(err)
	}
	a, err := New(c, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer a.Close()
	if _, err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	health, err := http.Get("http://" + a.Address() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatal("model-less node is not healthy", health.StatusCode)
	}
	status := a.info(nil)["capabilities"].(executor.ConfigurationCapabilities)
	if status.Chat.Available || status.ModelTasks.Available || status.Chat.Code != executor.CredentialMissing {
		t.Fatal("new node advertised missing model as configured", status)
	}
	for _, endpoint := range []string{"/api/settings", "/api/jobs", "/api/probe", "/api/info"} {
		response := httptest.NewRecorder()
		a.api(response, httptest.NewRequest(http.MethodGet, endpoint, nil))
		if response.Code != http.StatusOK {
			t.Fatal("base entry requires model", endpoint, response.Code)
		}
	}
	for _, name := range []string{"runs", "conversations"} {
		if _, err := os.Stat(filepath.Join(c.Home, name)); !os.IsNotExist(err) {
			t.Fatal("node/read-only access initialized model runtime", name, err)
		}
	}
}

func TestUnverifiedTaskCanExecuteLocallyAndRouteWithoutLocalWorkspace(t *testing.T) {
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "remote"}[remote], func(t *testing.T) {
			originExecutor := &fakeExecutor{filename: "without-probe.txt"}
			a := newNode(t, originExecutor)
			clearDiagnostic(t, a)
			b, targetExecutor := a, originExecutor
			if remote {
				targetExecutor = &fakeExecutor{filename: "without-probe.txt"}
				b = newNode(t, targetExecutor)
				clearDiagnostic(t, b)
				code, err := b.app.Peers.Invite()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := a.app.Peers.Pair(context.Background(), code); err != nil {
					t.Fatal(err)
				}
				c := a.cfg.Get()
				c.Projects = nil
				if err := a.cfg.Save(c); err != nil {
					t.Fatal(err)
				}
			}
			request := exitRequest()
			request.Target, request.TargetExplicit = b.cfg.Get().NodeID, true
			response := postTask(t, a, request)
			if response.Code != http.StatusAccepted {
				t.Fatal("unverified request rejected", response.Code, response.Body.String())
			}
			await(t, "file created without probe", func() bool {
				content, err := os.ReadFile(filepath.Join(b.root, "without-probe.txt"))
				return err == nil && string(content) == "hello world"
			})
			if originExecutor.probes.Load() != 0 || targetExecutor.probes.Load() != 0 || a.cfg.Get().ModelVerified || b.cfg.Get().ModelVerified {
				t.Fatal("real work ran or fabricated a diagnostic")
			}
		})
	}
}

func TestMissingConfigurationRejectsBeforeAcceptanceDespiteOldProbe(t *testing.T) {
	for _, missing := range []string{"key", "runtime"} {
		t.Run(missing, func(t *testing.T) {
			f := &fakeExecutor{}
			n := newNode(t, f)
			if missing == "key" {
				if err := n.cfg.SetKey(""); err != nil {
					t.Fatal(err)
				}
			} else {
				c := n.cfg.Get()
				c.RuntimeOptions = nil
				if err := n.cfg.Save(c); err != nil {
					t.Fatal(err)
				}
			}
			request := exitRequest()
			request.Target, request.Mode = n.cfg.Get().NodeID, "route"
			response := postTask(t, n, request)
			var rejection model.TaskRejection
			if response.Code != http.StatusConflict || json.Unmarshal(response.Body.Bytes(), &rejection) != nil || !rejection.Matches(request) {
				t.Fatal("missing definitive request-bound rejection", response.Code, response.Body.String())
			}
			var jobs []model.Job
			if err := n.app.Store.List("job", &jobs); err != nil || len(jobs) != 0 || f.calls.Load() != 0 {
				t.Fatal("rejected request created work", jobs, err)
			}
		})
	}
}

func TestProbeRemainsOptionalAndExpiresWithoutBlockingTasks(t *testing.T) {
	f := &fakeExecutor{}
	n := newNode(t, f)
	clearDiagnostic(t, n)
	response := httptest.NewRecorder()
	n.app.probe(response, httptest.NewRequest(http.MethodPost, "/api/probe", nil))
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	await(t, "manual diagnostic completed", func() bool { return n.cfg.Get().ModelVerified })
	if f.probes.Load() != 1 {
		t.Fatal("explicit probe did not execute exactly once")
	}
	c := n.cfg.Get()
	c.VerifiedBundle = "older-source"
	if err := n.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	if err := executor.InvalidateStaleVerification(n.cfg); err != nil {
		t.Fatal(err)
	}
	n.close()
	n.start(t, f)
	response = httptest.NewRecorder()
	n.app.probe(response, httptest.NewRequest(http.MethodGet, "/api/probe", nil))
	if !bytes.Contains(response.Body.Bytes(), []byte(`"stale"`)) || !executor.ConfiguredCapabilities(n.cfg, n.cfg.Get()).ModelTasks.Available {
		t.Fatal("expired evidence blocked configured capability", response.Body.String())
	}
}

func pairedTarget(t *testing.T, a, b *testNode) model.Peer {
	t.Helper()
	code, err := b.app.Peers.Invite()
	if err != nil {
		t.Fatal(err)
	}
	paired, err := a.app.Peers.Pair(context.Background(), code)
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.app.Peers.Get(paired.ID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRemoteReplayAndCancellationSurviveMissingConfiguration(t *testing.T) {
	a := newNode(t, &fakeExecutor{})
	f := &fakeExecutor{filename: "accepted.txt"}
	b := newNode(t, f)
	p := pairedTarget(t, a, b)
	request := exitRequest()
	var accepted model.Job
	if err := peer.Call(context.Background(), p, "POST", "/v1/jobs", request, &accepted); err != nil {
		t.Fatal(err)
	}
	await(t, "accepted request finished", func() bool {
		var current model.Job
		return b.app.Store.Get("job", accepted.ID, &current) == nil && model.Terminal(current.State)
	})
	if err := b.cfg.SetKey(""); err != nil {
		t.Fatal(err)
	}
	var replay model.Job
	if err := peer.Call(context.Background(), p, "POST", "/v1/jobs", request, &replay); err != nil || replay.ID != accepted.ID {
		t.Fatal("missing key hid previous acceptance", err, replay)
	}
	request.Goal += "changed"
	if err := peer.Call(context.Background(), p, "POST", "/v1/jobs", request, &replay); err == nil {
		t.Fatal("changed duplicate accepted")
	}
	request = exitRequest()
	if err := peer.Call(context.Background(), p, "POST", "/v1/cancel-request", map[string]string{"request_id": request.RequestID}, nil); err != nil {
		t.Fatal(err)
	}
	replay = model.Job{}
	if err := peer.Call(context.Background(), p, "POST", "/v1/jobs", request, &replay); err != nil || replay.State != "cancelled" || replay.Attempt != "" {
		t.Fatal("missing key prevented cancelled acceptance", err, replay)
	}
	if f.calls.Load() != 1 || f.probes.Load() != 0 {
		t.Fatal("replayed or cancelled request started another attempt", f.calls.Load())
	}
}
