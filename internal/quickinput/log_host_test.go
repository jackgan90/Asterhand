package quickinput

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type slowLogStop struct {
	*pipeHost
	started, release chan struct{}
	stopping         sync.Once
}

func (p *slowLogStop) Stop() error {
	p.stopping.Do(func() { close(p.started) })
	<-p.release
	return p.pipeHost.Stop()
}

type failedLogStop struct {
	*pipeHost
	failure error
}

func (p *failedLogStop) Stop() error { return p.failure }

func TestLogHostCloseRetainsFailedRetirement(t *testing.T) {
	p, received := logPipe(t, false)
	failure := errors.New("fixture: host exit unconfirmed")
	h := newLogWindowHost(t.TempDir(), nil, func() (hostProcess, error) { return &failedLogStop{pipeHost: p, failure: failure}, nil }).(*logHost)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- h.Open(ctx, model.ID()) }()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("no open")
	}
	h.mu.Lock()
	c := h.connection
	h.mu.Unlock()
	cancel()
	<-result
	select {
	case <-c.reaped:
	case <-time.After(time.Second):
		t.Fatal("retirement did not finish")
	}
	for range 2 {
		if err := h.Close(); !errors.Is(err, failure) {
			t.Fatal("lost failed retirement", err)
		}
	}
}

func logPipe(t *testing.T, respond bool) (*pipeHost, <-chan hostipc.Message) {
	t.Helper()
	commands, input := io.Pipe()
	output, events := io.Pipe()
	p := &pipeHost{input, output, make(chan struct{}), sync.Once{}}
	received := make(chan hostipc.Message, 8)
	t.Cleanup(func() { _ = p.Stop() })
	go func() {
		defer commands.Close()
		defer events.Close()
		initial, err := hostipc.Read(commands, false)
		if err != nil {
			return
		}
		send := func(m hostipc.Message) error {
			m.Version = hostipc.Version
			m.Session = initial.Session
			return hostipc.Write(events, m, true)
		}
		if send(hostipc.Message{Kind: hostipc.Ready, AppVersion: initial.AppVersion, Source: initial.Source, Runtime: "log-host"}) != nil {
			return
		}
		for {
			m, err := hostipc.Read(commands, false)
			if err != nil {
				return
			}
			received <- m
			if respond && m.Kind == hostipc.OpenLog {
				if send(hostipc.Message{Kind: hostipc.State, Request: m.Request}) != nil {
					return
				}
			}
		}
	}()
	return p, received
}

func TestLogHostReusesReadyAndCarriesClickMonitor(t *testing.T) {
	p, received := logPipe(t, true)
	launches := 0
	h := newLogWindowHost(t.TempDir(), nil, func() (hostProcess, error) { launches++; return p, nil })
	defer h.Close()
	anchor := hostipc.WindowAnchor{X: -1280, Y: 720}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), logAnchorContextKey{}, anchor), time.Second)
	defer cancel()
	for range 3 {
		if err := h.Open(ctx, model.ID()); err != nil {
			t.Fatal(err)
		}
		if m := <-received; m.Anchor == nil || *m.Anchor != anchor {
			t.Fatal("lost input monitor", m)
		}
	}
	if launches != 1 {
		t.Fatal("ready latch not reusable", launches)
	}
}

func TestLogHostTimeoutRetiresBeforeSlowStopAndStartsFresh(t *testing.T) {
	p, received := logPipe(t, false)
	slow := &slowLogStop{pipeHost: p, started: make(chan struct{}), release: make(chan struct{})}
	next, _ := logPipe(t, true)
	launches := 0
	h := newLogWindowHost(t.TempDir(), nil, func() (hostProcess, error) {
		launches++
		if launches == 1 {
			return slow, nil
		}
		return next, nil
	}).(*logHost)
	t.Cleanup(func() {
		close(slow.release)
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- h.Open(ctx, model.ID()) }()
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("no open")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("open waited for Stop")
	}
	select {
	case <-slow.started:
	case <-time.After(time.Second):
		t.Fatal("Stop not started")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := h.Open(ctx2, model.ID()); err != nil {
		t.Fatal("fresh connection blocked", err)
	}
	if launches != 2 {
		t.Fatal(launches)
	}
	// Both live and retired processes remain owned until shutdown is confirmed.
	h.mu.Lock()
	count := len(h.connections)
	h.mu.Unlock()
	if count != 2 {
		t.Fatal("lost retiring process", count)
	}
	_ = next.Stop()
}

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
	c := &logConnection{process: p, session: model.ID(), out: make(chan hostipc.Message, 32), done: make(chan struct{}), ready: make(chan struct{}), stopped: make(chan struct{})}
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
