package automation

type RunList struct {
	CommandID string `json:"command_id"`
	Cursor    string `json:"cursor,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type LogRead struct {
	CommandID string `json:"command_id"`
	RunID     string `json:"run_id"`
	StepID    string `json:"step_id,omitempty"`
	Stream    string `json:"stream,omitempty"`
	Format    string `json:"format,omitempty"`
	Encoding  string `json:"encoding,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Seconds   int    `json:"wait_seconds,omitempty"`
}
