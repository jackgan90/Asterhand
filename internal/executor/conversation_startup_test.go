package executor

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/conversation"
)

type startupWriteCloser struct{ io.Writer }

func (startupWriteCloser) Close() error { return nil }

func startupFixture(t *testing.T) (*ConversationRuntime, conversation.Binding) {
	t.Helper()
	manager, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := NewConversationRuntime(manager)
	t.Cleanup(func() { r.Close() })
	return r, conversation.Binding{Runtime: "openclaw", Generation: chatBindingID()}
}

func startupProcess(cleanup func()) *chatProcess {
	exited := make(chan struct{})
	close(exited)
	return &chatProcess{input: startupWriteCloser{io.Discard}, output: io.NopCloser(strings.NewReader("")), done: make(chan struct{}), exited: exited, cleanup: cleanup}
}

func TestConversationStartupSurvivesFirstCallerCancellation(t *testing.T) {
	r, binding := startupFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var starts, closes atomic.Int32
	r.start = func(ctx context.Context, _ config.Config, _ openClawOptions, _ providerBridge, _, _ string) (*chatProcess, error) {
		starts.Add(1)
		close(entered)
		select {
		case <-release:
			return startupProcess(func() { closes.Add(1) }), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := r.ensure(ctx, binding); first <- err }()
	<-entered
	_, attempt, err := r.beginStartup(context.Background(), binding)
	if err != nil || attempt == nil {
		t.Fatal("concurrent caller did not join startup", err)
	}
	cancel()
	if err := <-first; err == nil {
		t.Fatal("cancelled caller kept waiting")
	}
	select {
	case <-attempt.done:
		t.Fatal("caller cancellation killed shared startup")
	default:
	}
	close(release)
	process, err := r.ensure(context.Background(), binding)
	if err != nil || process == nil || starts.Load() != 1 || closes.Load() != 0 {
		t.Fatal("startup restarted or failed", err, starts.Load(), closes.Load())
	}
	if err = r.Close(); err != nil || closes.Load() != 1 {
		t.Fatal("owned process was not closed", err)
	}
}

func TestConversationCloseJoinsDetachedStartup(t *testing.T) {
	r, binding := startupFixture(t)
	entered := make(chan struct{})
	r.start = func(ctx context.Context, _ config.Config, _ openClawOptions, _ providerBridge, _, _ string) (*chatProcess, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	_, attempt, err := r.beginStartup(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not cancel and join startup")
	}
	select {
	case <-attempt.done:
	default:
		t.Fatal("startup escaped Close")
	}
}

func TestConversationStartupFailureIsSharedAndNextAccessCanRetry(t *testing.T) {
	r, binding := startupFixture(t)
	release := make(chan struct{})
	failure := errors.New("fixture startup failure")
	var starts atomic.Int32
	r.start = func(ctx context.Context, _ config.Config, _ openClawOptions, _ providerBridge, _, _ string) (*chatProcess, error) {
		if starts.Add(1) == 1 {
			<-release
			return nil, failure
		}
		return startupProcess(func() {}), nil
	}
	_, first, err := r.beginStartup(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := r.beginStartup(context.Background(), binding)
	if err != nil || first != second {
		t.Fatal("startup not shared", err)
	}
	close(release)
	<-first.done
	if first.err != failure {
		t.Fatal(first.err)
	}
	if _, err = r.ensure(context.Background(), binding); err != nil || starts.Load() != 2 {
		t.Fatal("explicit retry failed", err)
	}
}

func TestConversationChangedConfigurationDoesNotAdoptOldStartup(t *testing.T) {
	r, binding := startupFixture(t)
	release := make(chan struct{})
	var starts, closes atomic.Int32
	r.start = func(ctx context.Context, _ config.Config, _ openClawOptions, _ providerBridge, _, _ string) (*chatProcess, error) {
		if starts.Add(1) == 1 {
			<-release
		}
		return startupProcess(func() { closes.Add(1) }), nil
	}
	_, attempt, err := r.beginStartup(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.config.SetKey("synthetic-changed-key"); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-attempt.done
	if _, err = r.ensure(context.Background(), binding); err != nil || starts.Load() != 2 || closes.Load() != 1 {
		t.Fatal("stale configuration reused", err, starts.Load(), closes.Load())
	}
}
