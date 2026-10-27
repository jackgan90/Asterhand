package prompt

import (
	"strings"
	"testing"
)

func TestDefaultPolicyAndCapabilityNegotiation(t *testing.T) {
	model := Model{Provider: "fixture", Name: "fixture/chat"}
	caps := Capabilities{Tools: []string{}, History: true, Streaming: true, Cancellation: true}
	full := Default(Replace, model, caps)
	if full.Application.State != Pending || !strings.Contains(full.Text, "当前会话没有可调用工具") || !strings.Contains(full.Text, "fixture/chat") {
		t.Fatal("policy did not describe current facts")
	}
	if strings.Contains(full.Text, "你是 Asterhand") || strings.Contains(full.Text, "先给结论") ||
		!strings.Contains(full.Text, "保留原生") || !strings.Contains(full.Text, "仅在") ||
		!strings.HasPrefix(full.Application.Revision, "default-chat-v2-") {
		t.Fatal("capability adaptation replaced the native persona or lacks a scoped revision")
	}
	if Default(Replace, model, caps).Application.Revision != full.Application.Revision {
		t.Fatal("unstable policy revision")
	}
	changed := Default(Replace, Model{Name: "new-model"}, caps)
	if changed.Application.Revision == full.Application.Revision {
		t.Fatal("model facts did not change the policy revision")
	}
	for _, mode := range []Mode{Replace, Append, Unsupported, Mode("unknown")} {
		plan := Default(mode, model, caps)
		if mode == Replace || mode == Append {
			if plan.Application.State != Pending {
				t.Fatal("support was mistaken for application proof")
			}
		} else if plan.Application.Mode != Unsupported || plan.Application.State != Degraded {
			t.Fatal("missing override did not degrade")
		}
	}
	failed := full.Application.Fallback("unconfirmed")
	if failed.State != Degraded || failed.Mode != Replace {
		t.Fatal("failure lost supported capability")
	}
}
