// Package desktop contains the narrow OS boundary for the node's desktop UI.
// It owns no task state and never uses a remote control endpoint to stop a node.
package desktop

type State struct {
	Name     string
	Ready    bool
	Stopping bool
}

type Options struct {
	State        func() State
	OpenWeb      func() error
	LogDirectory string
	Stop         func(confirmed bool) bool
	CleanupDone  <-chan struct{}
}

func (s State) text() string {
	if s.Stopping {
		return "正在退出"
	}
	if !s.Ready {
		return "待配置"
	}
	return "助手就绪"
}
