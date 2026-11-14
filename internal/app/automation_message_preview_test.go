package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/conversation/wire"
	"github.com/jackgan90/Asterhand/internal/model"
)

func TestAutomationMessagePreviewPreservesOrderedToolsAndTextSegmentIDs(t *testing.T) {
	message := conversation.Message{ID: "message-00000001", Role: "assistant", Blocks: []conversation.Block{
		{Type: "tool", Tool: &wire.ToolActivity{ID: "first-call", Tool: "ls", State: "completed"}},
		{Type: "text", ID: "message-00000001_text_1", Text: strings.Repeat("中🙂", 200)},
		{Type: "tool", Tool: &wire.ToolActivity{ID: "second-call", Tool: "read", State: "failed"}},
		{Type: "text", ID: "message-00000001_text_3", Text: "still independently readable"},
	}}
	preview := messagePreview(message)
	if len(preview.Blocks) != 4 || preview.ID != message.ID || preview.Truncated {
		t.Fatal("message identity or ordered content changed")
	}
	bytes := 0
	for i, block := range preview.Blocks {
		original := message.Blocks[i]
		if block.Type != original.Type || block.ID != original.ID || block.Tool != original.Tool || !utf8.ValidString(block.Text) {
			t.Fatal("content lost its identity, order or valid UTF-8")
		}
		bytes += len(block.Text)
		if block.Type == "text" && !block.Truncated {
			t.Fatal("clipped text segment has no full-read marker")
		}
	}
	if bytes > 256 || message.Blocks[1].Truncated || len(message.Blocks[1].Text) <= 256 {
		t.Fatal("preview exceeded budget or mutated the domain history")
	}
}

func TestAutomationOrderedHistoryAndSegmentReadUseTheConversationGrant(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	a := n.app
	a.Chat.Close()
	const segment = "native-message-000001_text_2"
	text := strings.Repeat("visible after tool ", 100)
	runtime := &automationChatRuntime{segments: map[string]string{segment: text}, messages: []conversation.Message{
		{ID: "native-message-000001", Role: "assistant", Blocks: []conversation.Block{
			{Type: "text", ID: "native-message-000001_text_0", Text: "before"},
			{Type: "tool", Tool: &wire.ToolActivity{ID: "native-call-000001", Tool: "read", State: "completed"}},
			{Type: "text", ID: segment, Text: text},
		}},
	}}
	a.Chat = conversation.New(a.Config.Get().NodeID, a.Store, runtime)
	a.Chat.Start()
	human, err := a.Chat.Call(context.Background(), conversation.Request{Action: "create", Target: a.Chat.Target(), Operation: model.ID()})
	if err != nil {
		t.Fatal(err)
	}
	id := human.Conversation.ID
	_, token := grantTestAutomation(t, a, []string{"read"}, map[string][]string{"conversations": {id}})
	reply := testAutomationCall(a, token, "messages.list", "", automation.MessagesList{Conversation: id, Role: "default"})
	if reply.Error != nil {
		t.Fatal(reply.Error)
	}
	var listed struct {
		Page conversation.Page `json:"page"`
	}
	if err := json.Unmarshal(compactResult(reply.Data), &listed); err != nil || len(listed.Page.Messages) != 1 {
		t.Fatal("missing ordered message", err)
	}
	blocks := listed.Page.Messages[0].Blocks
	if len(blocks) != 3 || blocks[0].Text != "before" || blocks[1].Tool == nil || blocks[1].Tool.State != "completed" || blocks[2].ID != segment || !blocks[2].Truncated {
		t.Fatal("ordered tool/text preview changed")
	}
	reply = testAutomationCall(a, token, "messages.get", "", automation.MessageInput{Conversation: id, Role: "default", Message: segment})
	if reply.Error != nil || !strings.Contains(string(compactResult(reply.Data)), text) {
		t.Fatal("text segment could not be read independently", reply.Error)
	}
	denied := testAutomationCall(a, token, "messages.list", "", automation.MessagesList{Conversation: model.ID(), Role: "default"})
	if denied.Error == nil || runtime.sends.Load() != 0 {
		t.Fatal("history bypassed conversation scope or invoked the model")
	}
}
