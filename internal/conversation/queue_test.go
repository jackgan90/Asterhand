package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

type queuedSend struct {
	binding    Binding
	submission Submission
}
type queuedRuntime struct {
	fixtureRuntime
	createError, prepareError             error
	createGate, prepareGate, dispatchGate chan struct{}
	prepared                              chan struct{}
	calls                                 chan queuedSend
	cancels                               chan string
}

func waitQueueGate(ctx context.Context, gate chan struct{}) error {
	if gate == nil {
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *queuedRuntime) Create(ctx context.Context, binding Binding) (Binding, error) {
	if err := waitQueueGate(ctx, r.createGate); err != nil {
		return binding, err
	}
	if r.createError != nil {
		return binding, r.createError
	}
	return r.fixtureRuntime.Create(ctx, binding)
}
func (r *queuedRuntime) Prepare(ctx context.Context, _ Binding) error {
	select {
	case r.prepared <- struct{}{}:
	default:
	}
	if err := waitQueueGate(ctx, r.prepareGate); err != nil {
		return err
	}
	return r.prepareError
}
func (r *queuedRuntime) Submit(ctx context.Context, binding Binding, submission Submission) (Receipt, error) {
	r.calls <- queuedSend{binding, submission}
	if err := waitQueueGate(ctx, r.dispatchGate); err != nil {
		return Receipt{}, err
	}
	return r.fixtureRuntime.Submit(ctx, binding, submission)
}
func (r *queuedRuntime) Cancel(_ context.Context, _ Binding, id string) error {
	r.cancels <- id
	return nil
}
func queueFixture(t *testing.T) (*Service, *queuedRuntime, *store.Store) {
	t.Helper()
	records, err := store.Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	runtime := &queuedRuntime{prepared: make(chan struct{}, 32), calls: make(chan queuedSend, 32), cancels: make(chan string, 32)}
	service := New(model.ID(), records, runtime)
	t.Cleanup(func() { service.Close(); records.Close() })
	return service, runtime, records
}
func queueCallOK(t *testing.T, s *Service, request Request) Response {
	t.Helper()
	request.Target = s.Target()
	response, err := s.Call(context.Background(), request)
	if err != nil {
		t.Fatalf("%s: %v", request.Action, err)
	}
	return response
}
func queuedRequest(id, text string) Request {
	return Request{Action: "enqueue", Conversation: id, Operation: model.ID(), Text: text, DraftRevision: 1}
}
func waitQueueSend(t *testing.T, r *queuedRuntime) queuedSend {
	t.Helper()
	select {
	case send := <-r.calls:
		return send
	case <-time.After(3 * time.Second):
		t.Fatal("queue did not dispatch")
		return queuedSend{}
	}
}
func awaitQueue(t *testing.T, s *Service, predicate func(*QueueState) bool) *QueueState {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state := queueCallOK(t, s, Request{Action: "queue"}).Queue
		if predicate(state) {
			return state
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("queue state did not converge")
	return nil
}
func completeQueueTurn(t *testing.T, s *Service, r *queuedRuntime) {
	t.Helper()
	r.mu.Lock()
	r.observation = Observation{Accepted: true, Stopped: true, Run: "completed", Saved: true}
	r.mu.Unlock()
	if _, _, err := s.reconcile(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestQueueColdCreateReturnsImmediatelyAndPreservesCrossConversationFIFO(t *testing.T) {
	s, r, _ := queueFixture(t)
	r.createGate = make(chan struct{})
	a := queueCallOK(t, s, Request{Action: "prepare", Operation: model.ID()}).Conversation
	first := queuedRequest(a.ID, "first immutable text")
	response := queueCallOK(t, s, first)
	if a.State != "creating" || response.Turn.Delivery != "queued" {
		t.Fatalf("no immediate custody: %+v", response)
	}
	b := queueCallOK(t, s, Request{Action: "prepare", Operation: model.ID()}).Conversation
	second := queuedRequest(b.ID, "second target")
	queueCallOK(t, s, second)
	history := queueCallOK(t, s, Request{Action: "history", Conversation: b.ID})
	if history.Conversation.State != "creating" {
		t.Fatal("history waited for startup")
	}
	if len(r.calls) != 0 {
		t.Fatal("sent before creation")
	}
	close(r.createGate)
	got := waitQueueSend(t, r)
	if got.submission.Text != first.Text || got.binding.Key == "" {
		t.Fatalf("wrong first dispatch: %+v", got)
	}
	awaitQueue(t, s, func(q *QueueState) bool { return q.Active != nil && q.Active.Delivery == "accepted" })
	if len(r.calls) != 0 {
		t.Fatal("second generation overlapped first")
	}
	completeQueueTurn(t, s, r)
	got2 := waitQueueSend(t, r)
	if got2.submission.Text != second.Text || got.binding.Key == got2.binding.Key {
		t.Fatalf("wrong target/order: %+v", got2)
	}
}

func TestQueueBoundsDedupAndMetadataOnlyRestart(t *testing.T) {
	s, r, records := queueFixture(t)
	id := createFixture(t, s)
	r.prepareGate = make(chan struct{})
	requests := make([]Request, QueueMessageLimit)
	for i := range requests {
		requests[i] = queuedRequest(id, fmt.Sprintf("private-queue-text-%d", i))
		queueCallOK(t, s, requests[i])
	}
	queueCallOK(t, s, requests[0])
	full := queuedRequest(id, "new draft must stay")
	full.Target = s.Target()
	if _, err := s.Call(context.Background(), full); PublicError(err).Code != "queue_full" {
		t.Fatalf("queue overflow: %v", err)
	}
	conflict := requests[0]
	conflict.Target = s.Target()
	conflict.Text = "different"
	if _, err := s.Call(context.Background(), conflict); PublicError(err).Code != "conflict" {
		t.Fatalf("operation reused: %v", err)
	}
	if _, err := s.BeginConfiguration(); PublicError(err).Code != "busy" {
		t.Fatal("config changed while queue owns text")
	}
	if s.BeginStop(false) {
		t.Fatal("exit discarded text without confirmation")
	}
	reply := queueCallOK(t, s, Request{Action: "queue_item", Operation: requests[0].Operation, Conversation: id})
	if reply.QueueItem.Text != requests[0].Text || len(reply.Queue.Entries) != 8 {
		t.Fatal("read changed queue custody")
	}
	for _, kind := range []string{"chat-intent", "chat-turn", "chat-conversation"} {
		var raw []map[string]any
		if err := records.List(kind, &raw); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(raw)
		if strings.Contains(string(data), "private-queue-text") {
			t.Fatal("queued body persisted in metadata")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.Target().Node, records, r)
	defer restarted.Close()
	response := queueCallOK(t, restarted, requests[1])
	if response.Turn.Delivery != "rejected" || response.Turn.Run != "cancelled" || len(response.Queue.Entries) != 0 || len(r.calls) != 0 {
		t.Fatalf("restart replayed RAM intent: %+v", response)
	}
	legacy := requests[1]
	legacy.Action = "submit"
	if response = queueCallOK(t, restarted, legacy); response.Turn.Delivery != "rejected" || len(r.calls) != 0 {
		t.Fatal("legacy submit replayed a cancelled queue identity")
	}
}

func TestPreparingMessagesCannotBeWithdrawnAndContinueAutomatically(t *testing.T) {
	s, r, _ := queueFixture(t)
	id := createFixture(t, s)
	r.prepareGate = make(chan struct{})
	first, second := queuedRequest(id, "already sent from user perspective"), queuedRequest(id, "next message")
	queueCallOK(t, s, first)
	select {
	case <-r.prepared:
	case <-time.After(time.Second):
		t.Fatal("did not prepare")
	}
	queueCallOK(t, s, second)
	for _, action := range []string{"queue_pause", "queue_resume", "queue_cancel", "cancel"} {
		request := Request{Action: action, Target: s.Target()}
		if action == "queue_cancel" || action == "cancel" {
			request.Conversation, request.Operation = id, first.Operation
		}
		if _, err := s.Call(context.Background(), request); err == nil {
			t.Fatalf("%s accepted during preparation", action)
		}
	}
	close(r.prepareGate)
	if got := waitQueueSend(t, r); got.submission.Text != first.Text {
		t.Fatal("first user message was withdrawn")
	}
	awaitQueue(t, s, func(q *QueueState) bool { return q.Active != nil && q.Active.Delivery == "accepted" })
	completeQueueTurn(t, s, r)
	if got := waitQueueSend(t, r); got.submission.Text != second.Text {
		t.Fatal("subsequent message required manual resume")
	}
}

func TestUnknownQueuedDispatchOnlyReconcilesAndDoesNotReplay(t *testing.T) {
	s, r, _ := queueFixture(t)
	id := createFixture(t, s)
	r.loseReply = true
	first, second := queuedRequest(id, "unknown receipt"), queuedRequest(id, "next")
	queueCallOK(t, s, first)
	queueCallOK(t, s, second)
	waitQueueSend(t, r)
	awaitQueue(t, s, func(q *QueueState) bool { return q.Paused && q.Entries[0].State == "unknown" })
	response := queueCallOK(t, s, first)
	if response.Turn.Delivery != "unknown" || len(r.calls) != 0 {
		t.Fatal("duplicate unknown send replayed")
	}
	completeQueueTurn(t, s, r)
	if got := waitQueueSend(t, r); got.submission.Text != second.Text {
		t.Fatal("unknown body replayed")
	}
	settled := queueCallOK(t, s, Request{Action: "queue_item", Conversation: id, Operation: first.Operation})
	if settled.QueueItem.Turn.Delivery != "accepted" || settled.QueueItem.Text != "" {
		t.Fatal("lost completed receipt")
	}
}

func TestStopReplyKeepsUserMessageAndDoesNotPauseNextSend(t *testing.T) {
	s, r, _ := queueFixture(t)
	id := createFixture(t, s)
	first, second := queuedRequest(id, "stop this reply"), queuedRequest(id, "deliver next normally")
	queueCallOK(t, s, first)
	waitQueueSend(t, r)
	active := awaitQueue(t, s, func(q *QueueState) bool { return q.Active != nil && q.Active.Delivery == "accepted" }).Active
	queueCallOK(t, s, second)
	stop := Request{Action: "cancel", Conversation: id, Operation: first.Operation}
	queueCallOK(t, s, stop)
	if got := <-r.cancels; got != active.ID {
		t.Fatal("stopped wrong native turn")
	}
	r.mu.Lock()
	r.observation = Observation{Accepted: true, Stopped: true, Run: "cancelled", Saved: true}
	r.mu.Unlock()
	queueCallOK(t, s, Request{Action: "reconcile", Conversation: id, Operation: first.Operation})
	if got := waitQueueSend(t, r); got.submission.Text != second.Text {
		t.Fatal("stop paused or replayed queued text")
	}
	awaitQueue(t, s, func(q *QueueState) bool {
		return q.Active != nil && q.Active.Operation == second.Operation && q.Active.Delivery == "accepted"
	})
	queueCallOK(t, s, stop)
	if len(r.cancels) != 0 {
		t.Fatal("late stop affected new turn")
	}
	reply := queueCallOK(t, s, Request{Action: "queue_item", Conversation: id, Operation: first.Operation})
	if reply.QueueItem.Turn.Delivery != "accepted" || reply.QueueItem.Turn.Run != "cancelled" || reply.Queue.Paused {
		t.Fatal("stop withdrew user message or paused queue")
	}
}

func TestFailedCreationSettlesDependentsAndNewSendPreparesSameConversation(t *testing.T) {
	s, r, _ := queueFixture(t)
	r.createError = Fail("unavailable", "fixture startup failure")
	r.createGate = make(chan struct{})
	operation := model.ID()
	id := queueCallOK(t, s, Request{Action: "prepare", Operation: operation}).Conversation.ID
	message := queuedRequest(id, "preserve on startup failure")
	queueCallOK(t, s, message)
	close(r.createGate)
	awaitQueue(t, s, func(q *QueueState) bool { return len(q.Entries) == 0 && len(q.Failures) == 2 })
	reply := queueCallOK(t, s, Request{Action: "queue_item", Conversation: id, Operation: message.Operation})
	if reply.QueueItem.Text != message.Text || reply.QueueItem.Turn.Delivery != "rejected" {
		t.Fatal("undispatched text was not recoverable")
	}
	release, err := s.BeginConfiguration()
	if err != nil {
		t.Fatal("failed startup permanently blocked settings", err)
	}
	release()
	if len(r.calls) != 0 {
		t.Fatal("startup failure dispatched text")
	}
	r.createError = nil
	next := queuedRequest(id, "new send after configuration repair")
	queueCallOK(t, s, next)
	if got := waitQueueSend(t, r); got.submission.Text != next.Text {
		t.Fatal("new send replayed failed message")
	}
}

func TestStoppedUnknownReceiptReleasesFIFOWithoutFabricatingRejection(t *testing.T) {
	s, r, _ := queueFixture(t)
	id := createFixture(t, s)
	r.loseReply = true
	request := queuedRequest(id, "unknown does not mean rejected")
	queueCallOK(t, s, request)
	waitQueueSend(t, r)
	awaitQueue(t, s, func(q *QueueState) bool { return q.Paused && q.Entries[0].State == "unknown" })
	r.mu.Lock()
	r.observation = Observation{Stopped: true, Run: "interrupted"}
	r.mu.Unlock()
	if _, _, err := s.reconcile(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	awaitQueue(t, s, func(q *QueueState) bool { return len(q.Entries) == 0 && len(q.Failures) == 1 })
	response := queueCallOK(t, s, Request{Action: "queue_item", Conversation: id, Operation: request.Operation})
	if response.QueueItem.Turn.Delivery != "unknown" {
		t.Fatal("removal fabricated rejection")
	}
	result := queueCallOK(t, s, request)
	if result.Turn.Delivery != "unknown" || len(r.calls) != 0 {
		t.Fatal("removed unknown operation replayed")
	}
}
