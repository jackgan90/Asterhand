package automation

type RunList struct {
	CommandID string `json:"command_id" description:"Command ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	Cursor    string `json:"cursor,omitempty" description:"Opaque pagination cursor from a previous runs.list response"`
	Limit     int    `json:"limit,omitempty" description:"Page size (1..25 effective; 0 uses default)"`
}

type LogRead struct {
	CommandID string `json:"command_id" description:"Command ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	RunID     string `json:"run_id" description:"Run ID matching ^[a-zA-Z0-9_-]{16,64}$"`
	StepID    string `json:"step_id,omitempty" description:"Optional script step ID (^[a-zA-Z0-9_-]{16,64}$) to read a single step's output; omit for merged run output"`
	Stream    string `json:"stream,omitempty" description:"Optional 'stdout' or 'stderr'; omit for both streams"`
	Format    string `json:"format,omitempty" description:"'text' (default, decoded chunks) or 'raw' (base64 bytes)"`
	Encoding  string `json:"encoding,omitempty" description:"Text decoding: 'utf-8' (default), 'gbk', 'gb18030', 'big5', or 'utf-16le'"`
	Cursor    string `json:"cursor,omitempty" description:"Opaque cursor from previous next/previous response (omit on logs.tail)"`
	Seconds   int    `json:"wait_seconds,omitempty" description:"For logs.wait only: max wait seconds (1..30; 0 defaults to 30)"`
}
