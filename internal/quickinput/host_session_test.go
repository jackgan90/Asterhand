package quickinput

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type pipeHost struct {
	commands *io.PipeWriter
	events   *io.PipeReader
	exited   chan struct{}
	once     sync.Once
}

func (p *pipeHost) Input() io.WriteCloser   { return p.commands }
func (p *pipeHost) Output() io.ReadCloser   { return p.events }
func (p *pipeHost) Exited() <-chan struct{} { return p.exited }
func (p *pipeHost) GrantForeground() error  { return nil }
func (p *pipeHost) Stop() error {
	p.once.Do(func() { p.commands.Close(); p.events.Close(); close(p.exited) })
	return nil
}

func TestHostSessionDraftAndSnapshotRoundtrip(t *testing.T) {
	commands, input := io.Pipe()
	output, events := io.Pipe()
	p := &pipeHost{input, output, make(chan struct{}), sync.Once{}}
	defer p.Stop()
	protocolErrors := make(chan error, 1)
	go func() {
		defer commands.Close()
		defer events.Close()
		init, err := hostipc.Read(commands, false)
		if err != nil {
			protocolErrors <- err
			return
		}
		send := func(m hostipc.Message) error {
			m.Version = hostipc.Version
			m.Session = init.Session
			return hostipc.Write(events, m, true)
		}
		if err = send(hostipc.Message{Kind: hostipc.Ready, AppVersion: init.AppVersion, Source: init.Source, Runtime: "1.2.3.4"}); err != nil {
			protocolErrors <- err
			return
		}
		if err = send(hostipc.Message{Kind: hostipc.Edit, Epoch: init.Epoch, Sequence: 1, Text: "accepted 中文"}); err != nil {
			protocolErrors <- err
			return
		}
		var acknowledged bool
		for {
			m, err := hostipc.Read(commands, false)
			if err != nil {
				return
			}
			switch m.Kind {
			case hostipc.Accepted:
				acknowledged = m.Sequence == 1 && m.Revision == 1
			case hostipc.Snapshot:
				// The parent writer orders the edit ACK before the test's snapshot.
				if !acknowledged {
					protocolErrors <- hostipc.ErrProtocol
					return
				}
				_ = send(hostipc.Message{Kind: hostipc.SnapshotDone, Request: m.Request, Epoch: init.Epoch, Sequence: 1, Revision: 1})
			case hostipc.Quit:
				p.Stop()
				return
			}
		}
	}()
	draft := &Draft{}
	s, err := startRemoteSurface(t.TempDir(), "star", draft, func() (hostProcess, error) { return p, nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.Now().Add(time.Second)
	for {
		text, _ := draft.Read()
		if text == "accepted 中文" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("edit was not accepted")
		}
		time.Sleep(time.Millisecond)
	}
	if err = s.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-protocolErrors:
		t.Fatal(err)
	default:
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryIsBoundedAndClosedGenerationCannotRevive(t *testing.T) {
	c, _, _ := controllerFixture(t)
	created := make(chan struct{}, 2)
	c.op.Lock()
	c.factory = func() (Surface, error) { created <- struct{}{}; return &testSurface{}, nil }
	c.op.Unlock()
	c.Failed(hostFailure("host_exited"))
	select {
	case <-created:
	case <-time.After(time.Second):
		t.Fatal("recovery not attempted")
	}
	c.op.Lock()
	if c.Status().Registration != "registered" {
		t.Fatal("registration not restored")
	}
	oldGeneration := c.surfaceGeneration
	c.op.Unlock()
	c.Failed(hostFailure("host_exited"))
	deadline := time.Now().Add(time.Second)
	for c.Status().Registration != "unavailable" {
		if time.Now().After(deadline) {
			t.Fatal("crash burst not stopped")
		}
		time.Sleep(time.Millisecond)
	}
	c.op.Lock()
	if c.surface != nil || c.active != 0 {
		t.Fatal("failed recovery retained unusable host or hotkey")
	}
	c.op.Unlock()
	select {
	case <-created:
		t.Fatal("unbounded automatic respawn")
	default:
	}
	if _, err := c.Retry(); err != nil {
		t.Fatal(err)
	}
	c.failedGeneration(oldGeneration, hostFailure("host_exited"))
	c.op.Lock()
	defer c.op.Unlock()
	if c.Status().Registration != "registered" {
		t.Fatal("retired host callback affected new host")
	}
}
