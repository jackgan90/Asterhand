package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/operations"
)

type automationChatRuntime struct {
	sends atomic.Int32
	text  atomic.Value
}

func (*automationChatRuntime) Describe() (conversation.Descriptor, error) {
	return conversation.Descriptor{ID: "fixture", Binding: "fixture-v1", CanSend: true, Capabilities: conversation.Capabilities{History: true, Streaming: true, Cancellation: true, DurableReceipts: true}}, nil
}
func (*automationChatRuntime) Create(_ context.Context, b conversation.Binding) (conversation.Binding, error) {
	b.Session, b.Lifecycle = "session-"+b.Key, "one"
	return b, nil
}
func (f *automationChatRuntime) Submit(_ context.Context, _ conversation.Binding, s conversation.Submission) (conversation.Receipt, error) {
	f.text.Store(s.Text)
	f.sends.Add(1)
	return conversation.Receipt{Accepted: true, UserMessage: "native-message-000001"}, nil
}
func (*automationChatRuntime) Observe(context.Context, conversation.Binding, string, time.Time) (conversation.Observation, error) {
	return conversation.Observation{Accepted: true, Stopped: true, Saved: true, Run: "completed", UserMessage: "native-message-000001", AssistantMessage: "native-message-000002"}, nil
}
func (*automationChatRuntime) History(context.Context, conversation.Binding, int, string) (conversation.Page, error) {
	return conversation.Page{Messages: []conversation.Message{{ID: "native-message-000001", Role: "user", Blocks: []conversation.Block{{Type: "text", Text: strings.Repeat("visible", 1000)}}}}}, nil
}
func (*automationChatRuntime) Message(context.Context, conversation.Binding, string, int) (conversation.Fragment, error) {
	return conversation.Fragment{Message: "native-message-000001", Text: strings.Repeat("visible", 1000)}, nil
}
func (*automationChatRuntime) Cancel(context.Context, conversation.Binding, string) error { return nil }
func (*automationChatRuntime) Close() error                                               { return nil }

func TestAutomationConversationSharesHumanHistoryAndNeverResends(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	a := n.app
	a.Chat.Close()
	runtime := &automationChatRuntime{}
	a.Chat = conversation.New(a.Config.Get().NodeID, a.Store, runtime)
	a.Chat.Start()
	human, err := a.Chat.Call(context.Background(), conversation.Request{Action: "create", Target: a.Chat.Target(), Operation: model.ID()})
	if err != nil {
		t.Fatal(err)
	}
	id := human.Conversation.ID
	_, token := grantTestAutomation(t, a, []string{"read", "execute"}, map[string][]string{"conversations": {id}})
	op := operations.NewID(time.Now())
	args := automation.ConversationSend{ID: id, Role: "default", Text: "/literal", Mode: "quoted_command"}
	for range 2 {
		reply := testAutomationCall(a, token, "conversations.send", op, args)
		if reply.Error != nil {
			t.Fatal(reply)
		}
	}
	await(t, "native send", func() bool { return runtime.sends.Load() == 1 })
	if runtime.text.Load() != automation.QuoteCommand(args.Text) {
		t.Fatal("send escaped differently from human semantics")
	}
	reply := testAutomationCall(a, token, "messages.list", "", automation.MessagesList{Conversation: id, Role: "default"})
	if reply.Error != nil {
		t.Fatal(reply)
	}
	var listed struct {
		Page conversation.Page `json:"page"`
	}
	_ = json.Unmarshal(compactResult(reply.Data), &listed)
	if len(listed.Page.Messages) != 1 || !listed.Page.Messages[0].Truncated {
		t.Fatal("list did not expose a bounded complete index", reply)
	}
	reply = testAutomationCall(a, token, "messages.get", "", automation.MessageInput{Conversation: id, Role: "default", Message: "native-message-000001"})
	if reply.Error != nil {
		t.Fatal(reply)
	}
	if !strings.Contains(string(compactResult(reply.Data)), strings.Repeat("visible", 1000)) {
		t.Fatal("full visible message lost")
	}
	if strings.Contains(string(compactResult(reply.Data)), "\"prompt\"") {
		t.Fatal("internal prompt leaked")
	}
	if reply = testAutomationCall(a, token, "conversations.get", "", automation.ConversationID{ID: model.ID(), Role: "default"}); reply.Error == nil {
		t.Fatal("ungranted conversation exposed")
	}
	if runtime.sends.Load() != 1 {
		t.Fatal("read or replay resent a message")
	}
}
