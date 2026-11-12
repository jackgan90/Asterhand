package app

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/conversation"
)

func chatError(err error) error {
	if err == nil {
		return nil
	}
	e := conversation.PublicError(err)
	return &automation.Error{Code: e.Code, Message: e.Message, MessageKey: e.MessageKey, Args: e.MessageArgs}
}

func (a *App) chatRequest(action, role string) conversation.Request {
	if role == "" {
		role = conversation.DefaultRole
	}
	return conversation.Request{Action: action, Target: conversation.Target{Node: a.Config.Get().NodeID, Role: role}}
}

// publicChat explicitly selects visible data. Runtime binding details, prompt
// application records and other conversations' queue/active IDs stay private.
func publicChat(p automation.Principal, r conversation.Response) map[string]any {
	result := map[string]any{"target": r.Target, "capabilities": r.Capabilities, "can_send": r.CanSend, "busy": r.Busy}
	if r.Connection != "" {
		result["connection"] = r.Connection
	}
	if p.Allows("conversations", r.ActiveConversation) && r.ActiveConversation != "" {
		result["active_conversation_id"] = r.ActiveConversation
	}
	if r.Conversation != nil && p.Allows("conversations", r.Conversation.ID) {
		result["conversation"] = r.Conversation
	}
	if r.Turn != nil && p.Allows("conversations", r.Turn.Conversation) {
		result["turn"] = publicTurn(r.Turn)
	}
	if r.Live != nil {
		result["live"] = messagePreview(*r.Live)
	}
	if r.Page != nil {
		page := *r.Page
		page.Messages = make([]conversation.Message, len(r.Page.Messages))
		for i, m := range r.Page.Messages {
			page.Messages[i] = messagePreview(m)
		}
		result["page"] = page
	}
	if r.Fragment != nil {
		result["fragment"] = r.Fragment
	}
	if r.QueueItem != nil && p.Allows("conversations", r.QueueItem.Conversation) {
		result["queue_item"] = map[string]any{"conversation_id": r.QueueItem.Conversation, "turn_operation_id": r.QueueItem.Operation, "kind": r.QueueItem.Kind, "state": r.QueueItem.State, "turn": publicTurn(r.QueueItem.Turn), "error": r.QueueItem.Error}
	}
	if r.Queue != nil {
		items := []any{}
		for _, item := range append(append([]conversation.QueueItem{}, r.Queue.Entries...), r.Queue.Failures...) {
			if p.Allows("conversations", item.Conversation) {
				items = append(items, map[string]any{"conversation_id": item.Conversation, "turn_operation_id": item.Operation, "kind": item.Kind, "state": item.State, "turn": publicTurn(item.Turn), "error": item.Error})
			}
		}
		queue := map[string]any{"entries": items, "limit": r.Queue.Limit, "paused": r.Queue.Paused}
		if r.Queue.Active != nil && p.Allows("conversations", r.Queue.Active.Conversation) {
			queue["active"] = publicTurn(r.Queue.Active)
		}
		result["queue"] = queue
	}
	return result
}

func publicTurn(turn *conversation.Turn) any {
	if turn == nil {
		return nil
	}
	b, _ := json.Marshal(turn)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	delete(out, "prompt")
	return out
}

