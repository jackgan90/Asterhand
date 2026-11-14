package notifications

import (
	"math"
	"slices"
	"time"
)

// Queue has no timers, threads or platform calls. Its owner serializes access.
// The producer publishes each terminal run once; queued duplicates are also
// rejected, while UI retries use notification IDs rather than re-enqueueing.
type Queue struct {
	pending, active          []Card
	sequence, revision       uint64
	capacity                 int
	duration                 time.Duration
	available, paused, hover bool
	last, admitted           time.Time
}

func (q *Queue) Enqueue(c Completion, now time.Time) {
	if len(c.Name) > 64 || len(c.CommandID) > 64 || len(c.RunID) > 64 || len(c.Reason) > 64 ||
		c.CommandID == "" || c.RunID == "" || (c.Result != "succeeded" && c.Result != "failed") ||
		math.IsNaN(c.Elapsed) || math.IsInf(c.Elapsed, 0) || c.Elapsed < 0 {
		return
	}
	for _, list := range [][]Card{q.active, q.pending} {
		for _, item := range list {
			if item.RunID == c.RunID {
				return
			}
		}
	}
	q.sequence++
	item := Card{Completion: c, ID: q.sequence, Phase: "entering"}
	if item.Ended.IsZero() {
		item.Ended = now
	}
	details := 0
	for _, pending := range q.pending {
		if pending.Count == 0 {
			details++
		}
	}
	if details >= MaxPending {
		q.summarize(item)
	} else {
		q.pending = append(q.pending, item)
	}
	q.revision++
}

func (q *Queue) summarize(item Card) {
	count, failed := item.Count, item.Failed
	if count == 0 {
		count = 1
		if item.Result == "failed" {
			failed = 1
		}
	}
	for i := range q.pending {
		if q.pending[i].Count == 0 {
			continue
		}
		s := &q.pending[i]
		s.Count = min((1<<53)-1, s.Count+count)
		s.Failed = min(s.Count, s.Failed+failed)
		if item.ID < s.ID {
			s.ID = item.ID
			s.Ended = item.Ended
		}
		return
	}
	q.pending = append(q.pending, Card{ID: item.ID, Phase: "entering", Count: count, Failed: failed,
		Completion: Completion{Ended: item.Ended}})
}

func (q *Queue) expire(now time.Time) {
	retained := make([]Card, 0, len(q.pending))
	expired := []Card{}
	for _, item := range q.pending {
		if item.Count == 0 && now.Sub(item.Ended) >= MaxAge {
			expired = append(expired, item)
		} else {
			retained = append(retained, item)
		}
	}
	if len(expired) == 0 {
		return
	}
	q.pending = retained
	for _, item := range expired {
		q.summarize(item)
	}
	slices.SortFunc(q.pending, func(a, b Card) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	q.revision++
}

func (q *Queue) Advance(now time.Time) {
	delta := now.Sub(q.last)
	q.last = now
	q.expire(now)
	// An unobserved suspend or a blocked display adapter must not consume a
	// whole visible interval at once when execution resumes.
	if delta < 0 || delta > time.Second {
		delta = 0
	}
	if !q.available || q.paused || q.hover {
		return
	}
	before := len(q.active)
	q.active = slices.DeleteFunc(q.active, func(c Card) bool { return c.exited })
	if len(q.active) != before {
		q.revision++
	}
	for i := range q.active {
		item := &q.active[i]
		if i >= q.capacity || item.Phase != "visible" || item.Opening {
			continue
		}
		item.remaining -= delta
		if item.remaining <= 0 {
			item.Phase = "exiting"
			q.revision++
		}
	}
	if len(q.active) >= q.capacity || len(q.pending) == 0 || now.Sub(q.admitted) < AdmissionGap {
		return
	}
	item := q.pending[0]
	q.pending = slices.Delete(q.pending, 0, 1)
	item.remaining = q.duration
	q.active = append(q.active, item)
	q.admitted = now
	q.revision++
}

func (q *Queue) Event(e Event, now time.Time) {
	q.Advance(now)
	if e.Kind == "environment" {
		q.available = true
		q.capacity = max(0, min(MaxVisible, e.Capacity))
		q.paused = e.Paused
		q.duration = max(DefaultDuration, time.Duration(e.DurationMS)*time.Millisecond)
		q.revision++
		return
	}
	if e.Kind == "hover" {
		if q.hover && !e.Paused {
			for i := range q.active {
				q.active[i].remaining = max(time.Second, q.active[i].remaining)
			}
		}
		q.hover = e.Paused
		return
	}
	for i := range q.active {
		item := &q.active[i]
		if item.ID != e.ID {
			continue
		}
		switch e.Kind {
		case "entered":
			if item.Phase == "entering" {
				item.Phase = "visible"
				q.revision++
			}
		case "dismiss":
			if !item.Opening {
				item.Phase = "exiting"
				q.revision++
			}
		case "exited":
			if item.Phase == "exiting" {
				item.exited = true
			}
		}
		return
	}
}

func (q *Queue) Disconnected() {
	q.available = false
	q.hover = false
	q.revision++
	// A lost closing acknowledgement cannot keep a slot forever. Remaining
	// cards retain identity/time and restore without a second entrance.
	q.active = slices.DeleteFunc(q.active, func(c Card) bool { return c.Phase == "exiting" })
}

func (q *Queue) Action(id uint64) (Card, bool) {
	for i := range q.active {
		item := &q.active[i]
		if item.ID == id && item.Phase != "exiting" && !item.Opening {
			item.Opening = true
			item.ErrorKey = ""
			q.revision++
			return *item, true
		}
	}
	return Card{}, false
}

func (q *Queue) ActionDone(id uint64, key string) {
	for i := range q.active {
		item := &q.active[i]
		if item.ID != id {
			continue
		}
		item.Opening = false
		item.ErrorKey = key
		item.remaining = q.duration
		if key == "" {
			item.Phase = "exiting"
		}
		q.revision++
	}
}
