package hostipc

import "github.com/jackgan90/Asterhand/internal/editors"

type EditorRequest struct {
	Node    string          `json:"node_id"`
	Command editors.Command `json:"request"`
}

func (v EditorRequest) Valid() bool {
	command := v.Command
	if v.Node == "" || len(v.Node) > 128 || command.ID == "" || len(command.ID) > 160 {
		return false
	}
	switch command.Action {
	case "list":
		return command.Editor == "" && len(command.Content) == 0
	case "get", "commit", "update":
		return command.Editor != "" && len(command.Editor) <= 64 && len(command.Content) <= 100<<10
	default:
		return false
	}
}
