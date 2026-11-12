package automation

import (
	"encoding/base64"
	"encoding/json"

	"github.com/jackgan90/Asterhand/internal/operations"
)

type Cursor struct {
	Binding  string `json:"binding"`
	Revision string `json:"revision,omitempty"`
	After    string `json:"after"`
}

func EncodeCursor(binding any, revision, after string) string {
	if after == "" {
		return ""
	}
	b, _ := json.Marshal(Cursor{Binding: operations.Hash(binding), Revision: revision, After: after})
	return base64.RawURLEncoding.EncodeToString(b)
}

func ParseCursor(raw string, binding any) (Cursor, error) {
	if raw == "" {
		return Cursor{}, nil
	}
	if len(raw) > 24*1024 {
		return Cursor{}, Fail("invalid_cursor", "Cursor is too long")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, Fail("invalid_cursor", "Cursor is malformed")
	}
	c, err := Decode[Cursor](b)
	if err != nil || c.Binding != operations.Hash(binding) {
		return Cursor{}, Fail("invalid_cursor", "Cursor does not match this query and authorization")
	}
	return c, nil
}

func PageLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > 200 {
		return 0, Fail("invalid_parameters", "Page limit must be between 1 and 200")
	}
	return limit, nil
}
