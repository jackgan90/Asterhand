package quickinput

import (
	"context"
	"io"
	"time"

	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type hostDelivery struct {
	message hostipc.Message
	written chan error
}

// hostPeer owns the child side's single writer and bounded command reader.
type hostPeer struct {
	ctx      context.Context
	session  string
	out      chan hostDelivery
	in       chan hostipc.Message
	failures chan error
}

func newHostPeer(ctx context.Context, input io.Reader, output io.Writer, session string) *hostPeer {
	p := &hostPeer{ctx: ctx, session: session, out: make(chan hostDelivery, 32), in: make(chan hostipc.Message, 32), failures: make(chan error, 1)}
	go p.readLoop(input)
	go p.writeLoop(output)
	return p
}

func (p *hostPeer) fail(err error) {
	select {
	case p.failures <- err:
	default:
	}
}
func (p *hostPeer) send(m hostipc.Message) error { return p.enqueue(hostDelivery{message: m}) }

func (p *hostPeer) enqueue(delivery hostDelivery) error {
	delivery.message.Version, delivery.message.Session = hostipc.Version, p.session
	select {
	case p.out <- delivery:
		return nil
	case <-p.ctx.Done():
		return p.ctx.Err()
	default:
		return hostipc.ErrProtocol
	}
}

func (p *hostPeer) terminalFailure(code string) {
	written := make(chan error, 1)
	if p.enqueue(hostDelivery{message: hostipc.Message{Kind: hostipc.Failure, Code: code}, written: written}) != nil {
		return
	}
	// Flush the bounded diagnostic before process exit; never wait on a broken
	// parent indefinitely. It contains a fixed error code, not editor text.
	select {
	case <-written:
	case <-p.ctx.Done():
	case <-time.After(time.Second):
	}
}

func (p *hostPeer) readLoop(input io.Reader) {
	for {
		m, err := hostipc.Read(input, false)
		if err != nil {
			p.fail(err)
			return
		}
		if m.Session != p.session || m.Kind == hostipc.Initialize {
			p.fail(hostipc.ErrProtocol)
			return
		}
		select {
		case p.in <- m:
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *hostPeer) writeLoop(output io.Writer) {
	for {
		select {
		case delivery := <-p.out:
			err := hostipc.Write(output, delivery.message, true)
			if delivery.written != nil {
				delivery.written <- err
			}
			if err != nil {
				p.fail(err)
				return
			}
		case <-p.ctx.Done():
			return
		}
	}
}
