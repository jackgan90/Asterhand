package quickinput

import "github.com/jackgan90/Asterhand/internal/quickinput/hostipc"

// ReplyCompleted is best effort and never creates or activates a surface.
func (c *Controller) ReplyCompleted() {
	c.mu.Lock()
	surface, stopping := c.surface, c.stopping
	c.mu.Unlock()
	if notifier, ok := surface.(interface{ ReplyCompleted() }); ok && !stopping {
		notifier.ReplyCompleted()
	}
}

func (s *remoteSurface) ReplyCompleted() {
	// Dropping optional attention on a full/closed queue must not fail the host.
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.out <- hostipc.Message{Version: hostipc.Version, Session: s.session, Kind: hostipc.ReplyCompleted}:
	case <-s.done:
	default:
	}
}
