package config

import (
	"errors"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

func (m *Manager) SetJournal(j operations.Journal) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.journal = j
	return m.completePublication()
}
func (m *Manager) SaveOperation(c Config, i operations.Identity) error { return m.save(c, &i) }

func (m *Manager) completePublication() error {
	p := m.Get().Publication
	if p == nil {
		return nil
	}
	r, err := m.journal.Lookup(p.Actor, p.Node, p.ID)
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
	return m.journal.Finish(r, time.Now())
}
