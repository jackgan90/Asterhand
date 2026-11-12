package conversation

import (
	"encoding/json"

	"github.com/jackgan90/Asterhand/internal/store"
)

// ListPage bounds both row reads and returned metadata, even when an actor's
// allowed conversations are sparse. A nonempty next cursor can accompany an
// empty page; callers must continue rather than treating that as end-of-list.
func (s *Service) ListPage(before int64, limit int, allowed func(string) bool) ([]Conversation, int64, error) {
	pager, ok := s.catalog.store.(interface {
		Page(string, int64, int) ([]json.RawMessage, int64, error)
	})
	if !ok {
		return nil, 0, FailKey("unsupported", "err.conv.capability_unsupported")
	}
	rows, next, err := pager.Page("chat-conversation", before, limit)
	if err != nil {
		return nil, 0, err
	}
	items := []Conversation{}
	for _, raw := range rows {
		var item conversationRecord
		if err = json.Unmarshal(raw, &item); err != nil {
			return nil, 0, err
		}
		if item.Version != catalogVersion || item.Conversation.Target != s.Target() {
			continue
		}
		if allowed(item.Conversation.ID) {
			items = append(items, item.Conversation)
		}
	}
	return items, next, nil
}

// OperationStatus only consults metadata/receipts and never re-enqueues a body.
func (s *Service) OperationStatus(operation string) (*Conversation, *Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record, err := s.catalog.turn(operation); err == nil {
		return nil, &record.Turn, nil
	} else if !isMissing(err) {
		return nil, nil, err
	}
	var intent queueIntent
	if err := s.catalog.store.Get("chat-intent", operation, &intent); err == nil {
		return nil, &intent.Turn, nil
	} else if !isMissing(err) {
		return nil, nil, err
	}
	// Conversation IDs depend only on target and operation, not on runtime
	// configuration, so an old receipt remains queryable after model changes.
	id := createRecord(s.Target(), operation, Descriptor{}).Conversation.ID
	item, err := s.catalog.conversation(id)
	if err != nil {
		if PublicError(err).Code == "not_found" {
			return nil, nil, store.ErrNotFound
		}
		return nil, nil, err
	}
	return &item.Conversation, nil, nil
}
