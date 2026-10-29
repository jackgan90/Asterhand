package conversation

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func TestCompletionNotificationOnlyOnTransition(t *testing.T) {
	for _, state := range []string{"completed", "failed", "cancelled", "interrupted", "unknown"} {
		t.Run(state, func(t *testing.T) {
			runtime := &fixtureRuntime{}
			service, _ := newFixtureService(t, runtime)
			id := createFixture(t, service)
			var calls int
			service.SetCompletionHandler(func() {
				if !service.mu.TryLock() {
					t.Error("completion callback holds service mutex")
					return
				}
				service.mu.Unlock()
				calls++
			})
			request := Request{Action: "submit", Target: service.Target(), Conversation: id, Operation: model.ID(), Text: "fixture"}
			if _, err := service.Call(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			runtime.observation = Observation{Accepted: true, Stopped: state != "unknown", Run: state, Saved: state == "completed"}
			request.Action, request.Text = "reconcile", ""
			for range 3 {
				if _, err := service.Call(context.Background(), request); err != nil {
					t.Fatal(err)
				}
			}
			request.Action, request.Operation = "history", ""
			if _, err := service.Call(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			want := 0
			if state == "completed" {
				want = 1
			}
			if calls != want {
				t.Fatalf("notifications=%d, want %d", calls, want)
			}
		})
	}
}

func TestCompletionNotificationWithoutUIReads(t *testing.T) {
	runtime := &fixtureRuntime{}
	service, _ := newFixtureService(t, runtime)
	id := createFixture(t, service)
	var calls atomic.Int32
	done := make(chan struct{}, 2)
	service.SetCompletionHandler(func() { calls.Add(1); done <- struct{}{} })
	if _, err := service.Call(context.Background(), Request{Action: "submit", Target: service.Target(), Conversation: id, Operation: model.ID(), Text: "fixture"}); err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	runtime.observation = Observation{Accepted: true, Stopped: true, Run: "completed", Saved: true}
	runtime.mu.Unlock()
	service.Start()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("completion needs an active frontend")
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate completion")
	}
}

func TestCompletionNotificationSurvivesCatalogWriteFailure(t *testing.T) {
	runtime := &fixtureRuntime{}
	service, records := newFixtureService(t, runtime)
	id := createFixture(t, service)
	request := Request{Action: "submit", Target: service.Target(), Conversation: id, Operation: model.ID(), Text: "fixture"}
	if _, err := service.Call(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service.SetCompletionHandler(func() { calls++ })
	runtime.observation = Observation{Accepted: true, Stopped: true, Run: "completed", Saved: true}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		turn, _, err := service.reconcile(context.Background(), request.Operation)
		if err == nil || turn.Run != "completed" {
			t.Fatalf("lost observed completion: %+v %v", turn, err)
		}
	}
	if calls != 1 {
		t.Fatalf("completion notifications after failed persistence: %d", calls)
	}
}
