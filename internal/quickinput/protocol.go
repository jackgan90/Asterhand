package quickinput

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type bridgeMessage struct {
	Version  int    `json:"version"`
	Kind     string `json:"kind"`
	Epoch    string `json:"generation"`
	Revision uint64 `json:"revision"`
	Text     string `json:"text"`
	ID       string `json:"id"`
	Height   int    `json:"height,omitempty"`
}

func decodeBridgeMessage(raw string) (bridgeMessage, error) {
	var message bridgeMessage
	if len(raw) > 128*1024 || !utf8.ValidString(raw) || !pairedSurrogates(raw) {
		return message, fmt.Errorf("桥消息大小或 Unicode 无效")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&message); err != nil {
		return message, err
	}
	if d.Decode(new(any)) != io.EOF || message.Version != 1 {
		return message, fmt.Errorf("桥消息格式或版本无效")
	}
	return message, nil
}

// encoding/json replaces isolated surrogate escapes with U+FFFD. Reject them
// before decoding so a malformed edit is never silently changed into a draft.
func pairedSurrogates(raw string) bool {
	return hostipc.PairedSurrogates(raw)
}
