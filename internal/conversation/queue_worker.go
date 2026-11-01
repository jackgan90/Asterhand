package conversation

import (
	"context"
	"time"
)

func (s *Service) wakeQueueLocked() {
	if !s.queueStarted && !s.stopping {
		s.queueStarted = true
		s.wg.Add(1)
		s.Start()
		go s.runQueue()
	}
	select {
	case s.queueWake <- struct{}{}:
	default:
	}
}

func (s *Service) runQueue() {
	defer s.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.queueWake:
		case <-ticker.C:
		}
		if entry := s.nextQueued(); entry != nil {
			s.executeQueued(entry)
		}
	}
}

func (s *Service) nextQueued() *queuedOperation {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || len(s.queue) == 0 {
		return nil
	}
	entry := s.queue[0]
	if entry.item.State == "unknown" {
		record, err := s.catalog.turn(entry.request.Operation)
		if err != nil {
			return nil
		}
		entry.turn = record.Turn
		if record.Turn.Delivery == "accepted" {
			s.queue[0] = nil
			s.queue = s.queue[1:]
			if s.queuePaused == "unknown" {
				s.queuePaused = ""
			}
			s.wakeQueueLocked()
			return nil
		}
		if !record.Unsettled {
			entry.item.State = "failed"
		}
	}
	if entry.item.State == "failed" {
		s.settleQueueFailureLocked(entry)
		return nil
	}
	if s.queuePaused != "" || s.active != nil || s.configuring || entry.item.State != "queued" {
		return nil
	}
	entry.item.State = "preparing"
	entry.item.Error = nil
	return entry
}

func (s *Service) executeQueued(entry *queuedOperation) {
	ctx, cancel := context.WithTimeout(s.ctx, 210*time.Second)
	defer cancel()
	item, err := s.catalog.conversation(entry.item.Conversation)
	if err == nil && entry.item.Kind == "create" {
		_, err = s.finishCreation(ctx, item)
	}
	if err == nil && entry.item.Kind == "send" {
		if preparer, ok := s.runtime.(interface {
			Prepare(context.Context, Binding) error
		}); ok {
			err = preparer.Prepare(ctx, item.Binding)
		}
	}
	s.mu.Lock()
	if s.stopping || len(s.queue) == 0 || s.queue[0] != entry {
		s.mu.Unlock()
		return
	}
	if err != nil {
		if PublicError(err).Code == "busy" {
			entry.item.State = "queued"
		} else {
			entry.item.State = "failed"
			entry.item.Error = PublicError(err)
			s.settleQueueFailureLocked(entry)
		}
		s.mu.Unlock()
		return
	}
	if entry.item.Kind == "create" {
		s.queue[0] = nil
		s.queue = s.queue[1:]
		s.wakeQueueLocked()
		s.mu.Unlock()
		return
	}
	if s.queuePaused != "" {
		entry.item.State = "queued"
		s.mu.Unlock()
		return
	}
	entry.item.State = "sending"
	s.mu.Unlock()
	request := entry.request
	request.Action = "submit"
	turn, err := s.dispatch(s.ctx, request, true)
	s.mu.Lock()
	if turn != nil {
		entry.turn = *turn
	}
	if turn != nil && turn.Delivery == "accepted" {
		s.queue[0] = nil
		s.queue = s.queue[1:]
	} else if turn != nil && turn.Delivery == "unknown" {
		entry.item.State = "unknown"
		s.queuePaused = "unknown"
	} else if PublicError(err).Code == "busy" {
		entry.item.State = "queued"
	} else {
		entry.item.State = "failed"
		entry.item.Error = PublicError(err)
		s.settleQueueFailureLocked(entry)
	}
	s.wakeQueueLocked()
	s.mu.Unlock()
}
