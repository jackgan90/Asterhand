//go:build windows

package quickinput

import (
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func (s *nativeSurface) commandRequest(message bridgeMessage) {
	if message.Command == nil || message.Command.Validate() != nil || !commandswire.ValidID(message.ID) || s.commandSend == nil || s.remoteDraft == nil {
		return
	}
	epoch, _, _ := s.remoteDraft.state()
	call := hostipc.Message{Kind: hostipc.CommandCall, Epoch: epoch, CommandID: message.ID, Command: message.Command}
	if message.Command.Action == "logs" {
		call.Anchor = logAnchorForWindow(s.hwnd.Load())
	}
	_ = s.commandSend(call)
}
