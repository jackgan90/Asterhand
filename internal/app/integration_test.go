package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/executor"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/peer"
)

type fakeExecutor struct {
	calls    atomic.Int32
	started  chan struct{}
	gate     chan struct{}
	once     sync.Once
	filename string
	ask      bool
}

func (f *fakeExecutor) Execute(ctx context.Context, r executor.Run) (executor.Result, error) {
	f.calls.Add(1)
	if f.started != nil {
		f.once.Do(func() { close(f.started) })
	}
	tool := "asterhand_create_text"
	args := map[string]string{"path": f.filename, "content": "hello world"}
	if f.ask && r.Mode == "execute" {
		question, _ := json.Marshal(map[string]any{"operation_id": "fake-question", "tool": "asterhand_ask_user", "arguments": map[string]string{"question": "文件内容？"}})
		req, _ := http.NewRequestWithContext(ctx, "POST", r.Callback, bytes.NewReader(question))
		req.Header.Set("Authorization", "Bearer "+r.Token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return executor.Result{}, err
		}
		var answer map[string]string
		err = json.NewDecoder(res.Body).Decode(&answer)
		res.Body.Close()
		if err != nil || res.StatusCode != 200 {
			return executor.Result{}, fmt.Errorf("question failed: %v %d", err, res.StatusCode)
		}
		args["content"] = answer["answer"]
	}
	if r.Mode == "route" {
		tool = "asterhand_submit"
		args = map[string]string{"goal": "create hello world"}
	}
	if f.gate != nil {
		select {
		case <-ctx.Done():
			return executor.Result{}, ctx.Err()
		case <-f.gate:
		}
	}
	b, _ := json.Marshal(map[string]any{"operation_id": "fake-operation", "tool": tool, "arguments": args})
	req, _ := http.NewRequestWithContext(ctx, "POST", r.Callback, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+r.Token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return executor.Result{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var v any
		json.NewDecoder(res.Body).Decode(&v)
		return executor.Result{}, fmt.Errorf("callback %d: %v", res.StatusCode, v)
	}
	return executor.Result{OK: true, Status: "ok", Final: "fake executor completed"}, nil
}

type testNode struct {
	app    *App
	cfg    *config.Manager
	stop   context.CancelFunc
	root   string
	closed bool
}

func newNode(t *testing.T, f *fakeExecutor) *testNode {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := config.Open(filepath.Join(base, "home"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := c.Get()
	cfg.WebListen = "127.0.0.1:0"
	cfg.PeerListen = "127.0.0.1:0"
	cfg.Projects = []config.Project{{ID: "workspace", Name: "test", Root: root}}
	cfg.Configured = true
	cfg.ModelVerified = true
	if err = c.Save(cfg); err != nil {
		t.Fatal(err)
	}
	n := &testNode{cfg: c, root: root}
	n.start(t, f)
	t.Cleanup(func() { n.close() })
	return n
}
func (n *testNode) start(t *testing.T, f *fakeExecutor) {
	t.Helper()
	a, err := New(n.cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.app = a
	n.stop = cancel
	n.closed = false
	if _, err = a.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cfg := n.cfg.Get()
	cfg.Endpoint = "https://" + a.PeerAddress()
	if err = n.cfg.Save(cfg); err != nil {
		t.Fatal(err)
	}
}
func (n *testNode) close() {
	if !n.closed {
		n.closed = true
		n.stop()
		n.app.Close()
	}
}
func await(t *testing.T, description string, fn func() bool) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if fn() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("timeout: " + description)
		case <-tick.C:
		}
	}
}
func pairNodes(t *testing.T, a, b *testNode) model.Peer {
	t.Helper()
	invite, err := b.app.Peers.Invite()
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.app.Peers.Pair(context.Background(), invite)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestTwoNodesContinueAfterOriginStopsAndReplayWithoutDuplicates(t *testing.T) {
	a := newNode(t, &fakeExecutor{})
	bf := &fakeExecutor{started: make(chan struct{}), gate: make(chan struct{}), filename: "remote-hello.txt"}
	b := newNode(t, bf)
	pairNodes(t, a, b)
	r := model.Request{RequestID: model.ID(), ProjectID: "workspace", Goal: "route this file task", Mode: "route", Target: b.cfg.Get().NodeID, StartBefore: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	route, err := a.app.Engine.Accept(a.cfg.Get().NodeID, r)
	if err != nil {
		t.Fatal(err)
	}
	var out model.Outbox
	await(t, "B received", func() bool { return a.app.Store.Get("outbox", route.ID, &out) == nil && out.Job != nil })
	select {
	case <-bf.started:
	case <-time.After(5 * time.Second):
		t.Fatal("B did not start")
	}
	owner, remoteID := out.Job.Owner, out.Job.ID
	a.close()
	close(bf.gate)
	await(t, "B independently created file", func() bool {
		var j model.Job
		return b.app.Store.Get("job", remoteID, &j) == nil && j.State == "succeeded"
	})
	content, err := os.ReadFile(filepath.Join(b.root, "remote-hello.txt"))
	if err != nil || string(content) != "hello world" {
		t.Fatalf("bad B artifact: %q %v", content, err)
	}
	if _, err = os.Stat(filepath.Join(a.root, "remote-hello.txt")); !os.IsNotExist(err) {
		t.Fatal("file created on wrong node")
	}
	a.start(t, &fakeExecutor{})
	await(t, "A reconciled cached view", func() bool {
		return a.app.Store.Get("outbox", route.ID, &out) == nil && out.Job != nil && out.Job.State == "succeeded"
	})
	if out.Job.ID != remoteID || out.Job.Owner != owner {
		t.Fatal("changed task identity after reconnect")
	}
	p, err := a.app.Peers.Get(b.cfg.Get().NodeID)
	if err != nil {
		t.Fatal(err)
	}
	var replay model.Job
	if err = peer.Call(context.Background(), p, "POST", "/v1/jobs", out.Request, &replay); err != nil {
		t.Fatal(err)
	}
	if replay.ID != remoteID || bf.calls.Load() != 1 {
		t.Fatal("duplicate execution after retry")
	}
}
func TestTLSIdentityScopeRevocationAndIdempotency(t *testing.T) {
	a := newNode(t, &fakeExecutor{})
	b := newNode(t, &fakeExecutor{filename: "once.txt"})
	p := pairNodes(t, a, b)
	bad := p
	bad.Certificate = a.app.Peers.Certificate
	var info any
	if err := peer.Call(context.Background(), bad, "GET", "/v1/info", nil, &info); err == nil {
		t.Fatal("accepted substituted TLS certificate")
	}
	r := model.Request{RequestID: model.ID(), ProjectID: "not-authorized", Mode: "execute", Goal: "create", StartBefore: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if err := peer.Call(context.Background(), p, "POST", "/v1/jobs", r, nil); err == nil {
		t.Fatal("accepted unauthorized project")
	}
	r.ProjectID = "workspace"
	var first model.Job
	if err := peer.Call(context.Background(), p, "POST", "/v1/jobs", r, &first); err != nil {
		t.Fatal(err)
	}
	var second model.Job
	if err := peer.Call(context.Background(), p, "POST", "/v1/jobs", r, &second); err != nil || first.ID != second.ID {
		t.Fatal("lost-response retry not idempotent", err)
	}
	r.Goal = "different"
	if err := peer.Call(context.Background(), p, "POST", "/v1/jobs", r, nil); err == nil {
		t.Fatal("accepted conflicting idempotency content")
	}
	var inbound model.Peer
	b.app.Store.Get("peer", a.cfg.Get().NodeID, &inbound)
	inbound.Revoked = true
	b.app.Store.Put("peer", inbound.ID, inbound)
	if err := peer.Call(context.Background(), p, "GET", "/v1/info", nil, &info); err == nil {
		t.Fatal("revoked peer remains authorized")
	}
}
func TestWebAuthenticationAndOriginGuard(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	base := "http://" + n.app.Address()
	res, err := http.Get(base + "/api/settings")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("anonymous settings accessible")
	}
	body, _ := json.Marshal(map[string]string{"token": n.app.loginToken})
	req, _ := http.NewRequest("POST", base+"/api/login", bytes.NewReader(body))
	req.Header.Set("Origin", base)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var login map[string]string
	json.NewDecoder(res.Body).Decode(&login)
	res.Body.Close()
	if res.StatusCode != 200 || len(res.Cookies()) == 0 {
		t.Fatal("login failed")
	}
	req, _ = http.NewRequest("POST", base+"/api/invite", bytes.NewReader([]byte(`{}`)))
	req.AddCookie(res.Cookies()[0])
	req.Header.Set("Origin", "https://attacker.invalid")
	req.Header.Set("X-Asterhand-CSRF", login["csrf"])
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross-origin mutation accepted")
	}
	req, _ = http.NewRequest("GET", base+"/api/info", nil)
	req.Host = "attacker.invalid"
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("DNS rebinding Host accepted")
	}
}
