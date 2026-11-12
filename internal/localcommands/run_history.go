package localcommands

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/operations"
	_ "modernc.org/sqlite"
)

const (
	MaxHistoryBytes    int64 = 2 << 30
	MaxNodeRunBytes    int64 = 4 << 30
	historyReservation       = 4096
)

// This index stores observation and retention data. Only current.json admits
// execution; a prepared history entry never authorizes starting a process.
type runHistory struct{ db *sql.DB }

type RunSummary struct {
	CommandID       string     `json:"command_id"`
	CommandRevision string     `json:"command_revision"`
	Name            string     `json:"name"`
	ID              string     `json:"run_id"`
	RoundID         string     `json:"round_id"`
	Sequence        string     `json:"sequence"`
	Phase           string     `json:"phase"`
	Result          string     `json:"result,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	Started         time.Time  `json:"started_at"`
	Ended           *time.Time `json:"ended_at,omitempty"`
	LogComplete     bool       `json:"log_complete"`
	Details         string     `json:"details"`
	RetainedUntil   *time.Time `json:"retained_until,omitempty"`
}

func summarizeRun(r RunRecord, details string) RunSummary {
	s := RunSummary{CommandID: r.Command.ID, CommandRevision: r.Command.Revision, Name: r.Command.Name, ID: r.ID, RoundID: r.RoundID, Sequence: strconv.FormatUint(r.Sequence, 10), Phase: r.Phase, Result: r.Result, Reason: r.Reason, Started: r.Started, Ended: r.Ended, LogComplete: r.LogComplete, Details: details}
	if r.Ended != nil {
		until := r.Ended.Add(7 * 24 * time.Hour)
		s.RetainedUntil = &until
	}
	return s
}

func (s RunSummary) record() RunRecord {
	n, _ := strconv.ParseUint(s.Sequence, 10, 64)
	return RunRecord{Version: 1, ID: s.ID, RoundID: s.RoundID, Sequence: n, Command: Command{ID: s.CommandID, Revision: s.CommandRevision, Name: s.Name}, Phase: s.Phase, Result: s.Result, Reason: s.Reason, Started: s.Started, Ended: s.Ended, LogComplete: s.LogComplete}
}

func openRunHistory(root string) (*runHistory, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "history.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err == nil && version > 2 {
		err = operations.ErrConflict
	}
	if err == nil {
		_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
	CREATE TABLE IF NOT EXISTS runs(id TEXT PRIMARY KEY, command TEXT NOT NULL, round TEXT NOT NULL, started INTEGER NOT NULL, ended INTEGER NOT NULL, admitted INTEGER NOT NULL, details TEXT NOT NULL, body BLOB NOT NULL, pinned INTEGER NOT NULL DEFAULT 0);
	CREATE INDEX IF NOT EXISTS run_page ON runs(command,admitted,started,id);`)
	}
	if err == nil && version == 1 {
		_, err = db.Exec("ALTER TABLE runs ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0")
	}
	if err == nil {
		_, err = db.Exec("PRAGMA user_version=2")
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &runHistory{db}, nil
}

func (h *runHistory) reserve(r RunRecord) error {
	if err := h.expire(); err != nil {
		return err
	}
	var count int64
	if err := h.db.QueryRow("SELECT COUNT(*) FROM runs").Scan(&count); err != nil {
		return err
	}
	if count >= 100000 || (count+1)*historyReservation > 64<<20 {
		return operations.ErrCapacity
	}
	s := summarizeRun(r, "retained")
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(b) > historyReservation {
		return operations.ErrCapacity
	}
	_, err = h.db.Exec("INSERT INTO runs(id,command,round,started,ended,admitted,details,body) VALUES(?,?,?,?,0,0,'retained',?)", r.ID, r.Command.ID, r.RoundID, r.Started.UnixMilli(), b)
	return err
}

func (h *runHistory) save(r RunRecord) error {
	s := summarizeRun(r, "retained")
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(b) > historyReservation {
		return operations.ErrCapacity
	}
	ended := int64(0)
	if r.Phase == "finished" && r.Ended != nil {
		ended = r.Ended.UnixMilli()
	}
	// Legacy current records are imported before any fresh admission.
	_, err = h.db.Exec(`INSERT INTO runs(id,command,round,started,ended,admitted,details,body) VALUES(?,?,?,?,?,1,'retained',?)
	ON CONFLICT(id) DO UPDATE SET ended=excluded.ended, admitted=1,body=excluded.body`, r.ID, r.Command.ID, r.RoundID, r.Started.UnixMilli(), ended, b)
	return err
}

func (h *runHistory) get(id string) (RunSummary, error) {
	var b []byte
	var details string
	err := h.db.QueryRow("SELECT body,details FROM runs WHERE id=? AND admitted=1", id).Scan(&b, &details)
	if errors.Is(err, sql.ErrNoRows) {
		return RunSummary{}, operations.ErrMissing
	}
	var s RunSummary
	if err == nil {
		err = json.Unmarshal(b, &s)
		s.Details = details
	}
	return s, err
}

func (h *runHistory) expire() error {
	_, err := h.db.Exec("DELETE FROM runs WHERE pinned=0 AND ended>0 AND ended<? AND details<>'retained'", time.Now().Add(-operations.Retention).UnixMilli())
	return err
}

func (h *runHistory) pinCurrent(command string, ids []string) error {
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE runs SET pinned=0 WHERE command=?", command); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.Exec("UPDATE runs SET pinned=1 WHERE id=? AND command=?", id, command); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (h *runHistory) page(command, before string, limit int) ([]RunSummary, string, error) {
	if !wire.ValidID(command) || limit < 1 || limit > 200 {
		return nil, "", operations.ErrInvalid
	}
	// Row IDs are admission order, not wall-clock order. A cursor remains valid
	// as newer runs arrive; it never points at the mutable current round.
	end := int64(^uint64(0) >> 1)
	if before != "" {
		var err error
		end, err = strconv.ParseInt(before, 10, 64)
		if err != nil || end <= 0 {
			return nil, "", operations.ErrInvalid
		}
	}
	rows, err := h.db.Query("SELECT rowid,body,details FROM runs WHERE command=? AND admitted=1 AND rowid<? ORDER BY rowid DESC LIMIT ?", command, end, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []RunSummary{}
	next := ""
	last := int64(0)
	for rows.Next() {
		var row int64
		var b []byte
		var details string
		if err = rows.Scan(&row, &b, &details); err != nil {
			return nil, "", err
		}
		if len(items) == limit {
			next = strconv.FormatInt(last, 10)
			break
		}
		var s RunSummary
		if err = json.Unmarshal(b, &s); err != nil {
			return nil, "", err
		}
		s.Details = details
		items = append(items, s)
		last = row
	}
	return items, next, rows.Err()
}
