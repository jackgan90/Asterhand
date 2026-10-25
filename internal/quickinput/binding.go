// Package quickinput owns local desktop preferences and the bounded text surface.
// It has no task, executor, credential or peer state.
package quickinput

import (
	"fmt"
	"strings"
)

type Binding struct {
	Modifiers []string `json:"modifiers"`
	Key       string   `json:"key"`
}

func DefaultBinding() Binding { return Binding{[]string{"ctrl", "alt"}, "KeyJ"} }

func (b Binding) Normalize() (Binding, error) {
	seen := map[string]bool{}
	for _, modifier := range b.Modifiers {
		m := strings.ToLower(strings.TrimSpace(modifier))
		if m != "ctrl" && m != "alt" && m != "shift" {
			return Binding{}, fmt.Errorf("快捷键只支持 Ctrl、Alt 和 Shift 修饰键")
		}
		seen[m] = true
	}
	if !seen["ctrl"] && !seen["alt"] {
		return Binding{}, fmt.Errorf("快捷键至少包含 Ctrl 或 Alt")
	}
	result := Binding{Modifiers: []string{}, Key: b.Key}
	for _, m := range []string{"ctrl", "alt", "shift"} {
		if seen[m] {
			result.Modifiers = append(result.Modifiers, m)
		}
	}
	if _, ok := keyCodes[b.Key]; !ok {
		return Binding{}, fmt.Errorf("不支持该按键，请选择字母、数字或命名键（F12/Win/Fn 除外）")
	}
	if _, punctuation := keyLabels[b.Key]; punctuation {
		return Binding{}, fmt.Errorf("首版请使用字母、数字或命名键；标点按键的跨布局映射暂不支持")
	}
	if (seen["ctrl"] && seen["alt"] && b.Key == "Delete") || (seen["alt"] && b.Key == "Tab") || (seen["alt"] && b.Key == "F4") {
		return Binding{}, fmt.Errorf("该组合为系统保留快捷键")
	}
	return result, nil
}

func (b Binding) String() string {
	parts := make([]string, 0, len(b.Modifiers)+1)
	for _, m := range b.Modifiers {
		parts = append(parts, map[string]string{"ctrl": "Ctrl", "alt": "Alt", "shift": "Shift"}[m])
	}
	key := strings.TrimPrefix(strings.TrimPrefix(b.Key, "Key"), "Digit")
	if label, ok := keyLabels[b.Key]; ok {
		key = label
	}
	return strings.Join(append(parts, key), "+")
}

func (b Binding) Equal(other Binding) bool {
	return b.Key == other.Key && strings.Join(b.Modifiers, "+") == strings.Join(other.Modifiers, "+")
}

var keyLabels = map[string]string{"Semicolon": ";", "Quote": "'", "BracketLeft": "[", "BracketRight": "]", "Backquote": "`", "Backslash": "\\", "Slash": "/", "Comma": ",", "Period": ".", "Minus": "-", "Equal": "="}
var keyCodes = func() map[string]uint32 {
	keys := map[string]uint32{"Space": 0x20, "Tab": 9, "Enter": 13, "Pause": 0x13, "Insert": 0x2d, "Delete": 0x2e, "Home": 0x24, "End": 0x23, "PageUp": 0x21, "PageDown": 0x22,
		"Semicolon": 0xba, "Equal": 0xbb, "Comma": 0xbc, "Minus": 0xbd, "Period": 0xbe, "Slash": 0xbf, "Backquote": 0xc0, "BracketLeft": 0xdb, "Backslash": 0xdc, "BracketRight": 0xdd, "Quote": 0xde}
	for c := 'A'; c <= 'Z'; c++ {
		keys["Key"+string(c)] = uint32(c)
	}
	for c := '0'; c <= '9'; c++ {
		keys["Digit"+string(c)] = uint32(c)
	}
	for i := 1; i <= 11; i++ {
		keys[fmt.Sprintf("F%d", i)] = uint32(0x6f + i)
	}
	return keys
}()

func (b Binding) WindowsKeys() (uint32, uint32, error) {
	normal, err := b.Normalize()
	if err != nil {
		return 0, 0, err
	}
	var modifiers uint32
	for _, m := range normal.Modifiers {
		modifiers |= map[string]uint32{"alt": 1, "ctrl": 2, "shift": 4}[m]
	}
	return modifiers | 0x4000, keyCodes[normal.Key], nil
}
