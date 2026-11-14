package notifications

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Service owns one queue for the node. Execution only takes its short memory
// lock; all process, display and user actions run outside that lock.
type Service struct {
	mu                   sync.Mutex
	queue                Queue
	theme, locale, state string
	presenter            Presenter
	wake                 chan struct{}
	ctx                  context.Context
	cancel               context.CancelFunc
	done                 chan struct{}
	actions              chan Card
	results              chan actionResult
	open                 func(context.Context, Card) string
	closed               bool
}
type actionResult struct {
	id  uint64
	key string
}

func New(p Presenter, open func(context.Context, Card) string) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{presenter: p, open: open, theme: "star", locale: "en-US", state: "idle", wake: make(chan struct{}, 1),
		ctx: ctx, cancel: cancel, done: make(chan struct{}), actions: make(chan Card, 1), results: make(chan actionResult, 1)}
	if p == nil {
		s.state = "unsupported"
	}
	go s.run()
	return s
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) Enqueue(c Completion) {
	s.mu.Lock()
	if !s.closed && s.presenter != nil {
		s.queue.Enqueue(c, time.Now())
	}
	s.mu.Unlock()
	s.signal()
}
func (s *Service) Update(theme, locale string) {
	s.mu.Lock()
	s.theme, s.locale = theme, locale
	s.queue.revision++
	s.mu.Unlock()
	s.signal()
}
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{State: s.state, Pending: len(s.queue.pending), Visible: len(s.queue.active)}
}
func (s *Service) Close() error {
	s.Stop()
	<-s.done
	if s.presenter != nil {
		return s.presenter.Close()
	}
	return nil
}
func (s *Service) Stop() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
}

func (s *Service) runActions(done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-s.ctx.Done():
			return
		case card := <-s.actions:
			ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
			key := "err.localcmd.log_window_failed"
			if s.open != nil {
				key = s.open(ctx, card)
			}
			cancel()
			select {
			case s.results <- actionResult{card.ID, key}:
			case <-s.ctx.Done():
				return
			}
		}
	}
}

func (s *Service) run() {
	defer close(s.done)
	actionsDone := make(chan struct{})
	go s.runActions(actionsDone)
	defer func() { s.cancel(); <-actionsDone }()
	var events <-chan Event
	if s.presenter != nil {
		events = s.presenter.Events()
	}
	var sent uint64
	var retry time.Time
	failures := 0
	for {
		s.mu.Lock()
		s.queue.Advance(time.Now())
		snapshot := Snapshot{Revision: s.queue.revision, Cards: slices.Clone(s.queue.active), Theme: s.theme, Locale: s.locale}
		work := len(s.queue.active)+len(s.queue.pending) > 0
		apply := s.presenter != nil && failures < 5 && snapshot.Revision != sent && (work || sent != 0) && time.Now().After(retry)
		s.mu.Unlock()
		if apply {
			ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
			err := s.presenter.Apply(ctx, snapshot)
			cancel()
			s.mu.Lock()
			if err != nil {
				s.queue.Disconnected()
				s.state = "unavailable"
				failures++
				retry = time.Now().Add(time.Duration(min(60, 1<<min(failures, 6))) * time.Second)
			} else {
				sent = snapshot.Revision
				retry = time.Time{}
				s.state = "available"
			}
			s.mu.Unlock()
		}
		var tick <-chan time.Time
		var timer *time.Timer
		if work && failures < 5 {
			timer = time.NewTimer(50 * time.Millisecond)
			tick = timer.C
		}
		select {
		case <-s.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-s.wake:
		case <-tick:
		case result := <-s.results:
			s.mu.Lock()
			s.queue.ActionDone(result.id, result.key)
			s.mu.Unlock()
		case event := <-events:
			s.mu.Lock()
			if event.Kind == "disconnected" {
				if s.state != "unavailable" {
					failures++
					retry = time.Now().Add(time.Duration(min(60, 1<<min(failures, 6))) * time.Second)
				}
				s.queue.Disconnected()
				s.state = "unavailable"
			} else if event.Kind == "activate" {
				if card, ok := s.queue.Action(event.ID); ok {
					select {
					case s.actions <- card:
					default:
						s.queue.ActionDone(card.ID, "err.quickinput.surface_busy_retry")
					}
				}
			} else {
				if event.Kind == "entered" {
					failures = 0
				}
				s.queue.Event(event, time.Now())
			}
			s.mu.Unlock()
		}
		if timer != nil {
			timer.Stop()
		}
	}
}
