// Package editors routes a request to the window which owns the live editor.
// Registration contains no draft text. A closed window cannot be replaced by
// a new window with the same business target.
package editors

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

const maxPages = 32

type Command struct {
	ID        string          `json:"id"`
	Action    string          `json:"action"`
	Editor    string          `json:"editor,omitempty"`
	Revision  string          `json:"expected_revision,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Operation string          `json:"operation_id,omitempty"`
}
type Reply struct {
	ID    string          `json:"id"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}
type Poll struct {
	Page  string `json:"page"`
	Reply *Reply `json:"reply,omitempty"`
}
type work struct {
	command   Command
	result    chan Reply
	delivered bool
	check     func() error
}
type page struct {
	queue   chan *work
	kind    string
	streams int
}
type Broker struct {
	mu      sync.Mutex
	pages   map[string]*page
	pending map[string]*work
	done    chan struct{}
	once    sync.Once
}

func New() *Broker {
	return &Broker{pages: make(map[string]*page), pending: make(map[string]*work), done: make(chan struct{})}
}
func (b *Broker) Close() { b.once.Do(func() { close(b.done) }) }
func (b *Broker) Pages() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.pages))
	for id := range b.pages {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func validPage(id string) bool {
	return len(id) >= 16 && len(id) <= 64 && !strings.ContainsAny(id, ":/\\\x00")
}

func (b *Broker) Call(ctx context.Context, id string, c Command, check func() error) (json.RawMessage, error) {
	b.mu.Lock()
	p := b.pages[id]
	if p == nil {
		b.mu.Unlock()
		return nil, errors.New("editor_closed")
	}
	c.ID = id + ":" + model.ID()
	w := &work{command: c, result: make(chan Reply, 1), check: check}
	select {
	case p.queue <- w:
		b.pending[c.ID] = w
	default:
		b.mu.Unlock()
		return nil, errors.New("busy")
	}
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.pending, c.ID); b.mu.Unlock() }()
	limit := 6 * time.Second
	if c.Action == "commit" {
		limit = 25 * time.Second
	}
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case r := <-w.result:
		if r.Error != "" {
			return nil, errors.New(r.Error)
		}
		return r.Data, nil
	case <-ctx.Done():
	case <-timer.C:
	case <-b.done:
	}
	b.mu.Lock()
	delivered := w.delivered
	delete(b.pending, c.ID)
	b.mu.Unlock()
	if delivered && (c.Action == "commit" || c.Action == "update") {
		return nil, errors.New("result_unknown")
	}
	return nil, errors.New("editor_closed")
}

// Commands registers a live transport. An idle window sends no contents.
func (b *Broker) Commands(ctx context.Context, kind, id string) <-chan Command {
	if !validPage(id) {
		return nil
	}
	b.mu.Lock()
	p := b.pages[id]
	if p == nil {
		if len(b.pages) >= maxPages {
			b.mu.Unlock()
			return nil
		}
		p = &page{queue: make(chan *work, 4), kind: kind}
		b.pages[id] = p
	}
	if p.kind != kind {
		b.mu.Unlock()
		return nil
	}
	p.streams++
	b.mu.Unlock()
	out := make(chan Command)
	go func() {
		defer close(out)
		defer func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			p.streams--
			if p.streams != 0 || b.pages[id] != p {
				return
			}
			delete(b.pages, id)
			for key, w := range b.pending {
				if !strings.HasPrefix(key, id+":") {
					continue
				}
				code := "editor_closed"
				if w.delivered && (w.command.Action == "commit" || w.command.Action == "update") {
					code = "result_unknown"
				}
				select {
				case w.result <- Reply{Error: code}:
				default:
				}
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-b.done:
				return
			case w := <-p.queue:
				b.mu.Lock()
				if b.pending[w.command.ID] != w {
					b.mu.Unlock()
					continue
				}
				if w.check != nil && w.check() != nil {
					w.result <- Reply{Error: "forbidden"}
					b.mu.Unlock()
					continue
				}
				w.delivered = true
				b.mu.Unlock()
				select {
				case out <- w.command:
				case <-ctx.Done():
					return
				case <-b.done:
					return
				}
			}
		}
	}()
	return out
}
func (b *Broker) Respond(kind string, input Poll) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.pages[input.Page]
	r := input.Reply
	if p == nil || p.kind != kind || r == nil || !strings.HasPrefix(r.ID, input.Page+":") {
		return errors.New("invalid_parameters")
	}
	if w := b.pending[r.ID]; w != nil && w.delivered {
		select {
		case w.result <- *r:
		default:
		}
	}
	return nil
}