func (a *App) registerAutomationConversations(r *automation.Registry) {
	for _, action := range []string{"connection", "connect"} {
		automation.Register(r, automation.Definition{Name: "conversations." + action, Description: "Inspect or explicitly establish the existing chat connection without a model request", Role: "read", Write: action == "connect"}, nil, func(p automation.Principal, v automation.ConversationTarget) error {
			if len(p.Scopes["conversations"]) == 0 {
				return automation.Fail("forbidden", "No conversations are granted")
			}
			return nil
		},
			func(ctx context.Context, call automation.Call, v automation.ConversationTarget) (automation.Outcome, error) {
				reply, err := a.Chat.Call(ctx, a.chatRequest(action, v.Role))
				return automation.Outcome{Data: publicChat(call.Principal, reply), Summary: map[string]string{"connection": reply.Connection}, Accepted: err == nil}, chatError(err)
			})
	}
	automation.Register(r, automation.Definition{Name: "conversations.list", Description: "List permitted persistent conversations without changing the human selection", Role: "read"}, nil, nil, a.automationConversationList)
	automation.Register(r, automation.Definition{Name: "conversations.get", Description: "Read one conversation and its latest visible turn", Role: "read"}, func(v automation.ConversationID) string { return v.ID }, func(p automation.Principal, v automation.ConversationID) error {
		return p.Require("conversations", v.ID)
	},
		func(ctx context.Context, call automation.Call, v automation.ConversationID) (automation.Outcome, error) {
			request := a.chatRequest("select", v.Role)
			request.Conversation = v.ID
			reply, err := a.Chat.Call(ctx, request)
			return automation.Outcome{Data: publicChat(call.Principal, reply)}, chatError(err)
		})
	automation.Register(r, automation.Definition{Name: "conversations.create", Description: "Prepare one persistent conversation with a stable identity; no message is sent", Role: "edit", Write: true, Batch: true}, nil, func(p automation.Principal, v automation.ConversationTarget) error {
		return p.Require("conversations", "")
	}, a.automationConversationCreate)
	automation.Register(r, automation.Definition{Name: "conversations.send", Description: "Queue one message through the existing send queue; queued is RAM custody, accepted is native durable custody", Role: "execute", Write: true, Batch: true}, func(v automation.ConversationSend) string { return v.ID }, func(p automation.Principal, v automation.ConversationSend) error {
		return p.Require("conversations", v.ID)
	}, a.automationConversationSend)
	for _, action := range []string{"get", "stop"} {
		role := "read"
		if action == "stop" {
			role = "execute"
		}
		automation.Register(r, automation.Definition{Name: "turns." + action, Description: "Observe or stop the explicitly identified turn; never stop a later reply", Role: role, Write: action == "stop", Control: true, Batch: action == "stop"}, func(v automation.TurnInput) string { return v.Conversation }, func(p automation.Principal, v automation.TurnInput) error {
			return p.Require("conversations", v.Conversation)
		},
			func(ctx context.Context, call automation.Call, v automation.TurnInput) (automation.Outcome, error) {
				return a.automationTurn(ctx, call, v, action == "stop")
			})
	}
	automation.Register(r, automation.Definition{Name: "turns.wait", Description: "Observe the original turn for up to 30 seconds without resending or cancelling it", Role: "read", Wait: true}, func(v automation.TurnWait) string { return v.Conversation }, func(p automation.Principal, v automation.TurnWait) error {
		return p.Require("conversations", v.Conversation)
	}, a.automationTurnWait)
	automation.Register(r, automation.Definition{Name: "messages.list", Description: "Read a bounded page of visible user/assistant messages", Role: "read"}, func(v automation.MessagesList) string { return v.Conversation }, func(p automation.Principal, v automation.MessagesList) error {
		return p.Require("conversations", v.Conversation)
	}, a.automationMessages)
	automation.Register(r, automation.Definition{Name: "messages.get", Description: "Read a bounded raw-text fragment of a visible message", Role: "read"}, func(v automation.MessageInput) string { return v.Conversation }, func(p automation.Principal, v automation.MessageInput) error {
		return p.Require("conversations", v.Conversation)
	}, a.automationMessage)
}

func (a *App) automationConversationList(ctx context.Context, call automation.Call, v automation.ConversationList) (automation.Outcome, error) {
	if v.Role != "" && v.Role != conversation.DefaultRole {
		return automation.Outcome{}, automation.Fail("unsupported", "Role is not available")
	}
	limit, err := automation.PageLimit(v.Limit)
	if err != nil {
		return automation.Outcome{}, err
	}
	binding := []any{"conversations", call.Principal.ID, call.Principal.Scopes["conversations"]}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	var before int64
	if cursor.After != "" {
		before, err = strconv.ParseInt(cursor.After, 10, 64)
		if err != nil {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid row cursor")
		}
	}
	items, next, err := a.Chat.ListPage(before, limit, func(id string) bool { return call.Principal.Allows("conversations", id) })
	nextCursor := ""
	if next != 0 {
		nextCursor = automation.EncodeCursor(binding, "", strconv.FormatInt(next, 10))
	}
	return automation.Outcome{Data: map[string]any{"items": items, "next": nextCursor}}, chatError(err)
}

func (a *App) automationConversationCreate(ctx context.Context, call automation.Call, v automation.ConversationTarget) (automation.Outcome, error) {
	if err := call.Check(); err != nil {
		return automation.Outcome{}, err
	}
	request := a.chatRequest("prepare", v.Role)
	request.Operation = call.Identity.NativeID()
	reply, err := a.Chat.Call(ctx, request)
	summary := map[string]any{"turn_operation_id": request.Operation}
	accepted := reply.Conversation != nil && reply.Conversation.ID != ""
	if accepted {
		summary["conversation_id"], summary["state"] = reply.Conversation.ID, reply.Conversation.State
	}
	return automation.Outcome{Data: publicChat(call.Principal, reply), Summary: summary, Accepted: accepted, Unknown: accepted && err != nil}, chatError(err)
}

func (a *App) automationConversationSend(ctx context.Context, call automation.Call, v automation.ConversationSend) (automation.Outcome, error) {
	request := a.chatRequest("enqueue", v.Role)
	request.Conversation, request.Operation, request.Text = v.ID, call.Identity.NativeID(), v.Text
	if v.Mode == "quoted_command" {
		request.Text = automation.QuoteCommand(v.Text)
	} else if v.Mode != "" && v.Mode != "message" {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "Unknown send mode")
	}
	if err := call.Check(); err != nil {
		return automation.Outcome{}, err
	}
	reply, err := a.Chat.Call(ctx, request)
	summary := map[string]any{"conversation_id": v.ID, "turn_operation_id": request.Operation}
	accepted := reply.Turn != nil
	if reply.Turn != nil {
		summary["delivery"], summary["turn_id"] = reply.Turn.Delivery, reply.Turn.ID
	}
	// Acceptance here means the domain recorded the send intent. Native custody
	// remains the separate delivery field (queued vs accepted).
	return automation.Outcome{Data: publicChat(call.Principal, reply), Summary: summary, Accepted: accepted, Unknown: accepted && err != nil}, chatError(err)
}

