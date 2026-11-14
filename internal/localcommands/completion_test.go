package localcommands

import (
	"testing"
	"time"
)

func TestCompletionPublishesOnlyFreshPersistedFinalRuns(t *testing.T) {
	for _, scenario := range []string{"success", "failure", "stop", "off"} {
		t.Run(scenario, func(t *testing.T) {
			_, m, b, _, c := managedFixture(t)
			observed := make(chan Completion, 4)
			m.SetCompletionObserver(func(e Completion) { observed <- e })
			if scenario == "off" {
				c.NotifyOnCompletion = NotificationOff
			}
			accepted, err := m.Start(c)
			if err != nil {
				t.Fatal(err)
			}
			e := <-b.created
			if scenario == "failure" {
				e.code = 2
			}
			if scenario == "stop" {
				if _, err = m.Stop(c.ID, accepted.ID, "user_stop"); err != nil {
					t.Fatal(err)
				}
			} else {
				e.complete()
			}
			record := awaitFinished(t, m, c.ID)
			if scenario == "success" || scenario == "failure" {
				select {
				case event := <-observed:
					if event.RunID != record.ID || event.CommandID != c.ID || event.Result != record.Result {
						t.Fatal(event)
					}
				case <-time.After(time.Second):
					t.Fatal("missing completion")
				}
			}
			// Status reads and repeated finalization cannot replay the completion fact.
			for range 3 {
				m.Current(c.ID)
			}
			select {
			case event := <-observed:
				t.Fatal("unexpected completion", event)
			default:
			}
		})
	}
}
