package app

import "github.com/jackgan90/Asterhand/internal/conversation"

// Lists preserve every message identity and the domain cursor. Full bodies
// remain independently readable through messages.get, even for escaped text.
func messagePreview(message conversation.Message) conversation.Message {
	blocks := make([]conversation.Block, 0, len(message.Blocks))
	remaining := 256
	for _, block := range message.Blocks {
		if remaining == 0 {
			message.Truncated = true
			break
		}
		text, end, _ := textFragment(block.Text, 0, remaining)
		blocks = append(blocks, conversation.Block{Type: block.Type, Text: text})
		remaining -= len(text)
		if end < len(block.Text) {
			message.Truncated = true
			break
		}
	}
	message.Blocks = blocks
	return message
}
