package hostipc

import "github.com/jackgan90/Asterhand/internal/conversation/wire"

type PendingSubmission struct {
	Operation    string `json:"operation_id"`
	Conversation string `json:"conversation_id"`
}

type ChatReply struct {
	Response wire.Response      `json:"response"`
	Pending  *PendingSubmission `json:"pending,omitempty"`
	Selected string             `json:"selected,omitempty"`
	// The editor clears only this exact local sequence; subsequent input wins.
	ClearSequence *uint64 `json:"clear_sequence,omitempty"`
}
