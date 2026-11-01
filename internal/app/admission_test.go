package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/testutil/runtimefixture"
)

// No worker loops are started: each test controls the exact delivery boundary.
func deliveryFixture(t *testing.T) *App {
	t.Helper()
	c, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtimefixture.Configure(t, c)
	cfg := c.Get()
	cfg.Projects = []config.Project{{ID: "workspace", Root: t.TempDir()}}
	if err := c.Save(cfg); err != nil {
		t.Fatal(err)
	}
	a, err := New(c, &fakeExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func saveOutbox(t *testing.T, a *App, target string, cancel bool) model.Outbox {
	t.Helper()
	o := model.Outbox{ID: model.ID(), PeerID: target, Request: exitRequest(), State: "pending", Cancel: cancel, Updated: model.Now()}
	if err := a.Store.Put("outbox", o.ID, o); err != nil {
		t.Fatal(err)
	}
	return o
}

func readOutbox(t *testing.T, a *App, id string) model.Outbox {
	t.Helper()
	var o model.Outbox
	if err := a.Store.Get("outbox", id, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestLocalDeliveryRejectsMissingConfigurationButConfirmsCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejection", true: "cancel-first"}[cancel], func(t *testing.T) {
			a := deliveryFixture(t)
			if err := a.Config.SetKey(""); err != nil {
				t.Fatal(err)
			}
			o := saveOutbox(t, a, a.Config.Get().NodeID, cancel)
			a.refreshOutbox(context.Background(), o)
			actual := readOutbox(t, a, o.ID)
			if cancel {
				if actual.State != "delivered" || actual.Job == nil || actual.Job.State != "cancelled" || actual.Job.Attempt != "" {
					t.Fatal("cancelled delivery was rejected", actual)
				}
			} else if actual.State != "rejected" || actual.Job != nil || actual.Error == "" {
				t.Fatal("configuration failure remains unknown", actual)
			}
		})
	}
}

func TestRemoteDeliveryRejectsMissingConfigurationAndPreservesUnknown(t *testing.T) {
	a := deliveryFixture(t)
	b := newNode(t, &fakeExecutor{})
	invite, err := b.app.Peers.Invite()
	if err != nil {
		t.Fatal(err)
	}
	// Pair requires a local endpoint only as identity metadata; this origin has
	// no listener or worker loop in this deterministic delivery fixture.
	c := a.Config.Get()
	c.Endpoint = "https://127.0.0.1:17433"
	if err := a.Config.Save(c); err != nil {
		t.Fatal(err)
	}
	p, err := a.Peers.Pair(context.Background(), invite)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.cfg.SetKey(""); err != nil {
		t.Fatal(err)
	}
	o := saveOutbox(t, a, p.ID, false)
	a.refreshOutbox(context.Background(), o)
	actual := readOutbox(t, a, o.ID)
	if actual.State != "rejected" || actual.Job != nil || actual.Error == "" {
		t.Fatal("definite remote refusal not reflected", actual)
	}
	var jobs []model.Job
	if err := b.app.Store.List("job", &jobs); err != nil || len(jobs) != 0 {
		t.Fatal("refused task persisted remotely", err, jobs)
	}
	cancelled := saveOutbox(t, a, p.ID, true)
	a.refreshOutbox(context.Background(), cancelled)
	actual = readOutbox(t, a, cancelled.ID)
	if actual.State != "delivered" || actual.Job == nil || actual.Job.State != "cancelled" {
		t.Fatal("cancel-first refusal did not retain cancelled receipt", actual)
	}
	b.close()
	unknown := saveOutbox(t, a, p.ID, false)
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	a.refreshOutbox(ctx, unknown)
	actual = readOutbox(t, a, unknown.ID)
	if actual.State != "delivery_unknown" || actual.Request != unknown.Request || actual.PeerID != unknown.PeerID {
		t.Fatal("network failure became rejection or changed request", actual)
	}
}

func TestLocalOutboxAdmissionWaitsForConfigurationTransaction(t *testing.T) {
	a := deliveryFixture(t)
	a.mu.Lock()
	locked := true
	defer func() {
		if locked {
			a.mu.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() { _, err := a.acceptLocal(exitRequest()); done <- err }()
	select {
	case err := <-done:
		t.Fatal("local delivery crossed configuration lock", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := a.Config.SetKey(""); err != nil {
		t.Fatal(err)
	}
	a.mu.Unlock()
	locked = false
	select {
	case err := <-done:
		var rejection *model.TaskRejection
		if !errors.As(err, &rejection) {
			t.Fatal("admission did not see committed configuration", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("configuration transaction deadlocked local delivery")
	}
	var jobs []model.Job
	if err := a.Store.List("job", &jobs); err != nil || len(jobs) != 0 {
		t.Fatal("configuration race accepted a job", err, jobs)
	}
}
