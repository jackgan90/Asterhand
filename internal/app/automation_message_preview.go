package app

import "github.com/jackgan90/Asterhand/internal/conversation"

// Lists preserve every message identity and the domain cursor. Full bodies
// remain independently readable through messages_get, even for escaped text.
func messagePreview(message conversation.Message) conversation.Message {
	blocks := make([]conversation.Block, 0, len(message.Blocks))
	remaining := 256
	for _, block := range message.Blocks {
		if block.Type != "text" {
			blocks = append(blocks, block)
			continue
		}
		text, end, _ := textFragment(block.Text, 0, remaining)
		remaining -= len(text)
		if end < len(block.Text) {
			if block.ID != "" {
				block.Truncated = true
			} else {
				message.Truncated = true
			}
		}
		block.Text = text
		blocks = append(blocks, block)
	}
	message.Blocks = blocks
	return message
}
