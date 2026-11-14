package quickinput

import (
	"context"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/notifications"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

// notificationHost serializes process generations. Retiring a process completes
// before a replacement can be started, including after an initialization timeout.
type notificationHost struct {
	mu      sync.Mutex
	home    string
	launch  func() (hostProcess, error)
	process hostProcess
	session string
	ready   chan struct{}
	failed  chan struct{}
	out     chan hostipc.Message
	events  chan notifications.Event
	closed  bool
	workers sync.WaitGroup
}

func newNotificationHost(home string, launch func() (hostProcess, error)) *notificationHost {
	return &notificationHost{home: home, launch: launch, events: make(chan notifications.Event, 32)}
}
func (h *notificationHost) Events() <-chan notifications.Event { return h.events }

func (h *notificationHost) Apply(ctx context.Context, snapshot notifications.Snapshot) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return hostipc.ErrProtocol
	}
	if h.process != nil {
		select {
		case <-h.failed:
			if err := h.retire(); err != nil {
				return err
			}
		default:
		}
	}
	if h.process == nil {
		p, err := h.launch()
		if err != nil {
			return err
		}
		h.process = p
		h.session = model.ID()
		h.ready = make(chan struct{})
		h.failed = make(chan struct{})
		h.out = make(chan hostipc.Message, 8)
		failed := h.failed
		var once sync.Once
		fail := func() {
			once.Do(func() {
				close(failed)
				select {
				case h.events <- notifications.Event{Kind: "disconnected"}:
				default:
				}
			})
		}
		h.workers.Add(2)
		go h.write(p, h.session, h.out, failed, fail)
		go h.read(p, h.session, h.ready, failed, fail)
		h.out <- hostipc.Message{Kind: hostipc.Initialize, Home: h.home, Theme: snapshot.Theme, Locale: snapshot.Locale,
			Epoch: 1, AppVersion: config.Version, Source: config.SourceDigest}
	}
	select {
	case <-h.ready:
	case <-h.failed:
		_ = h.retire()
		return hostipc.ErrProtocol
	case <-ctx.Done():
		_ = h.retire()
		return ctx.Err()
	}
	select {
	case h.out <- hostipc.Message{Kind: hostipc.NotificationFrame, Notifications: &snapshot}:
		return nil
	case <-h.failed:
		return hostipc.ErrProtocol
	case <-ctx.Done():
		return ctx.Err()
	default:
		return hostipc.ErrProtocol
	}
}

func (h *notificationHost) write(p hostProcess, session string, out <-chan hostipc.Message, failed <-chan struct{}, fail func()) {
	defer h.workers.Done()
	defer fail()
	for {
		select {
		case <-p.Exited():
			return
		case <-failed:
			return
		case m := <-out:
			m.Version, m.Session = hostipc.Version, session
			if hostipc.Write(p.Input(), m, false) != nil {
				return
			}
		}
	}
}
func (h *notificationHost) read(p hostProcess, session string, ready chan struct{}, failed <-chan struct{}, fail func()) {
	defer h.workers.Done()
	defer fail()
	initialized := false
	for {
		m, err := hostipc.Read(p.Output(), true)
		if err != nil || m.Session != session {
			return
		}
		if m.Kind == hostipc.Ready && !initialized && m.AppVersion == config.Version && m.Source == config.SourceDigest {
			initialized = true
			close(ready)
			continue
		}
		if m.Kind != hostipc.NotificationEvent || !initialized {
			return
		}
		select {
		case h.events <- *m.Notification:
		case <-failed:
			return
		case <-p.Exited():
			return
		}
	}
}
func (h *notificationHost) retire() error {
	if h.process == nil {
		return nil
	}
	err := h.process.Stop()
	if err == nil {
		h.workers.Wait()
		h.process = nil
		// No events from a retired generation may affect restored cards.
		for {
			select {
			case <-h.events:
			default:
				return nil
			}
		}
	}
	return err
}
func (h *notificationHost) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	if h.process != nil {
		select {
		case h.out <- hostipc.Message{Kind: hostipc.Quit}:
		default:
		}
		select {
		case <-h.process.Exited():
		case <-time.After(time.Second):
		}
	}
	return h.retire()
}
