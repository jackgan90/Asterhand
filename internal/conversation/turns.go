package conversation

import (
	"context"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func requestDigest(request Request) string {
	return model.Hash(struct {
		Target       Target
		Conversation string
		Text         string
		Revision     uint64
	}{request.Target, request.Conversation, request.Text, request.DraftRevision})
}

func (s *Service) reserve(request Request) (*turnRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := s.catalog.turn(request.Operation)
	if err == nil {
		if old.Digest != requestDigest(request) {
			return nil, false, Fail("conflict", "同一操作身份已绑定其他草稿或会话")
		}
		return &old, false, nil
	}
	if !isMissing(err) {
		return nil, false, err
	}
	if s.stopping || s.configuring || s.active != nil || len(s.creating) != 0 {
		return nil, false, Fail("busy", "当前节点已有聊天正在运行或核对，草稿已保留")
	}
	// Refresh selection while holding the same admission lock as configuration.
	// A settings transaction may have completed after Call's public snapshot.
	descriptor, err := s.runtime.Describe()
	if err != nil {
		return nil, false, err
	}
	if !descriptor.CanSend {
		return nil, false, Fail("not_configured", "请先在本机设置配置模型凭据")
	}
	item, err := s.catalog.conversation(request.Conversation)
	if err != nil {
		return nil, false, err
	}
	if item.Conversation.State != "ready" {
		return nil, false, Fail("not_ready", "会话仍在创建，请刷新后发送")
	}
	if item.Binding.Generation != descriptor.Binding || item.Binding.Runtime != descriptor.ID {
		return nil, false, Fail("binding_changed", "原会话的运行时绑定不可用，请恢复原配置或新建会话")
	}
	now := time.Now().UTC()
	turnID := model.ID()
	record := &turnRecord{Version: catalogVersion, Target: request.Target, Digest: requestDigest(request), DraftRevision: request.DraftRevision,
		RuntimeRequest: turnID, Binding: item.Binding, Unsettled: true,
		Turn: Turn{Prompt: descriptor.Prompt.Normalize(), ID: turnID, Operation: request.Operation, Conversation: request.Conversation,
			Delivery: "pending", Run: "pending", Persistence: "pending", Provider: descriptor.Provider, Model: descriptor.Model, StartedAt: now, UpdatedAt: now}}
	if err = s.catalog.saveTurn(*record); err != nil {
		return nil, false, err
	}
	s.active = record
	item.LatestOperation, item.Conversation.UpdatedAt = request.Operation, now
	if err = s.catalog.saveConversation(item); err != nil {
		// Dispatch has not started. Retain this definite rejection in RAM and
		// retry its metadata write before reopening admission.
		record.Turn.Delivery, record.Turn.Run, record.Turn.Persistence = "rejected", "failed", "unknown"
		record.Unsettled = false
		return record, false, err
	}
	s.dispatching = true
	s.wg.Add(1)
	return record, true, nil
}

func (s *Service) submit(ctx context.Context, request Request) (*Turn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, dispatch, err := s.reserve(request)
	if err != nil {
		if record != nil {
			value := record.Turn
			return &value, err
		}
		return nil, err
	}
	if !dispatch {
		return &record.Turn, nil
	}
	defer s.wg.Done()
	// Once reserved, the operation belongs to the node. A host disappearing does
	// not cancel or replay it. The node owns the independent bounded deadline.
	runCtx, cancel := context.WithTimeout(s.ctx, 90*time.Second)
	defer cancel()
	receipt, callErr := s.runtime.Submit(runCtx, record.Binding, Submission{ID: record.RuntimeRequest, Text: request.Text, StartedAt: record.Turn.StartedAt})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispatching = false
	if receipt.Prompt != nil {
		record.Turn.Prompt = receipt.Prompt.Normalize()
	}
	if receipt.Accepted {
		record.Turn.Delivery, record.Turn.Run = "accepted", "running"
		record.Turn.UserMessage = receipt.UserMessage
	} else {
		record.Turn.Delivery, record.Turn.Run = "unknown", "unknown"
	}
	// Only an adapter's definite pre-dispatch rejection releases admission.
	if callErr != nil && PublicError(callErr).Code == "rejected" {
		record.Turn.Delivery, record.Turn.Run, record.Turn.Persistence = "rejected", "failed", "unknown"
		record.Unsettled = false
	}
	record.Turn.UpdatedAt = time.Now().UTC()
	if err = s.catalog.saveTurn(*record); err != nil {
		return &record.Turn, err
	}
	if !record.Unsettled {
		s.active = nil
	}
	value := record.Turn
	return &value, callErr
}

func (s *Service) reconcile(ctx context.Context, operation string) (*Turn, *Message, error) {
	if !s.observe.TryLock() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.active != nil && (operation == "" || operation == s.active.Turn.Operation) {
			value := s.active.Turn
			return &value, nil, nil
		}
		if operation == "" {
			return nil, nil, nil
		}
		record, err := s.catalog.turn(operation)
		return &record.Turn, nil, err
	}
	defer s.observe.Unlock()
	s.mu.Lock()
	if operation == "" {
		if s.active == nil || s.dispatching {
			s.mu.Unlock()
			return nil, nil, nil
		}
		operation = s.active.Turn.Operation
	}
	var record turnRecord
	var err error
	if s.active != nil && s.active.Turn.Operation == operation {
		record = *s.active
		if s.dispatching {
			s.mu.Unlock()
			return &record.Turn, nil, nil
		}
	} else {
		record, err = s.catalog.turn(operation)
	}
	s.mu.Unlock()
	if err != nil {
		return &record.Turn, nil, err
	}
	if !record.Unsettled {
		s.mu.Lock()
		if s.active != nil && s.active.Turn.Operation == operation {
			if err = s.catalog.saveTurn(record); err == nil {
				s.active = nil
			}
		}
		s.mu.Unlock()
		if err != nil || record.Turn.Delivery == "rejected" || record.Turn.Delivery == "accepted" && record.Turn.Persistence == "saved" {
			return &record.Turn, nil, err
		}
	}
	queryCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	if record.Unsettled && time.Since(record.Turn.StartedAt) >= TurnTimeout {
		_ = s.runtime.Cancel(queryCtx, record.Binding, record.RuntimeRequest)
	}
	observation, err := s.runtime.Observe(queryCtx, record.Binding, record.RuntimeRequest, record.Turn.StartedAt)
	if err != nil {
		return &record.Turn, nil, err
	}
	wasCompleted := record.Turn.Run == "completed"
	applyObservation(&record, observation)
	s.mu.Lock()
	if s.active != nil && s.active.Turn.Operation == operation {
		*s.active = record
	}
	err = s.catalog.saveTurn(record)
	if err == nil && !record.Unsettled && s.active != nil && s.active.Turn.Operation == operation {
		s.active = nil
	}
	handler := s.onCompleted
	s.mu.Unlock()
	// Completion and persistence are separate facts. A catalog write failure must
	// not hide a confirmed completion or trigger a second notification on retry.
	if !wasCompleted && record.Turn.Delivery == "accepted" && record.Turn.Run == "completed" && handler != nil {
		handler()
	}
	return &record.Turn, observation.Live, err
}

