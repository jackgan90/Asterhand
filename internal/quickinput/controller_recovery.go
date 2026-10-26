package quickinput

import (
	"time"
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
	c.status.Error, c.status.ErrorCode = "输入界面中断，正在恢复", "host_recovering"
	c.status.RecoveryNotice = "输入界面曾中断，已恢复主进程最后接收的草稿；尚未送达的输入或未确认候选可能丢失。"
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
	c.status.Error, c.status.ErrorCode = "", ""
	c.mu.Unlock()
	applyActivation(intent, surface)
}
