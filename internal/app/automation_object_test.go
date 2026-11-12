package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/automation"
)

func TestLargeObjectFragmentsAreCompleteBoundedAndRevisionBound(t *testing.T) {
	value := map[string]any{"text": strings.Repeat("\x00<&中文🙂", 50000)}
	want, _ := json.Marshal(value)
	var got strings.Builder
	cursor := ""
	first := ""
	for i := 0; i < 1000; i++ {
		out, err := automationObject(value, "json_fragments", cursor, "original-revision")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out.Data)
		if len(b) > automation.MaxResponse {
			t.Fatal("fragment exceeded byte budget")
		}
		var page struct {
			Text, Next string
			Complete   bool
		}
		_ = json.Unmarshal(b, &page)
		got.WriteString(page.Text)
		if page.Complete {
			break
		}
		cursor = page.Next
		if first == "" {
			first = cursor
		}
	}
	if got.String() != string(want) {
		t.Fatal("large object was truncated or corrupted")
	}
	if _, err := automationObject(value, "json_fragments", first, "changed-revision"); err == nil {
		t.Fatal("mixed revisions")
	}
	value["text"] = "changed during lease renewal"
	if _, err := automationObject(value, "json_fragments", first, "original-revision"); err == nil {
		t.Fatal("mixed snapshots")
	}
}
