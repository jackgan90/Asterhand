package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

type fixtureRuntime struct {
	mu           sync.Mutex
	sends        int
	createCalls  int
	loseReply    bool
	observation  Observation
	entered      chan struct{}
	release      chan struct{}
	afterSubmit  func()
	submitError  error
	capabilities *Capabilities
}

func (f *fixtureRuntime) Describe() (Descriptor, error) {
	capabilities := Capabilities{History: true, Streaming: true, Cancellation: true, DurableReceipts: true}
	if f.capabilities != nil {
		capabilities = *f.capabilities
	}
	return Descriptor{ID: "fixture", Binding: "fixture-v1", Provider: "fixture", Model: "fixture/chat", CanSend: true,
		Capabilities: capabilities}, nil
}
func (f *fixtureRuntime) Create(_ context.Context, binding Binding) (Binding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls++
	binding.Session, binding.Lifecycle = "session-"+binding.Key, "lifecycle-1"
	return binding, nil
}
func (f *fixtureRuntime) Submit(ctx context.Context, _ Binding, _ Submission) (Receipt, error) {
	f.mu.Lock()
	f.sends++
	f.mu.Unlock()
	if f.entered != nil {
		close(f.entered)
		select {
		case <-f.release:
		case <-ctx.Done():
			return Receipt{}, ctx.Err()
		}
	}
	if f.loseReply {
		return Receipt{}, errors.New("fixture connection lost after commit")
	}
	if f.afterSubmit != nil {
		f.afterSubmit()
	}
	if f.submitError != nil {
		return Receipt{}, f.submitError
	}
	return Receipt{Accepted: true, UserMessage: "native-user-message-1"}, nil
}
func (f *fixtureRuntime) Observe(context.Context, Binding, string, time.Time) (Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.observation, nil
}
func (f *fixtureRuntime) History(context.Context, Binding, int, string) (Page, error) {
	return Page{Messages: []Message{}}, nil
}
func (f *fixtureRuntime) Message(context.Context, Binding, string, int) (Fragment, error) {
	return Fragment{}, nil
}
func (f *fixtureRuntime) Cancel(context.Context, Binding, string) error { return nil }
func (f *fixtureRuntime) Close() error                                  { return nil }

func newFixtureService(t *testing.T, runtime *fixtureRuntime) (*Service, *store.Store) {
	t.Helper()
	records, err := store.Open(filepath.Join(t.TempDir(), "records.db"))
	if err != nil {
		t.Fatal(err)
	}
	service := New(model.ID(), records, runtime)
	t.Cleanup(func() { service.Close(); records.Close() })
	return service, records
}

func createFixture(t *testing.T, service *Service) string {
	t.Helper()
	response, err := service.Call(context.Background(), Request{Action: "create", Target: service.Target(), Operation: model.ID()})
	if err != nil || response.Conversation.State != "ready" {
		t.Fatalf("create: %+v %v", response, err)
	}
	return response.Conversation.ID
}

func TestLostAcknowledgementReconcilesWithoutReplaying(t *testing.T) {
	runtime := &fixtureRuntime{loseReply: true}
	service, records := newFixtureService(t, runtime)
	conversation := createFixture(t, service)
	request := Request{Action: "submit", Target: service.Target(), Conversation: conversation, Operation: model.ID(), Text: "private fixture prompt never copied to catalog", DraftRevision: 7}
	response, err := service.Call(context.Background(), request)
	if err == nil || response.Turn.Delivery != "unknown" {
		t.Fatalf("lost acknowledgement: %+v %v", response, err)
	}
	response, err = service.Call(context.Background(), request)
	if err != nil || response.Turn.Delivery != "unknown" || runtime.sends != 1 {
		t.Fatalf("duplicate replayed: %+v %v sends=%d", response, err, runtime.sends)
	}
	conflict := request
	conflict.Text = "changed"
	if _, err = service.Call(context.Background(), conflict); PublicError(err).Code != "conflict" {
		t.Fatalf("conflict accepted: %v", err)
	}
	var raw []map[string]any
	if err := records.List("chat-turn", &raw); err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(raw)
	if strings.Contains(string(serialized), request.Text) {
		t.Fatal("catalog contains message body")
	}
	var stored turnRecord
	if err := records.Get("chat-turn", request.Operation, &stored); err != nil || stored.Digest == "" || stored.RuntimeRequest == "" {
		t.Fatalf("missing recovery identity: %+v %v", stored, err)
	}
	// A fresh service reconstructs only metadata; it has no body to replay.
	runtime.observation = Observation{Accepted: true, Stopped: true, Run: "completed", Saved: true, UserMessage: "native-user-message-1", AssistantMessage: "native-assistant-1"}
	recovered := New(service.Target().Node, records, runtime)
	defer recovered.Close()
	result, err := recovered.Call(context.Background(), Request{Action: "reconcile", Target: service.Target(), Conversation: conversation, Operation: request.Operation})
	if err != nil || result.Turn.Delivery != "accepted" || result.Turn.Persistence != "saved" || result.Turn.Run != "completed" || runtime.sends != 1 {
		t.Fatalf("recovery: %+v %v sends=%d", result, err, runtime.sends)
	}
}

