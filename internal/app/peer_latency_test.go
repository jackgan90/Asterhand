package app

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestPeerChecksAllowSlowAuthenticatedConnections(t *testing.T) {
	a, b := newNode(t, &fakeExecutor{}), newNode(t, &fakeExecutor{})
	var delay atomic.Int64
	slow := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/info" {
			timer := time.NewTimer(time.Duration(delay.Load()))
			defer timer.Stop()
			select {
			case <-r.Context().Done():
				return
			case <-timer.C:
			}
		}
		b.app.remote(w, r)
	}))
	slow.TLS = &tls.Config{Certificates: []tls.Certificate{b.app.Peers.TLS}, MinVersion: tls.VersionTLS13}
	slow.StartTLS()
	defer slow.Close()
	c := b.cfg.Get()
	c.Endpoint = slow.URL
	if err := b.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	p := pairNodes(t, a, b)
	t.Run("slow-info-still-online", func(t *testing.T) {
		delay.Store(int64(2200 * time.Millisecond))
		w := httptest.NewRecorder()
		a.app.peersAPI(w, httptest.NewRequest(http.MethodGet, "/api/peers", nil))
		var rows []map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0]["online"] != true {
			t.Fatalf("slow authenticated peer reported offline: %s; error=%v", w.Body.String(), err)
		}
	})
	t.Run("metadata-callback-budget", func(t *testing.T) {
		delay.Store(int64(6 * time.Second))
		c.Name = "slow peer renamed"
		if err := b.cfg.Save(c); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		b.app.syncPeers(ctx)
		updated, err := a.app.Peers.Get(p.ID)
		if err != nil || updated.Name != c.Name {
			t.Fatalf("slow verified callback did not update metadata: name=%q error=%v", updated.Name, err)
		}
	})
}
