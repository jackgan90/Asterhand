package app

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

func exitRequest() model.Request {
	return model.Request{RequestID: model.ID(), ProjectID: "workspace", Goal: "write after gate", Mode: "execute", StartBefore: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
}

func TestBusyExitRequiresConfirmationAndPersistsReconciliation(t *testing.T) {
	f := &fakeExecutor{started: make(chan struct{}), gate: make(chan struct{}), filename: "exit.txt"}
	n := newNode(t, f)
	j, err := n.app.Engine.Accept(n.cfg.Get().NodeID, exitRequest())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("execution never started")
	}
	if n.app.RequestStop(false) || n.app.Stopping() {
		t.Fatal("busy tray click stopped work before confirmation")
	}
	select {
	case <-n.app.Done():
		t.Fatal("declined confirmation cancelled the app")
	default:
	}
	queued, err := n.app.Engine.Accept(n.cfg.Get().NodeID, exitRequest())
	if err != nil {
		t.Fatal("declined exit changed admission", err)
	}
	if !n.app.RequestStop(true) {
		t.Fatal("confirmed stop refused")
	}
	if _, err = n.app.Engine.Accept(n.cfg.Get().NodeID, exitRequest()); err == nil {
		t.Fatal("accepted work after stop")
	}
	webAddress, peerAddress := n.app.Address(), n.app.PeerAddress()
	n.close()
	for _, address := range []string{webAddress, peerAddress} {
		if conn, err := net.DialTimeout("tcp", address, time.Second); err == nil {
			conn.Close()
			t.Fatal("listener survived exit", address)
		}
	}
	s, err := store.Open(filepath.Join(n.cfg.Home, "asterhand.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Get("job", j.ID, &j); err != nil || j.State != "reconciliation_required" {
		t.Fatal("in-flight work not durably reconciled", j.State, err)
	}
	if err = s.Get("job", queued.ID, &queued); err != nil || queued.State != "queued" {
		t.Fatal("exit destroyed queued work", queued.State, err)
	}
}

func TestCloseWaitsForAdmittedHandlersBeforeClosingStore(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	entered, release, written := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	handler := n.app.requests.wrap(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
		written <- n.app.Store.Put("lifecycle-test", "late", map[string]string{"value": "committed"})
	})
	go handler(httptest.NewRecorder(), httptest.NewRequest("POST", "/accepted-before-exit", nil))
	<-entered
	if !n.app.RequestStop(false) {
		t.Fatal("idle exit refused")
	}
	denied := httptest.NewRecorder()
	n.app.requests.wrap(func(http.ResponseWriter, *http.Request) { t.Error("late handler admitted") })(denied, httptest.NewRequest("GET", "/health", nil))
	if denied.Code != http.StatusServiceUnavailable {
		t.Fatal("late request did not get 503", denied.Code)
	}
	closed := make(chan error, 1)
	go func() { closed <- n.app.Close() }()
	select {
	case err := <-closed:
		t.Fatal("closed store before admitted handler finished", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-written; err != nil {
		t.Fatal("late handler lost persistence", err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish")
	}
	if err := n.app.Close(); err != nil {
		t.Fatal("repeated close failed", err)
	}
}

func TestProbeCountsAsBusyAndCannotBeginAfterStop(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	n.app.mu.Lock()
	n.app.probeRunning = true
	n.app.mu.Unlock()
	if n.app.RequestStop(false) {
		t.Fatal("probe was treated as idle")
	}
	n.app.mu.Lock()
	n.app.probeRunning = false
	n.app.mu.Unlock()
	if !n.app.RequestStop(false) {
		t.Fatal("idle stop refused")
	}
	w := httptest.NewRecorder()
	n.app.probe(w, httptest.NewRequest("POST", "/api/probe", nil))
	if w.Code != 503 {
		t.Fatal("probe started during shutdown", w.Code)
	}
}
