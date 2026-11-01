package conversation

import "time"

// A failed operation is terminal, not a user-managed queue item. Retain a
// bounded RAM snapshot for presentation and host reconstruction, outside FIFO.
func (s *Service) settleQueueFailureLocked(entry *queuedOperation) bool {
	failed := []*queuedOperation{entry}
	if entry.item.Kind == "create" {
		for _, dependent := range s.queue[1:] {
			if dependent.item.Conversation == entry.item.Conversation {
				dependent.item.State, dependent.item.Error = "failed", entry.item.Error
				failed = append(failed, dependent)
			}
		}
	}
	// Publish terminal metadata before releasing custody. A storage error leaves
	// the failed head in place for metadata-only retry, never another Submit.
	for _, item := range failed {
		if item.item.Kind != "send" || item.turn.Delivery != "queued" {
			continue
		}
		turn := item.turn
		turn.Delivery, turn.Run, turn.UpdatedAt = "rejected", "failed", time.Now().UTC()
		if err := s.catalog.store.Put("chat-intent", item.request.Operation, queueIntent{Digest: requestDigest(item.request), Turn: turn}); err != nil {
			return false
		}
		item.turn = turn
	}
	remove := make(map[*queuedOperation]bool, len(failed))
	for _, item := range failed {
		remove[item] = true
		s.queueFailures = append(s.queueFailures, item)
	}
	remaining := s.queue[:0]
	for _, item := range s.queue {
		if !remove[item] {
			remaining = append(remaining, item)
		}
	}
	clear(s.queue[len(remaining):])
	s.queue = remaining
	if excess := len(s.queueFailures) - QueueMessageLimit; excess > 0 {
		clear(s.queueFailures[:excess])
		s.queueFailures = s.queueFailures[excess:]
	}
	s.queuePaused = ""
	s.wakeQueueLocked()
	return true
}
