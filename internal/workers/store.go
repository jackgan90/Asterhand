package workers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/i18n"
	_ "modernc.org/sqlite"
)

const (
	DBSchemaVersion    = 1
	MaxResultBytes     = 2 << 20  // 2 MiB per turn full text
	MaxTurnLogBytes    = 1 << 20  // 1 MiB per turn structured logs
	MaxTotalDetailBytes = 64 << 20 // 64 MiB node-wide retention cap
	SummaryRetention   = 30 * 24 * time.Hour
	DetailRetention    = 7 * 24 * time.Hour
)

var ErrNotFound = errors.New("not_found")

type Store struct {
	mu sync.Mutex
	db *sql.DB
}

func OpenStore(home string) (*Store, error) {
	dir := filepath.Join(home, "workers")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "workers.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var schema int
	if err = db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil {
		db.Close()
		return nil, err
	}
	if schema > DBSchemaVersion {
		db.Close()
		return nil, i18n.ErrArgs("err.store.schema_newer", map[string]string{
			"schema":    strconv.Itoa(schema),
			"supported": strconv.Itoa(DBSchemaVersion),
		})
	}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
	CREATE TABLE IF NOT EXISTS workers(
		worker_id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		alias TEXT NOT NULL,
		operation_id TEXT NOT NULL,
		state TEXT NOT NULL,
		body BLOB NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_workers_ws_alias ON workers(workspace_id, alias);
	CREATE INDEX IF NOT EXISTS idx_workers_op ON workers(operation_id);

	CREATE TABLE IF NOT EXISTS turns(
		turn_id TEXT PRIMARY KEY,
		worker_id TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		operation_id TEXT NOT NULL,
		client_correlation_id TEXT NOT NULL,
		state TEXT NOT NULL,
		body BLOB NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_turns_worker ON turns(worker_id, updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_turns_op ON turns(operation_id);

	CREATE TABLE IF NOT EXISTS turn_results(
		turn_id TEXT PRIMARY KEY,
		body BLOB NOT NULL,
		byte_len INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS turn_logs(
		turn_id TEXT NOT NULL,
		seq INTEGER NOT NULL,
		body BLOB NOT NULL,
		byte_len INTEGER NOT NULL,
		PRIMARY KEY(turn_id, seq)
	);
	PRAGMA user_version=1;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) SaveWorker(w Worker) error {
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(
		`INSERT INTO workers(worker_id, workspace_id, alias, operation_id, state, body, updated_at)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(worker_id) DO UPDATE SET
		   workspace_id=excluded.workspace_id,
		   alias=excluded.alias,
		   operation_id=excluded.operation_id,
		   state=excluded.state,
		   body=excluded.body,
		   updated_at=excluded.updated_at`,
		w.ID, w.WorkspaceID, w.Alias, w.OperationID, w.State, b, w.UpdatedAt.UnixMilli(),
	)
	return err
}

func (s *Store) GetWorker(workerID string) (Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b []byte
	err := s.db.QueryRow("SELECT body FROM workers WHERE worker_id=?", workerID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, err
	}
	var w Worker
	return w, json.Unmarshal(b, &w)
}

func (s *Store) FindActiveWorkerByAlias(workspaceID, alias string) (Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b []byte
	err := s.db.QueryRow(
		"SELECT body FROM workers WHERE workspace_id=? AND alias=? AND state<>? ORDER BY updated_at DESC LIMIT 1",
		workspaceID, alias, WorkerClosed,
	).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, err
	}
	var w Worker
	return w, json.Unmarshal(b, &w)
}

func (s *Store) WorkerByOperationID(operationID string) (Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b []byte
	err := s.db.QueryRow("SELECT body FROM workers WHERE operation_id=? LIMIT 1", operationID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return Worker{}, ErrNotFound
	}
	if err != nil {
		return Worker{}, err
	}
	var w Worker
	return w, json.Unmarshal(b, &w)
}

func (s *Store) ListWorkers(workspaceID string, includeClosed bool, limit int) ([]Worker, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	query := "SELECT body FROM workers WHERE 1=1"
	args := []any{}
	if workspaceID != "" {
		query += " AND workspace_id=?"
		args = append(args, workspaceID)
	}
	if !includeClosed {
		query += " AND state<>?"
		args = append(args, WorkerClosed)
	}
	query += " ORDER BY updated_at DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Worker{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var w Worker
		if err := json.Unmarshal(b, &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) SaveTurn(t Turn) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.db.Exec(
		`INSERT INTO turns(turn_id, worker_id, workspace_id, operation_id, client_correlation_id, state, body, updated_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(turn_id) DO UPDATE SET
		   state=excluded.state,
		   body=excluded.body,
		   updated_at=excluded.updated_at`,
		t.ID, t.WorkerID, t.WorkspaceID, t.OperationID, t.ClientCorrelationID, t.State, b, t.UpdatedAt.UnixMilli(),
	)
	return err
}

func (s *Store) GetTurn(turnID string) (Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b []byte
	err := s.db.QueryRow("SELECT body FROM turns WHERE turn_id=?", turnID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return Turn{}, ErrNotFound
	}
	if err != nil {
		return Turn{}, err
	}
	var t Turn
	return t, json.Unmarshal(b, &t)
}

func (s *Store) TurnByOperationID(operationID string) (Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b []byte
	err := s.db.QueryRow("SELECT body FROM turns WHERE operation_id=? LIMIT 1", operationID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return Turn{}, ErrNotFound
	}
	if err != nil {
		return Turn{}, err
	}
	var t Turn
	return t, json.Unmarshal(b, &t)
}

func (s *Store) ListTurnsByWorker(workerID string, limit int) ([]Turn, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT body FROM turns WHERE worker_id=? ORDER BY updated_at DESC LIMIT ?", workerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Turn{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var t Turn
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) ListNonTerminalTurns() ([]Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query("SELECT body FROM turns WHERE state IN (?,?,?) ORDER BY updated_at ASC", TurnPrepared, TurnRunning, TurnReconcile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Turn{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var t Turn
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SaveResultText(turnID, text string, now time.Time) (int, bool, error) {
	truncated := false
	raw := []byte(text)
	if len(raw) > MaxResultBytes {
		raw = raw[:clampUTF8Bytes(raw, MaxResultBytes)]
		truncated = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		`INSERT INTO turn_results(turn_id, body, byte_len, updated_at)
		 VALUES(?,?,?,?)
		 ON CONFLICT(turn_id) DO UPDATE SET body=excluded.body, byte_len=excluded.byte_len, updated_at=excluded.updated_at`,
		turnID, raw, len(raw), now.UnixMilli(),
	)
	return len(raw), truncated, err
}

func (s *Store) ReadResultText(turnID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b []byte
	err := s.db.QueryRow("SELECT body FROM turn_results WHERE turn_id=?", turnID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

func (s *Store) AppendLog(turnID string, entry LogEntry) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int
	_ = s.db.QueryRow("SELECT COALESCE(SUM(byte_len),0) FROM turn_logs WHERE turn_id=?", turnID).Scan(&total)
	if total >= MaxTurnLogBytes {
		return 0, automation.Fail("capacity", "Turn log budget reached")
	}
	var lastSeq int64
	_ = s.db.QueryRow("SELECT COALESCE(MAX(seq),0) FROM turn_logs WHERE turn_id=?", turnID).Scan(&lastSeq)
	entry.Seq = lastSeq + 1
	b, err := json.Marshal(entry)
	if err != nil {
		return 0, err
	}
	if total+len(b) > MaxTurnLogBytes {
		return 0, automation.Fail("capacity", "Turn log budget reached")
	}
	_, err = s.db.Exec("INSERT INTO turn_logs(turn_id, seq, body, byte_len) VALUES(?,?,?,?)", turnID, entry.Seq, b, len(b))
	return entry.Seq, err
}

func (s *Store) ReadLogs(turnID string, afterSeq int64, limit int) ([]LogEntry, bool, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		"SELECT body FROM turn_logs WHERE turn_id=? AND seq>? ORDER BY seq ASC LIMIT ?",
		turnID, afterSeq, limit+1,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, false, err
		}
		var e LogEntry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, false, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := false
	if len(out) > limit {
		hasMore = true
		out = out[:limit]
	}
	return out, hasMore, nil
}

func (s *Store) ReadLogsTail(turnID string, limit int) ([]LogEntry, bool, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		"SELECT body FROM turn_logs WHERE turn_id=? ORDER BY seq DESC LIMIT ?",
		turnID, limit+1,
	)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, false, err
		}
		var e LogEntry
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, false, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := false
	if len(out) > limit {
		hasMore = true
		out = out[:limit]
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, hasMore, nil
}

// PruneExpiredDetails evicts terminal turn result/log details beyond
// DetailRetention while never touching active or reconciliation_required turns.
func (s *Store) PruneExpiredDetails(now time.Time) {
	cutoff := now.Add(-DetailRetention).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.Exec(`DELETE FROM turn_results WHERE updated_at < ? AND turn_id IN (SELECT turn_id FROM turns WHERE state NOT IN (?,?,?))`,
		cutoff, TurnPrepared, TurnRunning, TurnReconcile)
	_, _ = s.db.Exec(`DELETE FROM turn_logs WHERE turn_id IN (SELECT turn_id FROM turns WHERE updated_at < ? AND state NOT IN (?,?,?))`,
		cutoff, TurnPrepared, TurnRunning, TurnReconcile)
}

func clampUTF8Bytes(b []byte, max int) int {
	if len(b) <= max {
		return len(b)
	}
	n := max
	for n > 0 && (b[n]&0xc0) == 0x80 {
		n--
	}
	return n
}
