package quickinput

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
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
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		value, err := strconv.ParseUint(raw[i+1:i+5], 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value < 0xd800 || value > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1:i+3] != `\u` {
			return false
		}
		low, err := strconv.ParseUint(raw[i+3:i+7], 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
