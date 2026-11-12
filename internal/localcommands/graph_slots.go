package localcommands

// All callers use the same execution capacity.
func (m *RunManager) reserveGraphSlot(_ *managedRun) bool {
	select {
	case m.graphSlots <- struct{}{}:
		return true
	default:
		return false
	}
}
func (m *RunManager) releaseGraphSlot(_ *managedRun) { <-m.graphSlots }
