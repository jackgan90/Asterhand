package wire

import "testing"

func TestNodeLogRequestKeepsCapabilitiesScoped(t *testing.T) {
	base := Request{Action: "read_log", ID: "command_id_123456", RunID: "run_id_1234567890", NodeID: "node_id_123456789", Cursor: 42}
	for _, change := range []func(*Request){
		func(*Request) {},
		func(r *Request) { r.Before = true },
		func(r *Request) { r.Action, r.Cursor = "tail_log", 0 },
		func(r *Request) { r.NodeID = "" },
	} {
		r := base
		change(&r)
		if err := r.Validate(); err != nil {
			t.Fatal("valid log request rejected", r, err)
		}
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.Action = "stop"; r.Cursor = 0 },
		func(r *Request) { r.Action = "status"; r.Cursor = 0; r.RunID = "" },
		func(r *Request) { r.NodeID = "../../file" },
		func(r *Request) { r.NodeID = ""; r.Before = true },
		func(r *Request) { r.Action = "tail_log"; r.Cursor = 0; r.Before = true },
		func(r *Request) { r.Cursor = -1 },
	} {
		r := base
		change(&r)
		if err := r.Validate(); err == nil {
			t.Fatal("unscoped node log request accepted", r)
		}
	}
}
