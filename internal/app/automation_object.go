package app

import (
	"encoding/json"
	"strconv"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/operations"
)

// JSON escaping can make a legal UI document larger than the public response
// budget. Explicit fragments preserve every byte without truncating its fields.
func automationObject(value any, format, cursor string, binding any) (automation.Outcome, error) {
	if format == "" || format == "object" {
		if cursor != "" {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Object reads do not use a cursor")
		}
		return automation.Outcome{Data: value}, nil
	}
	if format != "json_fragments" {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "Unknown object format")
	}
	binding = []any{binding, operations.Hash(value)}
	p, err := automation.ParseCursor(cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return automation.Outcome{}, err
	}
	offset, err := pageOffset(p.After, len(b))
	if err != nil {
		return automation.Outcome{}, err
	}
	text, end, err := textFragment(string(b), offset, 8192)
	if err != nil {
		return automation.Outcome{}, err
	}
	next := ""
	if end < len(b) {
		next = automation.EncodeCursor(binding, "", strconv.Itoa(end))
	}
	return automation.Outcome{Data: map[string]any{"format": "json_fragments", "text": text, "offset": strconv.Itoa(offset), "complete": end == len(b), "next": next}}, nil
}
