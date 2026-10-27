package conversation

import (
	"errors"
	"sort"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

const catalogVersion = 1

// These records intentionally contain no prompt, title derived from a prompt,
// response body, or runtime credential. Bodies have one owner: the harness.
type conversationRecord struct {
	Version         int          `json:"version"`
	Conversation    Conversation `json:"conversation"`
	Binding         Binding      `json:"binding"`
	LatestOperation string       `json:"latest_operation,omitempty"`
}

type turnRecord struct {
	Version        int     `json:"version"`
	Target         Target  `json:"target"`
	Turn           Turn    `json:"turn"`
	Digest         string  `json:"digest"`
	DraftRevision  uint64  `json:"draft_revision"`
	RuntimeRequest string  `json:"runtime_request"`
	Binding        Binding `json:"binding"`
	Unsettled      bool    `json:"unsettled"`
}

type catalog struct {
	store  metadataStore
	target Target
}

type metadataStore interface {
	Get(string, string, any) error
	Put(string, string, any) error
	List(string, any) error
}

func (c catalog) conversation(id string) (conversationRecord, error) {
	var record conversationRecord
	err := c.store.Get("chat-conversation", id, &record)
	if errors.Is(err, store.ErrNotFound) {
		return record, Fail("not_found", "会话不存在")
	}
	if err != nil {
		return record, err
	}
	if record.Version != catalogVersion || record.Conversation.Target != c.target {
		return record, Fail("binding_changed", "会话目录版本或目标不匹配")
	}
	return record, nil
}

func (c catalog) turn(operation string) (turnRecord, error) {
	var record turnRecord
	err := c.store.Get("chat-turn", operation, &record)
	if err == nil && (record.Version != catalogVersion || record.Target != c.target) {
		err = Fail("binding_changed", "轮次目录版本或目标不匹配")
	}
	return record, err
}

func (c catalog) saveConversation(record conversationRecord) error {
	return c.store.Put("chat-conversation", record.Conversation.ID, record)
}

func (c catalog) saveTurn(record turnRecord) error {
	return c.store.Put("chat-turn", record.Turn.Operation, record)
}

func (c catalog) list(offset int) ([]Conversation, error) {
	var records []conversationRecord
	if err := c.store.List("chat-conversation", &records); err != nil {
		return nil, err
	}
	list := make([]Conversation, 0, len(records))
	for _, record := range records {
		if record.Version != catalogVersion {
			return nil, Fail("binding_changed", "会话目录版本不兼容，请使用匹配的版本")
		}
		if record.Conversation.Target == c.target {
			list = append(list, record.Conversation)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].UpdatedAt.After(list[j].UpdatedAt) })
	if offset >= len(list) {
		return []Conversation{}, nil
	}
	return list[offset:min(offset+50, len(list))], nil
}

func (c catalog) turns() ([]turnRecord, error) {
	var records []turnRecord
	if err := c.store.List("chat-turn", &records); err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.Version != catalogVersion || record.Target != c.target {
			return nil, Fail("binding_changed", "轮次目录版本或目标不匹配")
		}
	}
	return records, nil
}

func (c catalog) latest(conversation string) (*Turn, error) {
	item, err := c.conversation(conversation)
	if err != nil {
		return nil, err
	}
	if item.LatestOperation == "" {
		return nil, nil
	}
	record, err := c.turn(item.LatestOperation)
	return &record.Turn, err
}

func createRecord(target Target, operation string, d Descriptor) conversationRecord {
	id := model.Hash(struct {
		Target    Target
		Operation string
	}{target, operation})[:32]
	now := time.Now().UTC()
	return conversationRecord{
		Version:      catalogVersion,
		Conversation: Conversation{ID: id, Target: target, RoleRevision: "default-v1", Runtime: d.ID, State: "creating", CreatedAt: now, UpdatedAt: now},
		Binding:      Binding{Runtime: d.ID, Generation: d.Binding, Key: id},
	}
}
