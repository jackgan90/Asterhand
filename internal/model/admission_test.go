package model

import (
	"encoding/json"
	"os"
	"testing"
)

func TestTaskRejectionDigestMatchesWebContract(t *testing.T) {
	data, err := os.ReadFile("../../tests/fixtures/task-rejection-requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Local   string  `json:"local_node_id"`
		Request Request `json:"request"`
		Digest  string  `json:"digest"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		r := tc.Request
		r.Mode = "route"
		if r.Target == "" {
			r.Target = tc.Local
		}
		if Hash(r) != tc.Digest {
			t.Fatalf("Go request contract changed for %s; update the Web matcher and protocol fixture together", r.RequestID)
		}
	}
}
