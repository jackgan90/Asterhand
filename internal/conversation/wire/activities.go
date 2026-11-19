package wire

// These projections contain no native transcript, runtime paths or credentials.
type ToolStep struct {
	Text   string `json:"step"`
	Status string `json:"status"`
}
type ToolApproval struct {
	ID             string   `json:"id"`
	Status         string   `json:"status"`
	Kind           string   `json:"kind"`
	Target         string   `json:"target,omitempty"`
	Targets        []string `json:"targets,omitempty"`
	Reason         string   `json:"reason"`
	CommandPreview string   `json:"command_preview,omitempty"`
	CanSession     bool     `json:"can_allow_session,omitempty"`
	CanAlways      bool     `json:"can_allow_always,omitempty"`
}
type ToolActivity struct {
	ID           string        `json:"id"`
	Turn         string        `json:"turn_id"`
	Tool         string        `json:"tool"`
	Summary      string        `json:"summary,omitempty"`
	State        string        `json:"state"`
	Sequence     int64         `json:"sequence"`
	ApprovalMode string        `json:"approval_mode,omitempty"`
	Detail       string        `json:"detail,omitempty"`
	Progress     []ToolStep    `json:"progress,omitempty"`
	Approval     *ToolApproval `json:"approval,omitempty"`
}
type ToolActivityPage struct {
	Workspace     string         `json:"workspace_id"`
	Authorized    bool           `json:"authorized"`
	ExecutionMode string         `json:"execution_mode,omitempty"`
	Items         []ToolActivity `json:"items"`
	Next          int64          `json:"next"`
}
