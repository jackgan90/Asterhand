package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
)

func TestPreparationFailureKeepsBoundedFeedbackAndReleasesAdmission(t *testing.T) {
	s, runtime, _ := queueFixture(t)
	id := createFixture(t, s)
	runtime.prepareError = Fail("unavailable", "controlled preparation failure")
	var failed Request
	for index := 0; index < QueueMessageLimit+2; index++ {
		failed = queuedRequest(id, fmt.Sprintf("failed message %d", index))
		queueCallOK(t, s, failed)
		awaitQueue(t, s, func(q *QueueState) bool { return len(q.Entries) == 0 && len(q.Failures) > 0 })
	}
	state := queueCallOK(t, s, Request{Action: "queue"}).Queue
	if state.Paused || len(state.Failures) != QueueMessageLimit || len(runtime.calls) != 0 {
		t.Fatalf("failure retained queue work or dispatched: %+v", state)
	}
	for _, entry := range state.Failures {
		if entry.Text != "" || entry.Turn.Delivery != "rejected" || entry.Turn.Run != "failed" {
			t.Fatalf("invalid failed snapshot: %+v", entry)
		}
	}
	restored := queueCallOK(t, s, Request{Action: "queue_item", Conversation: id, Operation: failed.Operation}).QueueItem
	if restored.Text != failed.Text || restored.Turn.Delivery != "rejected" {
		t.Fatal("failed body could not be restored after host reconstruction")
	}
	release, err := s.BeginConfiguration()
	if err != nil {
		t.Fatal("terminal feedback blocked configuration", err)
	}
	release()
	runtime.prepareError = nil
	if got := queueCallOK(t, s, failed).Turn; got.Delivery != "rejected" {
		t.Fatal("duplicate failed intent was retried")
	}
	next := queuedRequest(id, "new intentional send")
	queueCallOK(t, s, next)
	if got := waitQueueSend(t, runtime); got.submission.Text != next.Text {
		t.Fatal("old failure replayed before new message")
	}
}

type failingQueueSettlementStore struct {
	metadataStore
	fail atomic.Bool
}

func (s *failingQueueSettlementStore) Put(kind, id string, value any) error {
	data, _ := json.Marshal(value)
	var intent queueIntent
	_ = json.Unmarshal(data, &intent)
	if kind == "chat-intent" && intent.Turn.Delivery == "rejected" && s.fail.Load() {
		return errors.New("controlled metadata write failure")
	}
	return s.metadataStore.Put(kind, id, value)
}

func TestFailedReceiptMustPersistBeforeQueueReleasesCustody(t *testing.T) {
	s, runtime, records := queueFixture(t)
	id := createFixture(t, s)
	storage := &failingQueueSettlementStore{metadataStore: records}
	storage.fail.Store(true)
	s.catalog.store = storage
	runtime.prepareError = Fail("unavailable", "controlled preparation failure")
	request := queuedRequest(id, "never retry failed message")
	queueCallOK(t, s, request)
	awaitQueue(t, s, func(q *QueueState) bool { return len(q.Entries) == 1 && q.Entries[0].State == "failed" })
	if len(runtime.calls) != 0 || len(queueCallOK(t, s, Request{Action: "queue"}).Queue.Failures) != 0 {
		t.Fatal("failed metadata write released custody")
	}
	storage.fail.Store(false)
	awaitQueue(t, s, func(q *QueueState) bool { return len(q.Entries) == 0 && len(q.Failures) == 1 })
	duplicate := queueCallOK(t, s, request)
	if duplicate.Turn.Delivery != "rejected" || len(runtime.calls) != 0 {
		t.Fatal("metadata retry dispatched text")
	}
	legacy := request
	legacy.Action, legacy.Target = "submit", s.Target()
	response, err := s.Call(context.Background(), legacy)
	if err != nil || response.Turn.Delivery != "rejected" || len(runtime.calls) != 0 {
		t.Fatal("direct submit bypassed terminal receipt", err)
	}
}
