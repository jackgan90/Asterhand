package hostipc

import (
	"bytes"
	"strings"
	"testing"

	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

func TestLocalCommandsAreClosedDirectionalCapabilities(t *testing.T) {
	id := strings.Repeat("a", 32)
	valid := Message{Version: Version, Session: id, Kind: CommandCall, Epoch: 1, CommandID: id, Command: &commandswire.Request{Action: "launch", ID: id, Revision: id, Sequence: 1}}
	var data bytes.Buffer
	if err := Write(&data, valid, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(&data, true); err != nil {
		t.Fatal(err)
	}
	if err := valid.Validate(false); err == nil {
		t.Fatal("parent sent command request")
	}
	for _, change := range []func(*Message){
		func(m *Message) { m.Kind = Edit; m.Sequence = 1 }, func(m *Message) { m.Text = "arbitrary shell" },
		func(m *Message) { m.Command = &commandswire.Request{Action: "execute", ID: id} },
		func(m *Message) { m.Command = &commandswire.Request{Action: "list", ID: id} },
		func(m *Message) { m.Command = &commandswire.Request{Action: "launch", ID: id, Revision: id} },
		func(m *Message) { m.CommandReply = &commandswire.Response{} },
	} {
		m := valid
		change(&m)
		if err := m.Validate(true); err == nil {
			t.Fatalf("invalid capability accepted: %+v", m)
		}
	}
}
