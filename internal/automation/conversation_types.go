package automation

import "strings"

type ConversationTarget struct {
	Role string `json:"role_id,omitempty"`
}
type ConversationID struct {
	ID   string `json:"conversation_id"`
	Role string `json:"role_id,omitempty"`
}
type ConversationList struct {
	Role   string `json:"role_id,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type ConversationSend struct {
	ID   string `json:"conversation_id"`
	Role string `json:"role_id,omitempty"`
	Text string `json:"text"`
	Mode string `json:"mode,omitempty" description:"message (default) or quoted_command, matching the UI's Send as text action"`
}
type TurnInput struct {
	Conversation string `json:"conversation_id"`
	Operation    string `json:"turn_operation_id"`
	Role         string `json:"role_id,omitempty"`
}
type TurnWait struct {
	Conversation string `json:"conversation_id"`
	Operation    string `json:"turn_operation_id"`
	Role         string `json:"role_id,omitempty"`
	Seconds      int    `json:"timeout_seconds,omitempty"`
}
type MessagesList struct {
	Conversation string `json:"conversation_id"`
	Role         string `json:"role_id,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
}
type MessageInput struct {
	Conversation string `json:"conversation_id"`
	Role         string `json:"role_id,omitempty"`
	Message      string `json:"message_id"`
	Cursor       string `json:"cursor,omitempty"`
}

// QuoteCommand matches quickinput/assets/command-model.mjs quotedCommand.
func QuoteCommand(text string) string {
	longest, current := 0, 0
	for _, r := range text {
		if r == '`' {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + "text\n" + text + "\n" + fence
}
