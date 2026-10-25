package quickinput

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

func (c *Controller) Apply(ctx context.Context, change Change) (Status, error) {
	c.op.Lock()
	defer c.op.Unlock()
	if len(change.Operation) < 8 || len(change.Operation) > 100 {
		return c.Status(), &Error{400, "变更操作 ID 无效"}
	}
	encoded, _ := json.Marshal(change)
	if previous, ok := c.operations[change.Operation]; ok && time.Now().Before(previous.expires) {
		if previous.input != string(encoded) {
			return c.Status(), &Error{409, "该操作 ID 已用于其他变更"}
		}
		return previous.result, previous.err
	}
	result, err := c.apply(ctx, change)
	for key, entry := range c.operations {
		if time.Now().After(entry.expires) {
			delete(c.operations, key)
		}
	}
	if len(c.operations) >= 64 {
		var oldest string
		var expires time.Time
		for key, entry := range c.operations {
			if oldest == "" || entry.expires.Before(expires) {
				oldest, expires = key, entry.expires
			}
		}
		delete(c.operations, oldest)
	}
	c.operations[change.Operation] = operation{string(encoded), result, err, time.Now().Add(5 * time.Minute)}
	return result, err
}

func (c *Controller) apply(ctx context.Context, change Change) (Status, error) {
	before := c.Status()
	if c.isStopping() {
		return before, &Error{503, "节点正在退出"}
	}
	if !before.Supported || c.registrar == nil {
		return before, &Error{503, "当前没有可用的 Windows 交互桌面"}
	}
	if change.Revision != before.Desired.Revision {
		return before, &Error{409, "设置已被其他页面修改，请刷新后重试"}
	}
	if c.invalid && !change.Reset {
		return before, &Error{409, "桌面配置无效，请明确恢复默认"}
	}
	next := Preferences{1, model.ID(), change.Enabled, change.Binding, change.Theme}
	if change.Reset {
		next = Defaults()
	}
	next, err := next.validated()
	if err != nil {
		return before, &Error{400, err.Error()}
	}
	needsRegistration := !next.Binding.Equal(before.Desired.Binding) || (next.Enabled && c.active == 0)
	if err := c.cleanup(); err != nil && needsRegistration {
		return c.Status(), err
	}
	if !c.invalid && next.Enabled == before.Desired.Enabled && next.Binding.Equal(before.Desired.Binding) && next.Theme == before.Desired.Theme && (!next.Enabled || c.active != 0) {
		return c.Status(), nil
	}
	if err = ctx.Err(); err != nil {
		return before, err
	}
	if !next.Enabled && c.surface != nil {
		if err = c.surface.Snapshot(ctx); err != nil {
			return before, fmt.Errorf("请先完成输入或稍后重试：%w", err)
		}
	}
	if next.Enabled {
		wasAbsent := c.surface == nil
		if wasAbsent && !before.Desired.Enabled {
			defer func() {
				if c.Status().Desired.Revision == before.Desired.Revision {
					c.closeSurface()
				}
			}()
		}
		if _, err = c.ReadySurface(); err != nil {
			c.setError("unavailable", err)
			return c.Status(), err
		}
	}
	candidate := 0
	if needsRegistration {
		candidate, err = c.registrar.Register(next.Binding)
		if err != nil {
			if c.active == 0 {
				c.registrationFailed(err)
			}
			return c.Status(), err
		}
	}
	return c.persist(ctx, change, next, candidate)
}

func (c *Controller) persist(ctx context.Context, change Change, next Preferences, candidate int) (Status, error) {
	var err error
	if err = ctx.Err(); err == nil && c.isStopping() {
		err = &Error{503, "节点正在退出"}
	}
	if err == nil && c.invalid {
		err = c.store.PreserveInvalid()
	}
	if err == nil {
		err = c.store.Save(next)
	}
	if err != nil {
		if candidate != 0 {
			c.retire(candidate)
		}
		return c.Status(), fmt.Errorf("设置未提交，保留旧配置：%w", err)
	}
	// No context cancellation after this persistent commit point.
	c.invalid = false
	c.publish(change, next, candidate)
	return c.Status(), nil
}

func (c *Controller) publish(change Change, next Preferences, candidate int) {
	c.mu.Lock()
	old := c.active
	c.status.Desired, c.status.LastOperation, c.status.Error = next, change.Operation, ""
	c.status.ErrorCode = ""
	c.record = nil
	if next.Enabled {
		if candidate != 0 {
			c.active = candidate
		}
		b := next.Binding
		c.status.Effective = &b
		c.status.Registration = "registered"
	} else {
		c.active = 0
		c.status.Effective = nil
		c.status.Registration = "disabled"
	}
	c.mu.Unlock()
	if old != 0 && old != c.active {
		c.retire(old)
	}
	if !next.Enabled && candidate != 0 {
		c.retire(candidate)
	}
	if next.Enabled && c.surface != nil {
		if err := c.surface.Update(next.Theme); err != nil {
			c.Failed(err)
		}
	} else if c.surface != nil {
		c.closeSurface()
	}
}

func (c *Controller) Check(binding Binding) error {
	c.op.Lock()
	defer c.op.Unlock()
	if c.isStopping() || c.registrar == nil {
		return &Error{503, "桌面不可用或节点正在退出"}
	}
	if err := c.cleanup(); err != nil {
		return err
	}
	normal, err := binding.Normalize()
	if err != nil {
		return &Error{400, err.Error()}
	}
	if c.active != 0 && normal.Equal(c.Status().Desired.Binding) {
		return nil
	}
	id, err := c.registrar.Register(normal)
	if err != nil {
		return err
	}
	c.retire(id)
	if len(c.retired) != 0 {
		return &Error{409, "临时注册待清理，不能确认组合已释放"}
	}
	return nil
}
