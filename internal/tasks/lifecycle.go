package tasks

// BeginStop closes task admission and scheduling under the same lock used to
// register attempts. An idle tray click cannot race a newly started attempt.
func (e *Engine) BeginStop(confirmed bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !confirmed && len(e.active) != 0 {
		return false
	}
	e.stopping = true
	return true
}
