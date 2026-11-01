package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func TestLateSubmissionCannotExecuteAfterDefinitiveRefusal(t *testing.T) {
	a := deliveryFixture(t)
	f := &fakeExecutor{filename: "late-after-rejected.txt"}
	b := newNode(t, f)
	p, received, release, completed := delayedSubmissionPeer(t, a, b)
	if err := b.cfg.SetKey(""); err != nil {
		t.Fatal(err)
	}
	out := saveOutbox(t, a, p.ID, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan struct{})
	go func() { a.refreshOutbox(ctx, out); close(first) }()
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("first request not received")
	}
	cancel() // The call is unknown; the already received server handler survives.
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not stop waiting")
	}
	out = readOutbox(t, a, out.ID)
	if out.State != "delivery_unknown" {
		t.Fatal("first request should be unknown", out.State)
	}
	a.refreshOutbox(context.Background(), out)
	out = readOutbox(t, a, out.ID)
	if out.State != "rejected" {
		t.Fatal("retry should be rejected", out.State)
	}
	saved := saveNodeSettings(t, b, func(v map[string]any) { v["key"] = "synthetic-restored-key" })
	if saved.Code != http.StatusOK {
		t.Fatal(saved.Code, saved.Body.String())
	}
	release()
	select {
	case response := <-completed:
		var refusal model.TaskRejection
		if response.Code != 409 || json.Unmarshal(response.Body.Bytes(), &refusal) != nil || !refusal.Matches(out.Request) {
			t.Fatal("late submission did not return durable refusal", response.Code, response.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old handler did not complete")
	}
	var jobs []model.Job
	if err := b.app.Store.List("job", &jobs); err != nil || len(jobs) != 0 || f.calls.Load() != 0 {
		t.Fatal("late refused request created work", jobs, f.calls.Load(), err)
	}
	if _, err := os.Stat(filepath.Join(b.root, f.filename)); !os.IsNotExist(err) {
		t.Fatal("late refused request created a file", err)
	}
	if readOutbox(t, a, out.ID).State != "rejected" {
		t.Fatal("origin lost definitive outcome")
	}
}

func delayedSubmissionPeer(t *testing.T, a *App, b *testNode) (model.Peer, <-chan struct{}, func(), <-chan *httptest.ResponseRecorder) {
	t.Helper()
	invite, err := b.app.Peers.Invite()
	if err != nil {
		t.Fatal(err)
	}
	cfg := a.Config.Get()
	cfg.Endpoint = "https://127.0.0.1:17433"
	if err := a.Config.Save(cfg); err != nil {
		t.Fatal(err)
	}
	paired, err := a.Peers.Pair(context.Background(), invite)
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Peers.Get(paired.ID)
	if err != nil {
		t.Fatal(err)
	}
	received, release := make(chan struct{}), make(chan struct{})
	completed := make(chan *httptest.ResponseRecorder, 1)
	var requests atomic.Int32
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v1/jobs" && requests.Add(1) == 1 {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			close(received)
			<-release
			response := httptest.NewRecorder()
			b.app.remote(response, r)
			completed <- response
			return
		}
		b.app.remote(w, r)
	}))
	proxy.TLS = &tls.Config{Certificates: []tls.Certificate{b.app.Peers.TLS}, MinVersion: tls.VersionTLS13}
	proxy.StartTLS()
	stopDelay := sync.OnceFunc(func() { close(release) })
	t.Cleanup(proxy.Close)
	t.Cleanup(stopDelay)
	p.Endpoint = proxy.URL
	if err := a.Peers.Save(p); err != nil {
		t.Fatal(err)
	}
	return p, received, stopDelay, completed
}
