package quickinput

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

func TestCommandBridgeDeduplicatesDeliveryButAllowsNewLaunches(t *testing.T) {
	var count atomic.Int32
	s := &remoteSurface{session: model.ID(), epoch: 1, initialized: true, out: make(chan hostipc.Message, 32), done: make(chan struct{})}
	s.commands = func(context.Context, commandswire.Request) commandswire.Response {
		count.Add(1)
		return commandswire.Response{Started: true}
	}
	request := commandswire.Request{Action: "launch", ID: model.ID(), Revision: model.ID(), Sequence: 1}
	message := hostipc.Message{Kind: hostipc.CommandCall, Epoch: 1, CommandID: model.ID(), Command: &request}
	if err := s.receive(message); err != nil {
		t.Fatal(err)
	}
	if err := s.receive(message); err != nil {
		t.Fatal(err)
	}
	request.Sequence = 2
	if err := s.receive(message); err != nil {
		t.Fatal(err)
	}
	started, duplicate := 0, 0
	for range 3 {
		select {
		case result := <-s.out:
			if result.CommandReply.Started {
				started++
			} else if result.CommandReply.Error.Code == "duplicate" {
				duplicate++
			}
		case <-time.After(time.Second):
			t.Fatal("no command reply")
		}
	}
	if count.Load() != 2 || started != 2 || duplicate != 1 {
		t.Fatal(count.Load(), started, duplicate)
	}
	message.Epoch = 2
	if err := s.receive(message); err == nil {
		t.Fatal("stale host accepted")
	}
}

func TestCommandBridgeDoesNotBlockInputOnSlowLaunch(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	s := &remoteSurface{session: model.ID(), epoch: 1, initialized: true, out: make(chan hostipc.Message, 32), done: make(chan struct{}), draft: &Draft{}}
	s.commands = func(ctx context.Context, _ commandswire.Request) commandswire.Response {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return commandswire.Response{Started: true}
	}
	request := commandswire.Request{Action: "launch", ID: model.ID(), Revision: model.ID(), Sequence: 1}
	if err := s.receive(hostipc.Message{Kind: hostipc.CommandCall, Epoch: 1, CommandID: model.ID(), Command: &request}); err != nil {
		t.Fatal(err)
	}
	if err := s.receive(hostipc.Message{Kind: hostipc.Edit, Epoch: 1, Sequence: 1, Text: "new draft"}); err != nil {
		t.Fatal(err)
	}
	if message := <-s.out; message.Kind != hostipc.Accepted {
		t.Fatal(message)
	}
}
