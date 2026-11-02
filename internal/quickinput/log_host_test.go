package quickinput

import (
	"context"
	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
	"io"
	"sync"
	"testing"
	"time"
)

func TestLogHostSaturationKeepsHostAndStopAvailable(t *testing.T) {
	commands, input := io.Pipe()
	output, events := io.Pipe()
	p := &pipeHost{input, output, make(chan struct{}), sync.Once{}}
	defer p.Stop()
	defer commands.Close()
	defer events.Close()
	blocked := make(chan struct{})
	defer close(blocked)
	entered := make(chan struct{}, 5)
	h := &logHost{call: func(_ context.Context, request commandswire.Request) commandswire.Response {
		if request.Action == "stop" {
			return commandswire.Response{Started: true}
		}
		entered <- struct{}{}
		<-blocked
		return commandswire.Response{}
	}}
	c := &logConnection{process: p, session: model.ID(), out: make(chan hostipc.Message, 32), done: make(chan struct{}), ready: make(chan error, 1)}
	go h.readLoop(c)
	for i := 0; i < 5; i++ {
		req := commandswire.Request{Action: "status", ID: model.ID()}
		err := hostipc.Write(events, hostipc.Message{Version: hostipc.Version, Session: c.session, Kind: hostipc.CommandCall, Epoch: 1, CommandID: model.ID(), Command: &req}, true)
		if err != nil {
			t.Fatal(err)
		}
		if i < 4 {
			<-entered
		}
	}
	select {
	case reply := <-c.out:
		if reply.CommandReply.Error == nil || reply.CommandReply.Error.Code != "busy" {
			t.Fatal("missing busy reply", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("no busy reply")
	}
	req := commandswire.Request{Action: "stop", ID: model.ID(), RunID: model.ID()}
	if err := hostipc.Write(events, hostipc.Message{Version: hostipc.Version, Session: c.session, Kind: hostipc.CommandCall, Epoch: 1, CommandID: model.ID(), Command: &req}, true); err != nil {
		t.Fatal(err)
	}
	select {
	case reply := <-c.out:
		if !reply.CommandReply.Started {
			t.Fatal("stop not serviced", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("stop blocked by reads")
	}
	select {
	case <-c.done:
		t.Fatal("five valid concurrent window requests terminated entire log host")
	case <-time.After(50 * time.Millisecond):
	}
}
