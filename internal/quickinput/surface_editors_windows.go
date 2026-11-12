//go:build windows

package quickinput

import (
	"context"
	"time"

	"github.com/jackgan90/Asterhand/internal/editors"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type surfaceEditorRequest struct {
	request uint64
	id      string
	timer   *time.Timer
}

// All methods run on the WebView thread. A request can wake its renderer
// without showing the window or moving keyboard focus.
func (s *nativeSurface) beginEditorRequest(m hostipc.Message) {
	command := m.Editor.Command
	if s.activeEditor != nil {
		_ = s.editorSend(hostipc.Message{Kind: hostipc.EditorResult, Request: m.Request, EditorReply: &editors.Reply{ID: command.ID, Error: "busy"}})
		return
	}
	entry := &surfaceEditorRequest{request: m.Request, id: command.ID}
	s.activeEditor = entry
	entry.timer = time.AfterFunc(26*time.Second, func() {
		_ = s.dispatch(context.Background(), func() {
			s.finishEditorRequest(&editors.Reply{ID: command.ID, Error: "result_unknown"})
		})
	})
	s.resume()
	if err := s.postJSON(map[string]any{"kind": "editor_request", "node_id": m.Editor.Node, "request": command}); err != nil {
		s.finishEditorRequest(&editors.Reply{ID: command.ID, Error: "editor_closed"})
	}
}

func (s *nativeSurface) finishEditorRequest(reply *editors.Reply) {
	entry := s.activeEditor
	if entry == nil || reply == nil || entry.id != reply.ID {
		return
	}
	entry.timer.Stop()
	s.activeEditor = nil
	_ = s.editorSend(hostipc.Message{Kind: hostipc.EditorResult, Request: entry.request, EditorReply: reply})
	if visible, _, _ := uiDLL.NewProc("IsWindowVisible").Call(s.hwnd.Load()); visible == 0 && len(s.waiters) == 0 {
		s.suspend(nil)
	}
}
