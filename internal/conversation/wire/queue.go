package wire

// Queue status contains identities only. Body recovery is one item per response
// so even a maximally JSON-escaped 16,000-byte input fits the IPC frame budget.
type QueueItem struct {
	Operation    string `json:"operation_id"`
	Conversation string `json:"conversation_id"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	Text         string `json:"text,omitempty"`
	Turn         *Turn  `json:"turn,omitempty"`
	Error        *Error `json:"error,omitempty"`
}

type QueueState struct {
	Revision uint64      `json:"revision"`
	Entries  []QueueItem `json:"entries"`
	Paused   bool        `json:"paused"`
	Limit    int         `json:"limit"`
	Active   *Turn       `json:"active,omitempty"`
}