func applyObservation(record *turnRecord, observation Observation) {
	if observation.Prompt != nil {
		record.Turn.Prompt = observation.Prompt.Normalize()
	}
	if observation.Accepted {
		record.Turn.Delivery = "accepted"
		if observation.UserMessage != "" {
			record.Turn.UserMessage = observation.UserMessage
		}
	}
	if observation.Run != "" && (record.Unsettled || observation.Stopped) {
		record.Turn.Run = observation.Run
	}
	if observation.AssistantMessage != "" {
		record.Turn.AssistantMessage = observation.AssistantMessage
	}
	if observation.Saved {
		record.Turn.Persistence = "saved"
	} else if observation.Stopped {
		record.Turn.Persistence = "unknown"
	}
	if observation.Stopped {
		if record.Turn.Prompt.State == "pending" {
			record.Turn.Prompt = record.Turn.Prompt.Fallback("unconfirmed")
		}
		record.Unsettled = false
		if record.Turn.Run == "pending" || record.Turn.Run == "running" || record.Turn.Run == "unknown" {
			record.Turn.Run = "interrupted"
		}
		if record.Turn.Delivery == "pending" {
			record.Turn.Delivery = "unknown"
		}
	}
	record.Turn.UpdatedAt = time.Now().UTC()
}

func (s *Service) cancelTurn(ctx context.Context, operation string) error {
	record, err := s.catalog.turn(operation)
	if err != nil || !record.Unsettled {
		return err
	}
	return s.runtime.Cancel(ctx, record.Binding, record.RuntimeRequest)
}
