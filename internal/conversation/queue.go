package conversation

import (
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

func (s *Service) queueCall(request Request, response Response) (Response, error) {
	switch request.Action {
	case "prepare":
		item, err := s.prepareQueued(request)
		response.Conversation = &item
		return response, err
	case "enqueue":
		turn, err := s.enqueue(request)
		response.Turn = turn
		return response, err
	case "queue_item":
		return s.readQueueItem(request, response)
	}
	return response, nil
}

func (s *Service) readQueueItem(request Request, response Response) (Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range append(append([]*queuedOperation{}, s.queue...), s.queueFailures...) {
		if entry.item.Operation != request.Operation || entry.item.Conversation != request.Conversation {
			continue
		}
		if entry.item.Kind != "send" {
			return response, FailKey("invalid_request", "err.conv.select_queued")
		}
		item := s.queueItemLocked(entry)
		item.Text = entry.request.Text
		response.QueueItem = &item
		return response, nil
	}
	return s.finishedQueueItemLocked(request, response)
}

func (s *Service) prepareQueued(request Request) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || s.configuring {
		return Conversation{}, FailKey("busy", "err.conv.busy_stopping_or_config")
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
		return Conversation{}, FailKey("queue_full", "err.conv.queue_full_handle_existing")
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
			return nil, FailKey("conflict", "err.conv.op_bound_other")
		}
		return &record.Turn, nil
	} else if !isMissing(err) {
		return nil, err
	}
	var old queueIntent
	if err := s.catalog.store.Get("chat-intent", request.Operation, &old); err == nil {
		if old.Digest != requestDigest(request) {
			return nil, FailKey("conflict", "err.conv.op_bound_other")
		}
		return &old.Turn, nil
	} else if !isMissing(err) {
		return nil, err
	}
	if s.stopping || s.configuring {
		return nil, FailKey("busy", "err.conv.busy_draft_kept")
	}
	descriptor, err := s.runtime.Describe()
	if err != nil {
		return nil, err
	}
	if !descriptor.CanSend {
		if descriptor.SendErrorKey != "" {
			return nil, FailKey("not_configured", descriptor.SendErrorKey, descriptor.SendErrorArgs)
		}
		if descriptor.SendError != "" {
			return nil, Fail("not_configured", descriptor.SendError)
		}
		return nil, FailKey("not_configured", "err.conv.configure_key_first")
	}
	item, err := s.catalog.conversation(request.Conversation)
	if err != nil {
		return nil, err
	}
	if item.Binding.Runtime != descriptor.ID || item.Binding.Generation != descriptor.Binding {
		return nil, FailKey("binding_changed", "err.conv.binding_changed_new")
	}
	count, bytes := 0, len(request.Text)
	for _, entry := range s.queue {
		if entry.item.Kind == "send" {
			count++
			bytes += len(entry.request.Text)
		}
	}
	needsCreation := item.Conversation.State != "ready" && !s.queuedCreationLocked(item.Conversation.ID)
	operations := len(s.queue) + 1
	if needsCreation {
		operations++
	}
	if count >= QueueMessageLimit || bytes > QueueBodyLimit || operations > queueOperationLimit {
		return nil, FailKey("queue_full", "err.conv.send_queue_full")
	}
	now := time.Now().UTC()
	turn := Turn{ID: model.ID(), Operation: request.Operation, Conversation: request.Conversation, Delivery: "queued", Run: "queued", Persistence: "pending", Provider: descriptor.Provider, Model: descriptor.Model, StartedAt: now, UpdatedAt: now}
	if err := s.catalog.store.Put("chat-intent", request.Operation, queueIntent{Digest: requestDigest(request), Turn: turn}); err != nil {
		return nil, err
	}
	if needsCreation {
		s.queue = append(s.queue, &queuedOperation{item: QueueItem{Operation: model.ID(), Conversation: request.Conversation, Kind: "create", State: "queued"}})
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
	for _, entry := range s.queueFailures {
		state.Failures = append(state.Failures, s.queueItemLocked(entry))
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
		return response, FailKey("not_found", "err.conv.op_wrong_conversation")
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
