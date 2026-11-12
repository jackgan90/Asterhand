package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

func operationReceipt(q queryer, key string) (operations.Receipt, error) {
	var b []byte
	err := q.QueryRow("SELECT body FROM operations WHERE identity=?", key).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return operations.Receipt{}, operations.ErrMissing
	}
	if err != nil {
		return operations.Receipt{}, err
	}
	var r operations.Receipt
	err = json.Unmarshal(b, &r)
	return r, err
}

func (s *Store) Lookup(actor, node, id string) (operations.Receipt, error) {
	return operationReceipt(s.db, (operations.Identity{Actor: actor, Node: node, ID: id}).Key())
}

func (s *Store) Begin(identity operations.Identity, now time.Time) (operations.Receipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return operations.Receipt{}, false, err
	}
	defer tx.Rollback()
	r, err := operationReceipt(tx, identity.Key())
	if err == nil {
		if r.Digest != identity.Digest || r.Name != identity.Name || r.Target != identity.Target {
			return r, false, operations.ErrConflict
		}
		return r, false, nil
	}
	if !errors.Is(err, operations.ErrMissing) {
		return r, false, err
	}
	if err = operations.CheckFresh(identity.ID, now); err != nil {
		return r, false, err
	}
	// Terminal receipts retain their entire promise period. Active and unknown
	// receipts are never evicted to make room for fresh writes.
	if _, err = tx.Exec("DELETE FROM operations WHERE terminal=1 AND updated<?", now.Add(-operations.Retention).UnixMilli()); err != nil {
		return r, false, err
	}
	var count, reserved int64
	if err = tx.QueryRow("SELECT COUNT(*),COALESCE(SUM(reserved),0) FROM operations").Scan(&count, &reserved); err != nil {
		return r, false, err
	}
	r = operations.Receipt{Identity: identity, State: "prepared", Created: now.UnixMilli(), Updated: now.UnixMilli()}
	b, err := json.Marshal(r)
	if err != nil {
		return r, false, err
	}
	// Reserve the maximum outcome size before side effects. This also ensures
	// completion cannot fail merely because another request used the space.
	size := len(b) + operations.ResultLimit + 512
	if count >= operations.ReceiptLimit || reserved+int64(size) > operations.ReceiptBytes {
		return r, false, operations.ErrCapacity
	}
	if _, err = tx.Exec("INSERT INTO operations(identity,body,updated,terminal,reserved) VALUES(?,?,?,?,?)", identity.Key(), b, r.Updated, 0, size); err != nil {
		return r, false, err
	}
	err = tx.Commit()
	return r, err == nil, err
}

func (s *Store) Finish(r operations.Receipt, now time.Time) error {
	if len(r.Result) > operations.ResultLimit {
		return operations.ErrCapacity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, err := operationReceipt(s.db, r.Identity.Key())
	if err != nil {
		return err
	}
	if old.Identity != r.Identity || old.Created != r.Created {
		return operations.ErrConflict
	}
	if old.Terminal() {
		if old.State != r.State || old.Code != r.Code || string(old.Result) != string(r.Result) || old.Accepted != r.Accepted {
			return operations.ErrConflict
		}
		return nil
	}
	if r.State != "completed" && r.State != "rejected" && r.State != "unknown" && r.State != "accepted" {
		return operations.ErrConflict
	}
	r.Updated = now.UnixMilli()
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var reserved int
	if err = s.db.QueryRow("SELECT reserved FROM operations WHERE identity=?", r.Key()).Scan(&reserved); err != nil {
		return err
	}
	if len(b) > reserved {
		return operations.ErrCapacity
	}
	_, err = s.db.Exec("UPDATE operations SET body=?,updated=?,terminal=? WHERE identity=?", b, r.Updated, r.Terminal(), r.Key())
	return err
}

func (s *Store) Pending() ([]operations.Receipt, error) {
	rows, err := s.db.Query("SELECT body FROM operations WHERE terminal=0 ORDER BY updated")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []operations.Receipt
	for rows.Next() {
		var b []byte
		var r operations.Receipt
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Page reads a bounded set without materializing a whole record family. The
// opaque cursor is the last rowid, represented as a decimal string on the wire.
func (s *Store) Page(kind string, before int64, limit int) ([]json.RawMessage, int64, error) {
	if limit < 1 || limit > 200 || before < 0 {
		return nil, 0, errors.New("invalid_page")
	}
	rows, err := s.db.Query("SELECT rowid,body FROM records WHERE kind=? AND (?=0 OR rowid<?) ORDER BY rowid DESC LIMIT ?", kind, before, before, limit+1)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []json.RawMessage{}
	var last int64
	for rows.Next() {
		var id int64
		var b []byte
		if err = rows.Scan(&id, &b); err != nil {
			return nil, 0, err
		}
		if len(out) == limit {
			return out, last, nil
		}
		last = id
		out = append(out, json.RawMessage(b))
	}
	return out, 0, rows.Err()
}
