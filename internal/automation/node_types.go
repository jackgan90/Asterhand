package automation

type NodeGet struct {
	ID string `json:"target_node_id"`
}
type NodeList struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type NodeStop struct {
	Instance   string `json:"instance_id"`
	StopActive bool   `json:"stop_active_work,omitempty"`
}
type DiagnosticRun struct {
	Kind string `json:"kind" description:"model_tool performs the existing model/tool probe and may consume provider quota"`
}
type DiagnosticInspect struct {
	ID string `json:"diagnostic_id,omitempty"`
}
type DesktopOpen struct {
	View    string `json:"view" description:"settings or logs"`
	Command string `json:"command_id,omitempty"`
}
