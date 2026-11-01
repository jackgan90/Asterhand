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
	_ = s.commandSend(hostipc.Message{Kind: hostipc.CommandCall, Epoch: epoch, CommandID: message.ID, Command: message.Command})
}