func (a *App) automationTurn(ctx context.Context, call automation.Call, v automation.TurnInput, stop bool) (automation.Outcome, error) {
	request := a.chatRequest("queue_item", v.Role)
	request.Conversation, request.Operation = v.Conversation, v.Operation
	if !conversation.ValidID(v.Operation) {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "An exact turn_operation_id is required")
	}
	reply, err := a.Chat.Call(ctx, request)
	if err != nil {
		return automation.Outcome{}, chatError(err)
	}
	if stop {
		if reply.QueueItem == nil || reply.QueueItem.Turn == nil || reply.QueueItem.Turn.Delivery != "accepted" {
			return automation.Outcome{}, automation.Fail("not_ready", "Only a durably accepted turn can be stopped")
		}
		if err = call.Check(); err != nil {
			return automation.Outcome{}, err
		}
		request.Action = "cancel"
		reply, err = a.Chat.Call(ctx, request)
	} else if reply.QueueItem != nil && reply.QueueItem.Turn != nil && reply.QueueItem.Turn.Delivery != "queued" && reply.QueueItem.Turn.Delivery != "rejected" {
		request.Action = "reconcile"
		reply, err = a.Chat.Call(ctx, request)
	}
	return automation.Outcome{Data: publicChat(call.Principal, reply), Summary: map[string]any{"conversation_id": v.Conversation, "turn_operation_id": v.Operation, "stop_requested": stop && err == nil}, Accepted: stop && err == nil, Unknown: stop && err != nil}, chatError(err)
}

func (a *App) automationTurnWait(ctx context.Context, call automation.Call, v automation.TurnWait) (automation.Outcome, error) {
	duration, err := boundedWait(v.Seconds)
	if err != nil {
		return automation.Outcome{}, err
	}
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err = call.Check(); err != nil {
			return automation.Outcome{}, err
		}
		outcome, err := a.automationTurn(ctx, call, automation.TurnInput{Conversation: v.Conversation, Operation: v.Operation, Role: v.Role}, false)
		if err != nil {
			return outcome, err
		}
		data := outcome.Data.(map[string]any)
		turn, _ := data["turn"].(map[string]any)
		if turn != nil && (turn["run"] == "completed" || turn["run"] == "cancelled" || turn["run"] == "failed" || turn["run"] == "interrupted" || turn["delivery"] == "rejected") {
			return outcome, nil
		}
		select {
		case <-ctx.Done():
			return automation.Outcome{}, ctx.Err()
		case <-deadline.C:
			return outcome, nil
		case <-ticker.C:
		}
	}
}

func (a *App) automationMessages(ctx context.Context, call automation.Call, v automation.MessagesList) (automation.Outcome, error) {
	binding := []any{"messages", v.Conversation, v.Role, call.Principal.ID}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	request := a.chatRequest("history", v.Role)
	request.Conversation = v.Conversation
	if cursor.After != "" {
		request.Offset, err = strconv.Atoi(cursor.After)
		if err != nil {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid message offset")
		}
		request.Cursor = cursor.Revision
	}
	reply, err := a.Chat.Call(ctx, request)
	if err != nil {
		return automation.Outcome{}, chatError(err)
	}
	data := publicChat(call.Principal, reply)
	if reply.Page != nil && reply.Page.HasMore {
		data["next"] = automation.EncodeCursor(binding, reply.Page.Cursor, strconv.Itoa(reply.Page.Next))
	}
	return automation.Outcome{Data: data}, nil
}

func (a *App) automationMessage(ctx context.Context, call automation.Call, v automation.MessageInput) (automation.Outcome, error) {
	binding := []any{"message", v.Conversation, v.Message, v.Role, call.Principal.ID}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	request := a.chatRequest("message", v.Role)
	request.Conversation, request.Message = v.Conversation, v.Message
	if cursor.After != "" {
		request.Offset, err = strconv.Atoi(cursor.After)
		if err != nil {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid text offset")
		}
	}
	reply, err := a.Chat.Call(ctx, request)
	if err != nil {
		return automation.Outcome{}, chatError(err)
	}
	data := publicChat(call.Principal, reply)
	if reply.Fragment != nil && reply.Fragment.HasMore {
		data["next"] = automation.EncodeCursor(binding, "", strconv.Itoa(reply.Fragment.Next))
	}
	return automation.Outcome{Data: data}, nil
}
