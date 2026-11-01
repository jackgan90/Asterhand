package quickinput

import (
	"context"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/conversation/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

// chatController outlives UI host generations. It keeps only RAM delivery intent
// and a draft digest; message bodies belong to the node's runtime.
type chatController struct {
	mu               sync.Mutex
	client           wire.Transport
	ctx              context.Context
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	closing          bool
	inflight         int
	reads            int
	pending          map[string]*chatSubmission
	order            uint64
	selected         string
	selectionRequest string
}

type chatSubmission struct {
	identity       hostipc.PendingSubmission
	digest         string
	host           string
	sequence       uint64
	parentRevision uint64
	accepted       bool
	terminal       bool
	order          uint64
}

func (c *Controller) SetConversationClient(client wire.Transport) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	c.chat = &chatController{client: client, ctx: ctx, cancel: cancel, pending: map[string]*chatSubmission{}}
}

func (c *chatController) close() {
	c.mu.Lock()
	c.closing = true
	c.cancel()
	c.mu.Unlock()
	c.wg.Wait()
}

func (c *chatController) dispatch(surface *remoteSurface, message hostipc.Message) {
	request := *message.Chat
	priority := request.Action == "select" || request.Action == "prepare" || request.Action == "enqueue" || request.Action == "queue" || request.Action == "queue_item" || request.Action == "cancel"
	read := request.Action == "history" || request.Action == "message" || request.Action == "reconcile"
	c.mu.Lock()
	if c.closing || c.inflight >= 8 || !priority && c.inflight >= 4 || read && c.reads >= 2 {
		c.mu.Unlock()
		_ = surface.send(hostipc.Message{Kind: hostipc.ChatResult, ChatID: message.ChatID,
			ChatReply: &hostipc.ChatReply{Response: wire.Response{Error: &wire.Error{Code: "busy", Message: "会话入口正在处理请求，请稍后重试"}}}})
		return
	}
	c.inflight++
	if read {
		c.reads++
	}
	c.wg.Add(1)
	if request.Action == "select" || request.Action == "create" || request.Action == "prepare" || request.Action == "history" {
		c.selectionRequest = message.ChatID
	}
	if (request.Action == "submit" || request.Action == "enqueue") && c.pending[request.Operation] == nil {
		for operation, intent := range c.pending {
			if intent.terminal || intent.accepted {
				delete(c.pending, operation)
			}
		}
		if len(c.pending) >= 16 {
			c.inflight--
			c.wg.Done()
			c.mu.Unlock()
			_ = surface.send(hostipc.Message{Kind: hostipc.ChatResult, ChatID: message.ChatID,
				ChatReply: &hostipc.ChatReply{Response: wire.Response{Error: &wire.Error{Code: "busy", Message: "请先核对之前的发送状态"}}}})
			return
		}
		c.order++
		c.pending[request.Operation] = &chatSubmission{identity: hostipc.PendingSubmission{Operation: request.Operation, Conversation: request.Conversation},
			digest: model.Digest([]byte(request.Text)), host: surface.session, sequence: message.Sequence, parentRevision: message.Revision, order: c.order}
	}
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			c.inflight--
			if read {
				c.reads--
			}
			c.mu.Unlock()
			c.wg.Done()
		}()
		ctx, cancel := context.WithTimeout(c.ctx, 95*time.Second)
		defer cancel()
		response, err := c.client.Call(ctx, request)
		if err != nil {
			response.Error = wire.PublicError(err)
		}
		reply := c.decorate(surface, request, response, message.ChatID)
		_ = surface.send(hostipc.Message{Kind: hostipc.ChatResult, ChatID: message.ChatID, ChatReply: &reply})
	}()
}

func (c *chatController) decorate(surface *remoteSurface, request wire.Request, response wire.Response, requestID string) hostipc.ChatReply {
	c.mu.Lock()
	defer c.mu.Unlock()
	reply := hostipc.ChatReply{Response: response}
	if response.Conversation != nil && response.Error == nil && c.selectionRequest == requestID {
		c.selected = response.Conversation.ID
	}
	var receiptIntent *chatSubmission
	observe := func(turn *wire.Turn) {
		if turn == nil {
			return
		}
		if intent := c.pending[turn.Operation]; intent != nil {
			intent.accepted = intent.accepted || turn.Delivery == "accepted" || turn.Delivery == "queued"
			intent.terminal = turn.Run == "completed" || turn.Run == "cancelled" || turn.Run == "failed" || turn.Run == "interrupted"
			if receiptIntent == nil || intent.order > receiptIntent.order {
				receiptIntent = intent
			}
		}
	}
	observe(response.Turn)
	if response.QueueItem != nil {
		observe(response.QueueItem.Turn)
	}
	if response.Queue != nil {
		for _, entry := range response.Queue.Entries {
			observe(entry.Turn)
		}
		for _, entry := range response.Queue.Failures {
			observe(entry.Turn)
		}
		observe(response.Queue.Active)
	}
	if (request.Action == "submit" || request.Action == "enqueue") && response.Turn == nil && response.Error != nil && response.Error.Code != "unavailable" && response.Error.Code != "unknown" {
		delete(c.pending, request.Operation)
	}
	pending := c.latestPending()
	if receiptIntent == nil && request.Action == "hello" {
		receiptIntent = pending
	}
	if receiptIntent != nil && receiptIntent.accepted {
		if receiptIntent.host == surface.session {
			sequence := receiptIntent.sequence
			reply.ClearSequence = &sequence
		} else {
			text, revision := surface.draft.Read()
			if receiptIntent.parentRevision != 0 && revision == receiptIntent.parentRevision && model.Digest([]byte(text)) == receiptIntent.digest {
				reply.ClearSequence = &revision
			}
		}
	}
	if pending != nil {
		identity := pending.identity
		reply.Pending = &identity
	}
	reply.Selected = c.selected
	return reply
}

func (c *chatController) edited(host string, sequence, revision uint64, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for operation, pending := range c.pending {
		if !pending.accepted && !pending.terminal {
			continue
		}
		if pending.host == host && sequence > pending.sequence || revision > pending.parentRevision && model.Digest([]byte(text)) != pending.digest {
			delete(c.pending, operation)
		}
	}
}

func (c *chatController) latestPending() *chatSubmission {
	var latest *chatSubmission
	for _, pending := range c.pending {
		if latest == nil || pending.order > latest.order {
			latest = pending
		}
	}
	return latest
}
