package quickinput

import (
	"context"
	"errors"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

func (c *Controller) SetJournal(j operations.Journal) error {
	c.op.Lock()
	defer c.op.Unlock()
	c.journal = j
	return c.completePublication()
}
func (c *Controller) ApplyOperation(ctx context.Context, change Change, i operations.Identity) (Status, error) {
	change.publication = &i
	return c.Apply(ctx, change)
}
func (c *Controller) LocaleOperation(ctx context.Context, change LocaleChange, i operations.Identity) (Status, error) {
	change.publication = &i
	return c.SetLocale(ctx, change)
}

func (c *Controller) completePublication() error {
	p := c.Status().Desired.Publication
	if p == nil {
		return nil
	}
	if c.journal == nil {
		return operations.ErrMissing
	}
	r, err := c.journal.Lookup(p.Actor, p.Node, p.ID)
	if errors.Is(err, operations.ErrMissing) {
		if at, e := operations.IDTime(p.ID); e == nil && time.Since(at) > operations.Retention {
			return nil
		}
	}
	if err != nil {
		return err
	}
	if r.Identity != p.Identity {
		return operations.ErrConflict
	}
	if r.Terminal() {
		return nil
	}
	r.State, r.Accepted, r.Result = "completed", true, p.Result
	return c.journal.Finish(r, time.Now())
}
