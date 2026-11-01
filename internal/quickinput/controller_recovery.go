package quickinput

import (
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
)

const recoveryHealthyInterval = 60 * time.Second

// recoverSurface is called under op after checking the failed generation. Only
// the controller can replace a host, so settings/stop cannot create a second one.
func (c *Controller) recoverSurface(cause error) {
	before := c.Status()
	if c.surface == nil || !before.Desired.Enabled || c.closingFailed || (c.recoveryUsed && time.Since(c.readyAt) < recoveryHealthyInterval) {
		c.failSurface(cause)
		return
	}
	c.recoveryUsed = true
	c.mu.Lock()
	c.status.Registration, c.status.Surface = "recovering", "recovering"
	c.status.ErrorCode = "host_recovering"
	c.status.ErrorKey, c.status.ErrorArgs = "status.quickinput.host_recovering", nil
	c.status.Error = i18n.T(i18n.Effective(), "status.quickinput.host_recovering")
	c.status.RecoveryNoticeKey = "status.quickinput.recovery_host"
	c.status.RecoveryNotice = i18n.T(i18n.Effective(), "status.quickinput.recovery_host")
	c.mu.Unlock()
	c.closeSurface()
	if c.isStopping() {
		return
	}
	if c.closingFailed {
		c.failSurface(cause)
		return
	}
	surface, err := c.ReadySurface()
	if err != nil {
		c.failSurface(err)
		return
	}
	c.mu.Lock()
	intent := c.pendingActivation
	c.pendingActivation = nil
	c.status.Registration = before.Registration
	if c.active != 0 {
		c.status.Registration = "registered"
	}
	c.clearErrorLocked()
	c.mu.Unlock()
	applyActivation(intent, surface)
}
