package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

func (s *Store) AutomationState(identity operations.Identity) (operations.State, error) {
	var value operations.State
	err := s.Get("automation-state", identity.Key(), &value)
	if err == nil && value.Identity != identity {
		err = operations.ErrConflict
	}
	return value, err
}

// Every slot reserves 16 KiB before execution. Retained or uncertain records
// are never evicted to admit a new side effect.
func (s *Store) SaveAutomationState(value operations.State) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(b) > operations.StateBytes {
		return operations.ErrCapacity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old []byte
	err = tx.QueryRow("SELECT body FROM records WHERE kind='automation-state' AND id=?", value.Key()).Scan(&old)
	if err == nil {
		var previous operations.State
		if err = json.Unmarshal(old, &previous); err != nil {
			return err
		}
		if previous.Identity != value.Identity || previous.Instance != value.Instance {
			return operations.ErrConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.Exec("DELETE FROM records WHERE kind='automation-state' AND json_extract(body,'$.complete')=1 AND json_extract(body,'$.updated_ms')<?", time.Now().Add(-operations.Retention).UnixMilli()); err != nil {
			return err
		}
		var count int
		if err = tx.QueryRow("SELECT COUNT(*) FROM records WHERE kind='automation-state'").Scan(&count); err != nil {
			return err
		}
		if count >= operations.StateLimit {
			return operations.ErrCapacity
		}
	} else {
		return err
	}
	_, err = tx.Exec("INSERT INTO records(kind,id,body) VALUES('automation-state',?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body", value.Key(), b)
	if err != nil {
		return err
	}
	return tx.Commit()
}
