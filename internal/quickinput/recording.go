package quickinput

import (
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

type recording struct {
	ID      string    `json:"recording_id"`
	Expires time.Time `json:"expires"`
	Binding *Binding  `json:"binding,omitempty"`
	owner   string
}

func (c *Controller) StartRecording(owner string, sessionExpires time.Time) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stopping || !c.status.Supported || c.status.CleanupPending != 0 {
		return nil, &Error{409, "当前无法录制快捷键"}
	}
	if c.record != nil && time.Now().Before(c.record.Expires) {
		return nil, &Error{409, "另一个页面正在录制快捷键"}
	}
	expires := time.Now().Add(30 * time.Second)
	if sessionExpires.Before(expires) {
		expires = sessionExpires
	}
	c.record = &recording{ID: model.ID(), Expires: expires, owner: owner}
	return *c.record, nil
}

func (c *Controller) Recording(id, owner string, cancel bool) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.record == nil || c.record.ID != id || c.record.owner != owner || time.Now().After(c.record.Expires) {
		return nil, &Error{404, "录制已结束，请重新开始"}
	}
	result := *c.record
	if cancel {
		c.record = nil
	}
	return result, nil
}
