package quickinput

import (
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/conversation/wire"
	"github.com/jackgan90/Asterhand/internal/i18n"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type bridgeMessage struct {
	Version  int                   `json:"version"`
	Kind     string                `json:"kind"`
	Epoch    string                `json:"generation"`
	Revision uint64                `json:"revision"`
	Text     string                `json:"text"`
	ID       string                `json:"id"`
	Height   int                   `json:"height,omitempty"`
	Chat     *wire.Request         `json:"chat,omitempty"`
	Command  *commandswire.Request `json:"command,omitempty"`
}

func decodeBridgeMessage(raw string) (bridgeMessage, error) {
	var message bridgeMessage
	if len(raw) > 128*1024 || !utf8.ValidString(raw) || !pairedSurrogates(raw) {
		return message, i18n.Err("err.quickinput.bridge_size_unicode")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&message); err != nil {
		return message, err
	}
	if d.Decode(new(any)) != io.EOF || message.Version != 1 {
		return message, i18n.Err("err.quickinput.bridge_schema")
	}
	if message.Command != nil && message.Kind != "command" || message.Kind == "command" && message.Chat != nil {
		return message, i18n.Err("err.quickinput.bridge_cmd_fields")
	}
	return message, nil
}

// encoding/json replaces isolated surrogate escapes with U+FFFD. Reject them
// before decoding so a malformed edit is never silently changed into a draft.
func pairedSurrogates(raw string) bool {
	return hostipc.PairedSurrogates(raw)
}
