package wire

// These projections contain no native transcript, runtime paths or credentials.
type ToolStep struct {
	Text   string `json:"step"`
	Status string `json:"status"`
}
type ToolActivity struct {
	ID       string     `json:"id"`
	Turn     string     `json:"turn_id"`
	Tool     string     `json:"tool"`
	State    string     `json:"state"`
	Sequence int64      `json:"sequence"`
	Progress []ToolStep `json:"progress,omitempty"`
}
type ToolActivityPage struct {
	Workspace  string         `json:"workspace_id"`
	Authorized bool           `json:"authorized"`
	Items      []ToolActivity `json:"items"`
	Next       int64          `json:"next"`
}
