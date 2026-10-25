package quickinput

import (
	"strings"
	"testing"
)

func TestBridgeJSONBoundsAndUnicode(t *testing.T) {
	for _, raw := range []string{`{"version":1,"text":"\ud83d\ude00"}`, `{"version":1,"text":"\\ud800"}`, `{"version":1,"text":"中文😀"}`} {
		if _, err := decodeBridgeMessage(raw); err != nil {
			t.Fatal("valid message rejected", raw, err)
		}
	}
	for _, raw := range []string{`{"version":1,"text":"\ud800"}`, `{"version":1,"text":"\udc00"}`, `{"version":1,"text":"\ud800\u0041"}`, `{"version":1,"unknown":1}`, `{"version":2}`, `{"version":1} {}`, strings.Repeat(" ", 128*1024+1)} {
		if _, err := decodeBridgeMessage(raw); err == nil {
			t.Fatal("malformed message accepted", raw[:min(len(raw), 80)])
		}
	}
}
