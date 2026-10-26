package quickinput

import (
	"sync"

	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

// editorRelay coalesces an editor's full-text updates while one parent ACK is
// outstanding. Snapshot completion waits for the relevant edit to be accepted.
type editorRelay struct {
	mu                            sync.Mutex
	epoch, acknowledged, revision uint64
	inflight, latest              *hostipc.Message
	waiters                       map[string]editWaiter
	send                          func(hostipc.Message) error
	fail                          func(error)
}

type editWaiter struct {
	sequence uint64
	complete func()
}

func newEditorRelay(epoch, revision uint64, send func(hostipc.Message) error, fail func(error)) *editorRelay {
	return &editorRelay{epoch: epoch, acknowledged: revision, revision: revision, waiters: map[string]editWaiter{}, send: send, fail: fail}
}

func (r *editorRelay) submit(text string, sequence uint64, snapshotID string, complete func()) {
	r.mu.Lock()
	var outgoing *hostipc.Message
	if sequence > r.acknowledged {
		m := hostipc.Message{Kind: hostipc.Edit, Text: text, Sequence: sequence, Epoch: r.epoch}
		if r.inflight == nil {
			r.inflight, outgoing = &m, &m
		} else if sequence > r.inflight.Sequence {
			r.latest = &m
		}
	}
	if snapshotID != "" && sequence > r.acknowledged {
		r.waiters[snapshotID] = editWaiter{sequence, complete}
		complete = nil
	}
	r.mu.Unlock()
	if outgoing != nil {
		if err := r.send(*outgoing); err != nil {
			r.fail(err)
		}
	}
	if complete != nil {
		complete()
	}
}

func (r *editorRelay) accept(m hostipc.Message) error {
	r.mu.Lock()
	if r.inflight == nil || m.Sequence != r.inflight.Sequence || m.Epoch != r.epoch || m.Revision < r.revision {
		r.mu.Unlock()
		return hostipc.ErrProtocol
	}
	r.acknowledged, r.revision = m.Sequence, m.Revision
	r.inflight, r.latest = r.latest, nil
	outgoing := r.inflight
	var completed []func()
	for id, waiter := range r.waiters {
		if waiter.sequence <= r.acknowledged {
			completed = append(completed, waiter.complete)
			delete(r.waiters, id)
		}
	}
	r.mu.Unlock()
	if outgoing != nil {
		if err := r.send(*outgoing); err != nil {
			return err
		}
	}
	for _, complete := range completed {
		complete()
	}
	return nil
}

func (r *editorRelay) cancelSnapshot(id string) { r.mu.Lock(); delete(r.waiters, id); r.mu.Unlock() }
func (r *editorRelay) state() (epoch, sequence, revision uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.epoch, r.acknowledged, r.revision
}
