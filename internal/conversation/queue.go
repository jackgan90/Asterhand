package conversation

import (
	"context"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

const QueueMessageLimit = 8
const QueueBodyLimit = 128 * 1024
const queueOperationLimit = 16

type queuedOperation struct {
	request Request
	item    QueueItem
	turn    Turn
}

// Only metadata is durable. A queued body is never dispatched unless its normal
// chat-turn reservation was durably written by submit first.
type queueIntent struct {
	Digest string `json:"digest"`
	Turn   Turn   `json:"turn"`
}

func (s *Service) recoverQueueIntents() {
	var intents []queueIntent
	if err := s.catalog.store.List("chat-intent", &intents); err != nil {
		s.fault = err
		return
	}
	for _, intent := range intents {
		if intent.Turn.Delivery != "queued" {
			continue
		}
		if _, err := s.catalog.turn(intent.Turn.Operation); err == nil {
			continue
		} else if !isMissing(err) {
			s.fault = err
			return
		}
		intent.Turn.Delivery, intent.Turn.Run = "rejected", "cancelled"
		intent.Turn.UpdatedAt = time.Now().UTC()
		if err := s.catalog.store.Put("chat-intent", intent.Turn.Operation, intent); err != nil {
			s.fault = err
			return
		}
	}
}

func (s *Service) queueCall(ctx context.Context, request Request, response Response) (Response, error) {
	switch request.Action {
	case "prepare":
		item, err := s.prepareQueued(request)
		response.Conversation = &item
		return response, err
	case "enqueue":
		turn, err := s.enqueue(request)
		response.Turn = turn
		return response, err
	case "queue_pause":
		s.mu.Lock()
		s.queuePaused = "manual"
		operation := ""
		if s.active != nil {
			operation = s.active.Turn.Operation
		}
		s.mu.Unlock()
		if operation != "" {
			return response, s.cancelTurn(ctx, operation)
		}
		return response, nil
	case "queue_resume":
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.queue) > 0 && s.queue[0].item.State == "unknown" {
			return response, Fail("busy", "先核对已发消息，队列不会重复发送")
		}
		if len(s.queue) > 0 && s.queue[0].item.State == "failed" {
			if s.queue[0].item.Kind == "send" {
				if _, err := s.catalog.turn(s.queue[0].request.Operation); err == nil || !isMissing(err) {
					return response, Fail("busy", "请先取回或移除失败消息，再继续队列")
				}
			}
			s.queue[0].item.State = "queued"
		}
		s.queuePaused = ""
		s.wakeQueueLocked()
	case "queue_item", "queue_cancel":
		return s.readQueueItem(request, response)
	}
	return response, nil
}

func (s *Service) readQueueItem(request Request, response Response) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, entry := range s.queue {
		if entry.item.Operation != request.Operation || entry.item.Conversation != request.Conversation {
			continue
		}
		if entry.item.Kind != "send" && request.Action != "queue_cancel" {
			return response, Fail("invalid_request", "请选择待发消息")
		}
		item := s.queueItemLocked(entry)
		item.Text = entry.request.Text
		if request.Action == "queue_cancel" {
			if entry.item.State == "sending" || entry.item.State == "unknown" {
				return response, Fail("busy", "消息已开始派发，请先停止并核对")
			}
			if entry.item.Kind == "create" {
				for _, dependent := range s.queue {
					if dependent.item.Kind == "send" && dependent.item.Conversation == entry.item.Conversation {
						return response, Fail("busy", "请先取回或取消该会话的待发消息")
					}
				}
			} else {
				if err := s.rejectQueuedLocked(entry); err != nil {
					return response, err
				}
			}
			copy(s.queue[i:], s.queue[i+1:])
			s.queue[len(s.queue)-1] = nil
			s.queue = s.queue[:len(s.queue)-1]
			item.State = "cancelled"
			if item.Turn != nil && item.Turn.Delivery == "queued" {
				item.Turn.Delivery, item.Turn.Run = "rejected", "cancelled"
				item.Turn.UpdatedAt = time.Now().UTC()
			}
			s.wakeQueueLocked()
		}
		response.QueueItem = &item
		return response, nil
	}
	if request.Action == "queue_item" {
		return s.finishedQueueItemLocked(request, response)
	}
	return response, Fail("not_found", "该消息已离开待发队列，请刷新状态")
}

func (s *Service) prepareQueued(request Request) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || s.configuring {
		return Conversation{}, Fail("busy", "节点正在退出或修改配置")
	}
	descriptor, err := s.runtime.Describe()
	if err != nil {
		return Conversation{}, err
	}
	item := createRecord(s.Target(), request.Operation, descriptor)
	old, err := s.catalog.conversation(item.Conversation.ID)
	if err == nil {
		item = old
	} else if PublicError(err).Code != "not_found" {
		return Conversation{}, err
	}
	if item.Conversation.State == "ready" || s.queuedCreationLocked(item.Conversation.ID) {
		return item.Conversation, nil
	}
	if len(s.queue) >= queueOperationLimit {
		return Conversation{}, Fail("queue_full", "操作队列已满，请先处理已有操作")
	}
	if err = s.catalog.saveConversation(item); err != nil {
		return Conversation{}, err
	}
	s.queue = append(s.queue, &queuedOperation{request: request, item: QueueItem{Operation: request.Operation, Conversation: item.Conversation.ID, Kind: "create", State: "queued"}})
	s.wakeQueueLocked()
	return item.Conversation, nil
}

