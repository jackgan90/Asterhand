package quickinput

import (
	"context"
	"time"

	"github.com/jackgan90/Asterhand/internal/editors"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

// The session lives as long as this host generation. Only a real editor
// request wakes the input renderer, including when its window is hidden.
type EditorHandler func(context.Context, string, func(hostipc.EditorRequest) editors.Reply)

func (c *Controller) SetEditorHandler(handler EditorHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.liveEditors = handler
}

func (s *remoteSurface) serveEditors(handler EditorHandler) {
	if handler == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	go func() {
		defer cancel()
		handler(ctx, s.session, func(request hostipc.EditorRequest) editors.Reply {
			// The node's broker owns the caller deadline. Also bound this
			// transport wait so a lost host reply cannot block later requests.
			timeout, stop := context.WithTimeout(ctx, 28*time.Second)
			defer stop()
			result, err := s.exchange(timeout, hostipc.Message{Kind: hostipc.EditorCall, Editor: &request}, hostipc.EditorResult)
			if err != nil || result.EditorReply == nil || result.EditorReply.ID != request.Command.ID {
				code := "editor_closed"
				if request.Command.Action == "commit" || request.Command.Action == "update" {
					code = "result_unknown"
				}
				return editors.Reply{ID: request.Command.ID, Error: code}
			}
			return *result.EditorReply
		})
	}()
}
