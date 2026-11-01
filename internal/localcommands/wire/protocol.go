// Package wire contains the closed local-command capability of the input host.
package wire

import (
	"fmt"
	"regexp"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,64}$`)

func ValidID(id string) bool { return identifier.MatchString(id) }

type Request struct {
	Action   string `json:"action"`
	ID       string `json:"command_id,omitempty"`
	Revision string `json:"revision,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	Catalog  string `json:"catalog,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
}

func (r Request) Validate() error {
	valid := false
	switch r.Action {
	case "list":
		valid = r.ID == "" && r.Revision == "" && r.Sequence == 0 && r.Offset >= 0 && r.Offset <= 10000 && (r.Catalog == "" || ValidID(r.Catalog))
	case "launch":
		valid = ValidID(r.ID) && ValidID(r.Revision) && r.Sequence > 0 && r.Offset == 0 && r.Catalog == ""
	case "settings":
		valid = (r.ID == "" || ValidID(r.ID)) && r.Revision == "" && r.Sequence == 0 && r.Offset == 0 && r.Catalog == ""
	}
	if !valid {
		return fmt.Errorf("本地命令请求无效")
	}
	return nil
}

type Entry struct {
	ID          string   `json:"id"`
	Revision    string   `json:"revision"`
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Detail      string   `json:"detail,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

type Response struct {
	Entries []Entry `json:"entries,omitempty"`
	Catalog string  `json:"catalog,omitempty"`
	Next    int     `json:"next,omitempty"`
	Started bool    `json:"started,omitempty"`
	Error   *Error  `json:"error,omitempty"`
}
