package executor

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/testutil/runtimefixture"
)

type connectionWriter func([]byte) (int, error)

func (write connectionWriter) Write(data []byte) (int, error) { return write(data) }
func (connectionWriter) Close() error                         { return nil }

func TestConversationConnectionSnapshots(t *testing.T) {
	r, binding := startupFixture(t)
	runtimefixture.Configure(t, r.config)
	var calls atomic.Int32
	state := "connected"
	process := startupProcess(func() {})
	process.pending = map[string]chan chatResult{}
	process.input = connectionWriter(func(data []byte) (int, error) {
		var command chatCommand
		if err := json.Unmarshal(data, &command); err != nil {
			return 0, err
		}
		if command.Action != "connection" && command.Action != "connect" {
			return 0, errors.New("unexpected model or history call")
		}
		calls.Add(1)
		process.mu.Lock()
		waiter := process.pending[command.ID]
		process.mu.Unlock()
		payload, _ := json.Marshal(map[string]string{"state": state})
		waiter <- chatResult{ID: command.ID, Result: payload}
		return len(data), nil
	})
	entered, release := make(chan struct{}), make(chan struct{})
	r.start = func(ctx context.Context, _ config.Config, _ openClawOptions, _ providerBridge, _, _ string) (*chatProcess, error) {
		close(entered)
		select {
		case <-release:
			return process, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx := context.Background()
	if got := r.Connection(ctx, false); got != "unavailable" {
		t.Fatal(got)
	}
	if got := r.Connection(ctx, true); got != "connecting" {
		t.Fatal(got)
	}
	<-entered
	if got := r.Connection(ctx, false); got != "connecting" || calls.Load() != 0 {
		t.Fatal(got, calls.Load())
	}
	close(release)
	if _, err := r.ensure(ctx, binding); err != nil {
		t.Fatal(err)
	}
	for _, next := range []string{"connected", "connecting", "unavailable", "connected"} {
		state = next
		if got := r.Connection(ctx, false); got != next {
			t.Fatal(got, next)
		}
	}
	if calls.Load() != 4 {
		t.Fatal("unexpected connection requests", calls.Load())
	}
	if err := r.config.SetKey(""); err != nil {
		t.Fatal(err)
	}
	if got := r.Connection(ctx, true); got != "unavailable" || calls.Load() != 4 {
		t.Fatal("missing key used existing connection", got)
	}
	if err := r.config.SetKey("changed-fixture-key"); err != nil {
		t.Fatal(err)
	}
	if got := r.Connection(ctx, false); got != "unavailable" {
		t.Fatal("stale configuration is connected", got)
	}
	process.stop()
	if got := r.Connection(ctx, false); got != "unavailable" {
		t.Fatal("dead process is connected", got)
	}
}

func TestConversationConnectionFailureDoesNotAutomaticallyRetry(t *testing.T) {
	r, _ := startupFixture(t)
	runtimefixture.Configure(t, r.config)
	var starts atomic.Int32
	r.start = func(context.Context, config.Config, openClawOptions, providerBridge, string, string) (*chatProcess, error) {
		starts.Add(1)
		return nil, errors.New("startup failed")
	}
	ctx := context.Background()
	if got := r.Connection(ctx, true); got != "connecting" {
		t.Fatal(got)
	}
	r.gate <- struct{}{}
	attempt := r.startup
	<-r.gate
	if attempt != nil {
		<-attempt.done
	}
	for range 3 {
		if got := r.Connection(ctx, false); got != "unavailable" {
			t.Fatal(got)
		}
	}
	if starts.Load() != 1 {
		t.Fatal("snapshot retried startup", starts.Load())
	}
}
