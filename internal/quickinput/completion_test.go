package quickinput

import (
	"testing"

	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func TestReplyCompletionUsesCurrentHostWithoutCreatingOrActivating(t *testing.T) {
	c := &Controller{}
	c.ReplyCompleted() // No host: no creation, caching, or activation.
	surface := &remoteSurface{session: "session-completion", out: make(chan hostipc.Message, 1), done: make(chan struct{})}
	c.surface = surface
	c.ReplyCompleted()
	message := <-surface.out
	if message.Kind != hostipc.ReplyCompleted || message.Validate(false) != nil {
		t.Fatal("invalid completion message", message)
	}
	surface.out <- message
	c.ReplyCompleted() // Full optional notification queue must not close the host.
	select {
	case <-surface.done:
		t.Fatal("attention killed host")
	default:
	}
	<-surface.out
	c.stopping = true
	c.ReplyCompleted()
	if len(surface.out) != 0 {
		t.Fatal("notified stopping host")
	}
	c.stopping = false
	close(surface.done)
	c.ReplyCompleted()
	if len(surface.out) != 0 {
		t.Fatal("notified closed host")
	}
}
