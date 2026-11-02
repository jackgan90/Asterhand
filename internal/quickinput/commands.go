package quickinput

import (
	"context"
	"time"

	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type CommandHandler func(context.Context, commandswire.Request) commandswire.Response

// SetCommandHandler is configured before attaching the desktop surface.
func (c *Controller) SetCommandHandler(handler CommandHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commands = handler
}

func (s *remoteSurface) commandCall(message hostipc.Message) error {
	s.mu.Lock()
	valid := !s.closing && s.err == nil && message.Epoch == s.epoch
	handler := s.commands
	duplicate := false
	request := *message.Command
	if valid && request.Action == "launch" {
		duplicate = request.Sequence <= s.commandSequence
		if !duplicate {
			s.commandSequence = request.Sequence
		}
	}
	s.mu.Unlock()
	if !valid {
		return hostFailure("host_protocol_invalid")
	}
	respond := func(response commandswire.Response) {
		_ = s.send(hostipc.Message{Kind: hostipc.CommandResult, CommandID: message.CommandID, CommandReply: &response})
	}
	if duplicate {
		respond(commandswire.Response{Error: commandswire.FailKey("duplicate", "err.quickinput.cmd_duplicate")})
		return nil
	}
	if handler == nil {
		respond(commandswire.Response{Error: commandswire.FailKey("unavailable", "err.quickinput.cmd_not_ready")})
		return nil
	}
	go func() {
		budget := 3 * time.Second
		if request.Action == "logs" {
			budget = 20 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		if message.Anchor != nil && request.Action == "logs" {
			ctx = context.WithValue(ctx, logAnchorContextKey{}, *message.Anchor)
		}
		respond(handler(ctx, request))
	}()
	return nil
}
