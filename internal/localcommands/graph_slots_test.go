package localcommands

import "testing"

func TestHumanAndAutomationShareAllGraphSlots(t *testing.T) {
	m := &RunManager{graphSlots: make(chan struct{}, GraphExecutionSlots)}
	for range GraphExecutionSlots {
		if !m.reserveGraphSlot(nil) {
			t.Fatal("lost slot")
		}
	}
	if m.reserveGraphSlot(nil) {
		t.Fatal("exceeded capacity")
	}
	for range GraphExecutionSlots {
		m.releaseGraphSlot(nil)
	}
}
