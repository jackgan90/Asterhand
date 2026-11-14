package notifications

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func completion(i int, now time.Time) Completion {
	return Completion{CommandID: "command-identifier", RunID: fmt.Sprintf("run-identifier-%05d", i), Name: "build", Result: "succeeded", Started: now, Ended: now}
}
func tick(q *Queue, now *time.Time, duration time.Duration) {
	for duration > 0 {
		step := min(duration, 50*time.Millisecond)
		*now = now.Add(step)
		q.Advance(*now)
		duration -= step
	}
}
func TestQueueCapacityTimingHoverAndRecovery(t *testing.T) {
	now := time.Unix(1000, 0)
	q := &Queue{}
	q.Event(Event{Kind: "environment", Capacity: 3, DurationMS: 3000}, now)
	for i := range 10 {
		q.Enqueue(completion(i, now), now)
	}
	tick(q, &now, time.Second)
	if len(q.active) != 3 || len(q.pending) != 7 {
		t.Fatal(len(q.active), len(q.pending))
	}
	// Entrance time is excluded; duplicate acknowledgements cannot restart dwell.
	tick(q, &now, 4*time.Second)
	for _, c := range q.active {
		if c.Phase != "entering" {
			t.Fatal(c)
		}
	}
	for _, c := range q.active {
		q.Event(Event{Kind: "entered", ID: c.ID}, now)
	}
	tick(q, &now, 2*time.Second)
	q.Event(Event{Kind: "hover", Paused: true}, now)
	tick(q, &now, 10*time.Second)
	for _, c := range q.active {
		if c.Phase != "visible" {
			t.Fatal(c)
		}
	}
	q.Event(Event{Kind: "hover"}, now)
	q.Disconnected()
	tick(q, &now, time.Second)
	q.Event(Event{Kind: "environment", Capacity: 3, DurationMS: 3000}, now)
	tick(q, &now, 1100*time.Millisecond)
	for _, c := range q.active {
		if c.Phase != "exiting" {
			t.Fatal(c)
		}
	}
	if len(q.pending) != 7 {
		t.Fatal("released slots before hidden")
	}
	for _, c := range slices.Clone(q.active) {
		q.Event(Event{Kind: "exited", ID: c.ID}, now)
	}
	tick(q, &now, time.Second)
	if len(q.active) != 3 || len(q.pending) != 4 {
		t.Fatal(len(q.active), len(q.pending))
	}
}
func TestQueueOverflowExpiryAndSummaryOrder(t *testing.T) {
	now := time.Unix(1000, 0)
	q := &Queue{}
	for i := range 10000 {
		c := completion(i, now)
		if i%2 == 0 {
			c.Result = "failed"
		}
		q.Enqueue(c, now)
	}
	if len(q.pending) != 101 || q.pending[100].Count != 9900 || q.pending[100].Failed != 4950 {
		t.Fatal("unbounded or incorrect summary", len(q.pending))
	}
	q.Advance(now.Add(MaxAge))
	if len(q.pending) != 1 || q.pending[0].Count != 10000 || q.pending[0].Failed != 5000 || q.pending[0].ID != 1 {
		t.Fatal(q.pending)
	}
	q.Enqueue(completion(10001, now.Add(MaxAge)), now.Add(MaxAge))
	if q.pending[0].Count != 10000 {
		t.Fatal("new arrivals overtook summary")
	}
}
func TestQueueCapacityReductionPreservesHiddenTime(t *testing.T) {
	now := time.Unix(1000, 0)
	q := &Queue{}
	q.Event(Event{Kind: "environment", Capacity: 3, DurationMS: 3000}, now)
	for i := range 3 {
		q.Enqueue(completion(i, now), now)
	}
	tick(q, &now, time.Second)
	for _, c := range q.active {
		q.Event(Event{Kind: "entered", ID: c.ID}, now)
	}
	q.Event(Event{Kind: "environment", Capacity: 1, DurationMS: 3000}, now)
	tick(q, &now, 4*time.Second)
	if q.active[0].Phase != "exiting" || q.active[1].remaining != DefaultDuration || q.active[2].remaining != DefaultDuration {
		t.Fatal(q.active)
	}
}
func TestQueueActionAndStaleAcknowledgements(t *testing.T) {
	now := time.Unix(1000, 0)
	q := &Queue{}
	q.Event(Event{Kind: "environment", Capacity: 3, DurationMS: 3000}, now)
	q.Enqueue(completion(0, now), now)
	q.Enqueue(completion(0, now), now)
	q.Advance(now)
	id := q.active[0].ID
	q.Event(Event{Kind: "entered", ID: id}, now)
	if _, ok := q.Action(id); !ok {
		t.Fatal("missing action")
	}
	tick(q, &now, 10*time.Second)
	q.ActionDone(id, "err.localcmd.run_changed")
	if q.active[0].Phase != "visible" || q.active[0].ErrorKey == "" {
		t.Fatal(q.active)
	}
	q.Event(Event{Kind: "dismiss", ID: id}, now)
	q.Event(Event{Kind: "exited", ID: id}, now)
	q.Advance(now)
	q.Event(Event{Kind: "entered", ID: id}, now)
	if len(q.active) != 0 || len(q.pending) != 0 {
		t.Fatal("stale event resurrected card")
	}
}
