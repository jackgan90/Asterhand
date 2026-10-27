package conversation

import (
	"context"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
	"sync"
	"testing"
)

func TestDefiniteRejectionReleasesGenerationSlot(t *testing.T) {
	runtime := &fixtureRuntime{submitError: Fail("rejected", "native pre-admission refusal")}
	service, _ := newFixtureService(t, runtime)
	id := createFixture(t, service)
	request := Request{Action: "submit", Target: service.Target(), Conversation: id, Operation: model.ID(), Text: "keep draft"}
	response, err := service.Call(context.Background(), request)
	if PublicError(err).Code != "rejected" || response.Turn.Delivery != "rejected" || response.Turn.Run != "failed" {
		t.Fatalf("rejection not preserved: %+v %v", response, err)
	}
	release, err := service.BeginConfiguration()
	if err != nil {
		t.Fatal("refused input still owns node slot", err)
	}
	release()
	runtime.submitError = nil
	request.Operation = model.ID()
	if _, err := service.Call(context.Background(), request); err != nil {
		t.Fatal("next explicit send blocked", err)
	}
	if runtime.sends != 2 {
		t.Fatal("unexpected automatic replay", runtime.sends)
	}
}

type pausedCreationCatalog struct {
	*store.Store
	mu     sync.Mutex
	paused bool
	id     string
	read   chan struct{}
	resume chan struct{}
}

func (p *pausedCreationCatalog) Get(kind, id string, value any) error {
	err := p.Store.Get(kind, id, value)
	p.mu.Lock()
	pause := err == nil && kind == "chat-conversation" && id == p.id && !p.paused
	if pause {
		p.paused = true
	}
	p.mu.Unlock()
	if pause {
		close(p.read)
		<-p.resume
	}
	return err
}

func TestStaleHistoryCreationPreservesLatestOperation(t *testing.T) {
	runtime := &fixtureRuntime{}
	service, records := newFixtureService(t, runtime)
	operation := model.ID()
	descriptor, _ := runtime.Describe()
	item := createRecord(service.Target(), operation, descriptor)
	if err := service.catalog.saveConversation(item); err != nil {
		t.Fatal(err)
	}
	paused := &pausedCreationCatalog{Store: records, id: item.Conversation.ID, read: make(chan struct{}), resume: make(chan struct{})}
	service.catalog.store = paused
	historyDone := make(chan error, 1)
	go func() {
		_, err := service.Call(context.Background(), Request{Action: "history", Target: service.Target(), Conversation: item.Conversation.ID})
		historyDone <- err
	}()
	<-paused.read
	if _, err := service.Call(context.Background(), Request{Action: "create", Target: service.Target(), Operation: operation}); err != nil {
		t.Fatal(err)
	}
	request := Request{Action: "submit", Target: service.Target(), Conversation: item.Conversation.ID, Operation: model.ID(), Text: "reviewer fixture"}
	if _, err := service.Call(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	before, err := service.catalog.latest(item.Conversation.ID)
	if err != nil || before == nil || before.Operation != request.Operation {
		t.Fatalf("setup missing latest: %+v %v", before, err)
	}
	close(paused.resume)
	if err := <-historyDone; err != nil {
		t.Fatal(err)
	}
	after, err := service.catalog.latest(item.Conversation.ID)
	if err != nil || after == nil || after.Operation != request.Operation {
		t.Fatalf("stale history creation erased latest operation: before=%+v after=%+v err=%v", before, after, err)
	}
}
