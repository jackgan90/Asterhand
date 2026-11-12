package automation

type TaskSubmit struct {
	Target      string `json:"target_node_id"`
	Project     string `json:"project_id"`
	Goal        string `json:"goal"`
	StartBefore string `json:"start_before,omitempty" description:"RFC3339 deadline; default is operation creation time plus two hours"`
}
type TaskRef struct {
	Kind    string `json:"kind" description:"job or outbox"`
	ID      string `json:"id"`
	Project string `json:"project_id"`
	Target  string `json:"target_node_id"`
}
type TaskList struct {
	Kind   string `json:"kind,omitempty" description:"job (default) or outbox"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type TaskGet struct {
	Task   TaskRef `json:"task"`
	Part   string  `json:"part,omitempty" description:"summary (default), goal, result, error, question, answer, or artifacts"`
	Cursor string  `json:"cursor,omitempty"`
}
type TaskAnswer struct {
	Task        TaskRef `json:"task"`
	Attempt     string  `json:"attempt"`
	Interaction string  `json:"interaction_id"`
	Answer      string  `json:"answer"`
}
type TaskEvents struct {
	Task   TaskRef `json:"task"`
	After  string  `json:"after_sequence,omitempty"`
	Cursor string  `json:"cursor,omitempty"`
}
type TaskResolve struct {
	Task    TaskRef `json:"task"`
	Attempt string  `json:"attempt,omitempty"`
	Reason  string  `json:"reason"`
}
