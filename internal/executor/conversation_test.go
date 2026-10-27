package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation"
)

func TestConversationCancelDeadlineWhileStarting(t *testing.T) {
	runtime := NewConversationRuntime(nil)
	runtime.gate <- struct{}{} // Held by the cold-start owner.
	defer func() { <-runtime.gate; runtime.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runtime.Cancel(ctx, conversation.Binding{}, "operation") }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancel returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expired cancellation waited for cold startup")
	}
}

func TestConversationCloseCancelsStartupOwner(t *testing.T) {
	runtime := NewConversationRuntime(nil)
	runtime.gate <- struct{}{}
	owner := make(chan struct{})
	go func() {
		<-runtime.ctx.Done()
		<-runtime.gate
		close(owner)
	}()
	done := make(chan error, 1)
	go func() { done <- runtime.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		<-owner
	case <-time.After(time.Second):
		t.Fatal("close did not interrupt the startup owner")
	}
}
