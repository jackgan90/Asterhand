package quickinput

// MenuLabel distinguishes a configured shortcut from an actually usable one.
func (s Status) MenuLabel() string {
	if !s.Supported {
		return "快捷输入 · 当前平台不支持"
	}
	if !s.Desired.Enabled {
		return "快捷输入 · 已禁用"
	}
	if s.Registration == "recovering" {
		return "快捷输入 · 正在恢复"
	}
	if s.Surface == "initializing" || s.Registration == "initializing" {
		return "快捷输入 · 正在初始化"
	}
	if s.Registration == "registered" && s.Surface == "ready" && s.Effective != nil {
		return "快捷输入 · " + s.Effective.String()
	}
	if s.Registration == "conflict" {
		return "快捷输入 · 快捷键冲突"
	}
	return "快捷输入 · 不可用，点击重试"
}
