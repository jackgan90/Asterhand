package conversation

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/store"
)

// Service has one node-wide generation slot. It shares no scheduling state with
// file tasks. The catalog persists identities; queued text lives only in RAM
// until Runtime takes durable custody of the message body.
type Service struct {
	queue         []*queuedOperation
	queuePaused   string
	queueFailures []*queuedOperation
	queueWake     chan struct{}
	queueStarted  bool
	queueRevision uint64
	mu            sync.Mutex
	observe       sync.Mutex
	catalog       catalog
	runtime       Runtime
	active        *turnRecord
	dispatching   bool
	creating      map[string]bool
	stopping      bool
	configuring   bool
	fault         error
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	start         sync.Once
	close         sync.Once
	closeErr      error
	onCompleted   func()
}

// SetCompletionHandler connects a best-effort desktop notification. The handler
// runs outside mu and must only enqueue bounded work, never call back into Call.
func (s *Service) SetCompletionHandler(handler func()) {
	s.mu.Lock()
	s.onCompleted = handler
	s.mu.Unlock()
}

func New(node string, records *store.Store, runtime Runtime) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{catalog: catalog{store: records, target: Target{Node: node, Role: DefaultRole}}, runtime: runtime,
		creating: map[string]bool{}, queueWake: make(chan struct{}, 1), ctx: ctx, cancel: cancel}
	s.recoverCatalog()
	s.recoverQueueIntents()
	return s
}

func (s *Service) Target() Target { return s.catalog.target }

func (s *Service) recoverCatalog() {
	records, err := s.catalog.turns()
	if err != nil {
		s.fault = err
		return
	}
	for _, record := range records {
		if !record.Unsettled {
			continue
		}
		if s.active != nil {
			s.fault = Fail("recovery_required", "存在多个未核对轮次，请保留会话目录并检查运行时")
			return
		}
		copy := record
		s.active = &copy
		// A catalog write may have failed between reserving the turn and touching
		// its conversation. Restore the metadata pointer without replaying text.
		item, err := s.catalog.conversation(record.Turn.Conversation)
		if err == nil {
			item.LatestOperation = record.Turn.Operation
			err = s.catalog.saveConversation(item)
		}
		if err != nil {
			s.fault = err
			return
		}
	}
}

func (s *Service) Start() {
	s.start.Do(func() {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					_, _, _ = s.reconcile(s.ctx, "")
				case <-s.ctx.Done():
					return
				}
			}
		}()
	})
}

func (s *Service) Call(ctx context.Context, request Request) (response Response, err error) {
	response = Response{Target: s.Target()}
	if request.Action == "connect" || request.Action == "connection" {
		response.Connection = "unavailable"
	}
	defer func() { s.mu.Lock(); response.Queue = s.queueSnapshotLocked(); s.mu.Unlock() }()
	if err := request.Validate(); err != nil {
		return response, err
	}
	if request.Action != "hello" && request.Target != s.Target() {
		return response, Fail("wrong_target", "会话目标不属于当前节点")
	}
	s.mu.Lock()
	fault, stopping := s.fault, s.stopping
	response.Busy = s.active != nil
	if s.active != nil {
		response.ActiveConversation = s.active.Turn.Conversation
	}
	s.mu.Unlock()
	if fault != nil {
		return response, fault
	}
	if stopping {
		return response, Fail("stopping", "节点正在退出，草稿已保留")
	}
	descriptor, describeErr := s.runtime.Describe()
	response.Capabilities, response.CanSend = descriptor.Capabilities, descriptor.CanSend
	response.Prompt = descriptor.Prompt.Normalize()
	if describeErr == nil {
		caps := descriptor.Capabilities
		if (request.Action == "create" || request.Action == "submit" || request.Action == "prepare" || request.Action == "enqueue") && (!caps.History || !caps.DurableReceipts) ||
			(request.Action == "history" || request.Action == "message") && !caps.History || request.Action == "cancel" && !caps.Cancellation {
			return response, Fail("unsupported", "当前运行时未提供此会话能力")
		}
	}
	switch request.Action {
	case "connect", "connection":
		if runtime, ok := s.runtime.(interface {
			Connection(context.Context, bool) string
		}); ok && describeErr == nil {
			response.Connection = runtime.Connection(ctx, request.Action == "connect")
		}
		return response, nil
	case "prepare", "enqueue", "queue", "queue_item":
		if describeErr != nil && (request.Action == "prepare" || request.Action == "enqueue") {
			return response, describeErr
		}
		return s.queueCall(request, response)
	case "hello":
		return response, describeErr
	case "list":
		var err error
		response.Conversations, err = s.catalog.list(request.Offset)
		return response, err
	case "create":
		if describeErr != nil {
			return response, describeErr
		}
		item, err := s.create(ctx, request.Operation)
		response.Conversation = &item
		return response, err
	case "submit":
		if describeErr != nil {
			return response, describeErr
		}
		turn, err := s.submit(ctx, request)
		response.Turn = turn
		return response, err
	default:
		return s.readOrCancel(ctx, request, response)
	}
}