func (s *Service) enqueue(request Request) (*Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, err := s.catalog.turn(request.Operation); err == nil {
		if record.Digest != requestDigest(request) {
			return nil, Fail("conflict", "操作身份已绑定其他消息")
		}
		return &record.Turn, nil
	} else if !isMissing(err) {
		return nil, err
	}
	var old queueIntent
	if err := s.catalog.store.Get("chat-intent", request.Operation, &old); err == nil {
		if old.Digest != requestDigest(request) {
			return nil, Fail("conflict", "操作身份已绑定其他消息")
		}
		return &old.Turn, nil
	} else if !isMissing(err) {
		return nil, err
	}
	if s.stopping || s.configuring {
		return nil, Fail("busy", "节点正在退出或修改配置，草稿已保留")
	}
	descriptor, err := s.runtime.Describe()
	if err != nil {
		return nil, err
	}
	if !descriptor.CanSend {
		return nil, Fail("not_configured", descriptor.SendError)
	}
	item, err := s.catalog.conversation(request.Conversation)
	if err != nil {
		return nil, err
	}
	if item.Binding.Runtime != descriptor.ID || item.Binding.Generation != descriptor.Binding {
		return nil, Fail("binding_changed", "会话绑定已变化，请恢复配置或新建会话")
	}
	count, bytes := 0, len(request.Text)
	for _, entry := range s.queue {
		if entry.item.Kind == "send" {
			count++
			bytes += len(entry.request.Text)
		}
	}
	if count >= QueueMessageLimit || bytes > QueueBodyLimit || len(s.queue) >= queueOperationLimit {
		return nil, Fail("queue_full", "待发队列已满，当前草稿已保留")
	}
	if item.Conversation.State != "ready" && !s.queuedCreationLocked(item.Conversation.ID) {
		return nil, Fail("not_ready", "请先完成该会话创建")
	}
	now := time.Now().UTC()
	turn := Turn{ID: model.ID(), Operation: request.Operation, Conversation: request.Conversation, Delivery: "queued", Run: "queued", Persistence: "pending", Provider: descriptor.Provider, Model: descriptor.Model, StartedAt: now, UpdatedAt: now}
	if err := s.catalog.store.Put("chat-intent", request.Operation, queueIntent{Digest: requestDigest(request), Turn: turn}); err != nil {
		return nil, err
	}
	s.queue = append(s.queue, &queuedOperation{request: request, turn: turn, item: QueueItem{Operation: request.Operation, Conversation: request.Conversation, Kind: "send", State: "queued"}})
	s.wakeQueueLocked()
	return &turn, nil
}

func (s *Service) queuedCreationLocked(conversation string) bool {
	for _, entry := range s.queue {
		if entry.item.Kind == "create" && entry.item.Conversation == conversation {
			return true
		}
	}
	return false
}

func (s *Service) queueItemLocked(entry *queuedOperation) QueueItem {
	item := entry.item
	if item.Kind == "send" {
		turn := entry.turn
		item.Turn = &turn
	}
	return item
}

func (s *Service) queueSnapshotLocked() *QueueState {
	s.queueRevision++
	state := &QueueState{Revision: s.queueRevision, Entries: make([]QueueItem, 0, len(s.queue)), Paused: s.queuePaused != "", Limit: QueueMessageLimit}
	for _, entry := range s.queue {
		state.Entries = append(state.Entries, s.queueItemLocked(entry))
	}
	if s.active != nil {
		turn := s.active.Turn
		state.Active = &turn
	}
	return state
}

func (s *Service) finishedQueueItemLocked(request Request, response Response) (Response, error) {
	record, err := s.catalog.turn(request.Operation)
	turn := record.Turn
	if isMissing(err) {
		var intent queueIntent
		err = s.catalog.store.Get("chat-intent", request.Operation, &intent)
		turn = intent.Turn
	}
	if err != nil || turn.Conversation != request.Conversation {
		return response, Fail("not_found", "操作不属于此会话")
	}
	response.QueueItem = &QueueItem{Operation: request.Operation, Conversation: request.Conversation, Kind: "send", State: "settled", Turn: &turn}
	return response, nil
}

func (s *Service) rejectQueuedLocked(entry *queuedOperation) error {
	turn := entry.turn
	turn.Delivery, turn.Run, turn.UpdatedAt = "rejected", "cancelled", time.Now().UTC()
	return s.catalog.store.Put("chat-intent", entry.request.Operation, queueIntent{Digest: requestDigest(entry.request), Turn: turn})
}

func (s *Service) discardQueueLocked() {
	for _, entry := range s.queue {
		if entry.item.Kind == "send" && entry.item.State != "sending" && entry.item.State != "unknown" {
			_ = s.rejectQueuedLocked(entry)
		}
	}
}