func TestNodeSlotAndConfigurationGuardCoverDispatch(t *testing.T) {
	runtime := &fixtureRuntime{entered: make(chan struct{}), release: make(chan struct{})}
	service, _ := newFixtureService(t, runtime)
	first, second := createFixture(t, service), createFixture(t, service)
	done := make(chan error, 1)
	go func() {
		_, err := service.Call(context.Background(), Request{Action: "submit", Target: service.Target(), Conversation: first, Operation: model.ID(), Text: "first"})
		done <- err
	}()
	<-runtime.entered
	if _, err := service.BeginConfiguration(); PublicError(err).Code != "busy" {
		t.Fatalf("configuration admitted: %v", err)
	}
	if service.BeginStop(false) {
		t.Fatal("busy node stopped without confirmation")
	}
	if _, err := service.Call(context.Background(), Request{Action: "submit", Target: service.Target(), Conversation: second, Operation: model.ID(), Text: "second"}); PublicError(err).Code != "busy" {
		t.Fatalf("second generation admitted: %v", err)
	}
	close(runtime.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Historic delivery uncertainty must not hold the runtime slot after proof
	// that execution stopped and cannot resume.
	runtime.observation = Observation{Stopped: true, Run: "interrupted"}
	if _, _, err := service.reconcile(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	release, err := service.BeginConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestIdentityCreationAndClosedCapabilities(t *testing.T) {
	runtime := &fixtureRuntime{}
	service, _ := newFixtureService(t, runtime)
	operation := model.ID()
	request := Request{Action: "create", Target: service.Target(), Operation: operation}
	first, err := service.Call(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Call(context.Background(), request)
	if err != nil || first.Conversation.ID != second.Conversation.ID || runtime.createCalls != 1 {
		t.Fatalf("create duplicate: %+v %v", second, err)
	}
	for _, request := range []Request{
		{Action: "list", Target: Target{Node: model.ID(), Role: DefaultRole}},
		{Action: "gateway", Target: service.Target()},
		{Action: "list", Target: service.Target(), Text: "unexpected"},
		{Action: "submit", Target: service.Target(), Conversation: first.Conversation.ID, Operation: model.ID(), Text: "  /reset"},
	} {
		if _, err := service.Call(context.Background(), request); err == nil {
			t.Fatalf("accepted forbidden request: %+v", request)
		}
	}
	if runtime.sends != 0 {
		t.Fatal("validation reached model")
	}
}

func TestCatalogWriteFailureAfterNativeAcceptanceRecoversWithoutReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	records, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fixtureRuntime{}
	service := New(model.ID(), records, runtime)
	defer service.Close()
	conversation := createFixture(t, service)
	runtime.afterSubmit = func() { records.Close() }
	request := Request{Action: "submit", Target: service.Target(), Conversation: conversation, Operation: model.ID(), Text: "accepted before catalog failure"}
	response, err := service.Call(context.Background(), request)
	if err == nil || response.Turn == nil || response.Turn.Delivery != "accepted" {
		t.Fatalf("lost write boundary: %+v %v", response, err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	runtime.afterSubmit = nil
	runtime.observation = Observation{Accepted: true, Stopped: true, Saved: true, Run: "completed", AssistantMessage: "native-assistant"}
	recovered := New(service.Target().Node, reopened, runtime)
	defer recovered.Close()
	response, err = recovered.Call(context.Background(), Request{Action: "reconcile", Target: service.Target(), Conversation: conversation, Operation: request.Operation})
	if err != nil || response.Turn.Persistence != "saved" || runtime.sends != 1 {
		t.Fatalf("recovery replayed: %+v %v sends=%d", response, err, runtime.sends)
	}
}

func TestNewerCatalogSchemaFailsClosed(t *testing.T) {
	runtime := &fixtureRuntime{}
	service, records := newFixtureService(t, runtime)
	op := model.ID()
	if err := records.Put("chat-turn", op, turnRecord{Version: catalogVersion + 1, Target: service.Target(), Turn: Turn{Operation: op}}); err != nil {
		t.Fatal(err)
	}
	recovered := New(service.Target().Node, records, runtime)
	defer recovered.Close()
	if _, err := recovered.Call(context.Background(), Request{Action: "hello"}); PublicError(err).Code != "binding_changed" {
		t.Fatalf("newer schema accepted: %v", err)
	}
	if runtime.sends != 0 {
		t.Fatal("schema failure invoked model")
	}
}

type failingConversationStore struct {
	*store.Store
	fail bool
}

func (f *failingConversationStore) Put(kind, id string, value any) error {
	if f.fail && kind == "chat-conversation" {
		return errors.New("fixture catalog disk full")
	}
	return f.Store.Put(kind, id, value)
}

func TestConversationPointerFailureNeverDispatches(t *testing.T) {
	runtime := &fixtureRuntime{}
	service, records := newFixtureService(t, runtime)
	conversation := createFixture(t, service)
	failing := &failingConversationStore{records, true}
	service.catalog.store = failing
	request := Request{Action: "submit", Target: service.Target(), Conversation: conversation, Operation: model.ID(), Text: "unsent"}
	response, err := service.Call(context.Background(), request)
	if err == nil || response.Turn == nil || response.Turn.Delivery != "rejected" || runtime.sends != 0 {
		t.Fatalf("pre-dispatch storage failure: %+v %v", response, err)
	}
	failing.fail = false
	if _, _, err = service.reconcile(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	release, err := service.BeginConfiguration()
	if err != nil {
		t.Fatal("recovered disk still blocks idle runtime", err)
	}
	release()
}

func TestStoppedHistoricUncertaintyCanStillResolve(t *testing.T) {
	runtime := &fixtureRuntime{loseReply: true}
	service, _ := newFixtureService(t, runtime)
	conversation := createFixture(t, service)
	request := Request{Action: "submit", Target: service.Target(), Conversation: conversation, Operation: model.ID(), Text: "original"}
	_, _ = service.Call(context.Background(), request)
	runtime.observation = Observation{Stopped: true, Run: "interrupted"}
	if _, _, err := service.reconcile(context.Background(), request.Operation); err != nil {
		t.Fatal(err)
	}
	release, err := service.BeginConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	release()
	runtime.observation = Observation{Accepted: true, Saved: true, Stopped: true, Run: "completed", AssistantMessage: "late-persisted"}
	response, err := service.Call(context.Background(), Request{Action: "history", Target: service.Target(), Conversation: conversation})
	if err != nil || response.Turn.Persistence != "saved" || response.Turn.Delivery != "accepted" || runtime.sends != 1 {
		t.Fatalf("historical uncertainty never resolved: %+v %v", response, err)
	}
}

func TestRuntimeMissingDurableConversationCapabilityIsRejected(t *testing.T) {
	runtime := &fixtureRuntime{capabilities: &Capabilities{Streaming: true}}
	service, _ := newFixtureService(t, runtime)
	_, err := service.Call(context.Background(), Request{Action: "create", Target: service.Target(), Operation: model.ID()})
	if PublicError(err).Code != "unsupported" || runtime.createCalls != 0 || runtime.sends != 0 {
		t.Fatalf("unsupported runtime admitted: %v", err)
	}
}