func (s *Service) readOrCancel(ctx context.Context, request Request, response Response) (Response, error) {
	item, err := s.catalog.conversation(request.Conversation)
	if err != nil {
		return response, err
	}
	if item.Conversation.State == "creating" && request.Action == "history" {
		// Reading an unprepared or failed conversation never retries creation.
		// A subsequent explicit send queues a new preparation with the same key.
		response.Conversation = &item.Conversation
		return response, nil
	}
	response.Conversation = &item.Conversation
	if request.Operation != "" {
		record, err := s.catalog.turn(request.Operation)
		if err != nil || record.Turn.Conversation != request.Conversation {
			return response, Fail("not_found", "操作不属于此会话")
		}
		response.Turn = &record.Turn
	} else {
		response.Turn, err = s.catalog.latest(request.Conversation)
		if err != nil {
			return response, err
		}
	}
	switch request.Action {
	case "select":
		return response, nil
	case "history":
		if response.Turn != nil && (response.Turn.Delivery != "accepted" || response.Turn.Persistence != "saved") {
			turn, _, reconcileErr := s.reconcile(ctx, response.Turn.Operation)
			if reconcileErr == nil {
				response.Turn = turn
			}
		}
		page, err := s.runtime.History(ctx, item.Binding, request.Offset, request.Cursor)
		response.Page = &page
		return response, err
	case "message":
		fragment, err := s.runtime.Message(ctx, item.Binding, request.Message, request.Offset)
		response.Fragment = &fragment
		return response, err
	case "cancel":
		if response.Turn == nil {
			return response, nil
		}
		if response.Turn.Delivery != "accepted" {
			return response, Fail("not_ready", "当前没有可停止的回复")
		}
		if err = s.cancelTurn(ctx, response.Turn.Operation); err != nil {
			return response, err
		}
		fallthrough
	case "reconcile":
		if response.Turn != nil {
			response.Turn, response.Live, err = s.reconcile(ctx, response.Turn.Operation)
		}
		return response, err
	}
	return response, Fail("unsupported", "不支持此会话操作")
}

func (s *Service) create(ctx context.Context, operation string) (Conversation, error) {
	s.mu.Lock()
	if s.stopping || s.configuring {
		s.mu.Unlock()
		return Conversation{}, Fail("busy", "节点正在切换配置或退出")
	}
	descriptor, err := s.runtime.Describe()
	if err != nil {
		s.mu.Unlock()
		return Conversation{}, err
	}
	item := createRecord(s.Target(), operation, descriptor)
	old, err := s.catalog.conversation(item.Conversation.ID)
	if err == nil {
		item = old
	} else if PublicError(err).Code != "not_found" {
		s.mu.Unlock()
		return Conversation{}, err
	} else if err = s.catalog.saveConversation(item); err != nil {
		s.mu.Unlock()
		return Conversation{}, err
	}
	if item.Conversation.State == "ready" || s.creating[item.Conversation.ID] {
		s.mu.Unlock()
		return item.Conversation, nil
	}
	s.mu.Unlock()
	item, err = s.finishCreation(ctx, item)
	return item.Conversation, err
}

func (s *Service) finishCreation(ctx context.Context, item conversationRecord) (conversationRecord, error) {
	s.mu.Lock()
	// A history read may have captured creating before another caller completed
	// creation and submitted a turn. Never publish that stale directory snapshot.
	current, err := s.catalog.conversation(item.Conversation.ID)
	if err != nil {
		s.mu.Unlock()
		return item, err
	}
	item = current
	if item.Conversation.State == "ready" {
		s.mu.Unlock()
		return item, nil
	}
	if s.stopping || s.configuring || s.creating[item.Conversation.ID] {
		s.mu.Unlock()
		return item, Fail("busy", "会话正在创建或节点正在切换状态，请刷新核对")
	}
	s.creating[item.Conversation.ID] = true
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.creating, item.Conversation.ID)
		s.mu.Unlock()
		s.wg.Done()
	}()
	bound, err := s.runtime.Create(ctx, item.Binding)
	if err != nil {
		return item, err
	}
	item.Binding, item.Conversation.State = bound, "ready"
	item.Conversation.UpdatedAt = time.Now().UTC()
	if err = s.catalog.saveConversation(item); err != nil {
		return item, err
	}
	return item, nil
}

// BeginConfiguration closes admission atomically with the busy check. The app
// must defer the returned release until credentials and settings are settled.
func (s *Service) BeginConfiguration() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping || s.configuring || s.active != nil || len(s.creating) != 0 || len(s.queue) != 0 {
		return nil, Fail("busy", "请先处理待发消息，并停止和核对当前聊天，再修改模型设置")
	}
	s.configuring = true
	var once sync.Once
	return func() { once.Do(func() { s.mu.Lock(); s.configuring = false; s.mu.Unlock() }) }, nil
}

func (s *Service) BeginStop(confirmed bool, otherAdmission ...func() bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !confirmed && (s.active != nil || len(s.creating) != 0 || len(s.queue) != 0) {
		return false
	}
	for _, ready := range otherAdmission {
		if !ready() {
			return false
		}
	}
	s.stopping = true
	return true
}

func (s *Service) Close() error {
	s.close.Do(func() {
		s.BeginStop(true)
		s.mu.Lock()
		s.discardQueueLocked()
		var operation string
		if s.active != nil {
			operation = s.active.Turn.Operation
		}
		s.mu.Unlock()
		if operation != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = s.cancelTurn(ctx, operation)
			_, _, _ = s.reconcile(ctx, operation)
			cancel()
		}
		s.cancel()
		s.closeErr = s.runtime.Close()
		s.wg.Wait()
		s.mu.Lock()
		s.queue = nil
		s.mu.Unlock()
	})
	return s.closeErr
}

func isMissing(err error) bool { return errors.Is(err, store.ErrNotFound) }
