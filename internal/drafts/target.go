package drafts

type Target struct {
	Kind         string `json:"kind"`
	Node         string `json:"node_id"`
	Command      string `json:"command_id,omitempty"`
	Conversation string `json:"conversation_id,omitempty"`
	Role         string `json:"role_id,omitempty"`
	Project      string `json:"project_id,omitempty"`
	Task         string `json:"task_id,omitempty"`
	TaskKind     string `json:"task_kind,omitempty"`
	Settings     string `json:"settings,omitempty"`
	Interaction  string `json:"interaction_id,omitempty"`
	Attempt      string `json:"attempt,omitempty"`
	Baseline     string `json:"baseline_revision,omitempty"`
}
