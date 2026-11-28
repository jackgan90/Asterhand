package workers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/i18n"
	_ "modernc.org/sqlite"
)

const (
	DBSchemaVersion     = 1
	MaxResultBytes      = 2 << 20  // 2 MiB per turn full text
	MaxTurnLogBytes     = 1 << 20  // 1 MiB per turn structured logs
	MaxTotalDetailBytes = 64 << 20 // 64 MiB node-wide retention cap
	resultChunkBytes    = 16 << 10 // 16 KiB per B-tree indexed result chunk
	SummaryRetention    = 30 * 24 * time.Hour
	DetailRetention     = 7 * 24 * time.Hour
)

var (
	ErrNotFound        = errors.New("not_found")
	ErrStoreClosed     = errors.New("workers_store_closed")
	ErrInvalidCursor   = errors.New("invalid_cursor")
	ErrListCursorStale = errors.New("list_cursor_stale")
)

type Store struct {
	mu                sync.Mutex
	db                *sql.DB
	detailBytes       int64
	detailBytesLoaded bool
	workersRev        uint64
	wsRev             map[string]uint64
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

	CREATE TABLE IF NOT EXISTS turn_result_chunks(
		turn_id TEXT NOT NULL,
		chunk_idx INTEGER NOT NULL,
		body BLOB NOT NULL,
		byte_len INTEGER NOT NULL,
		PRIMARY KEY(turn_id, chunk_idx)
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
	st := &Store{db: db}
	if err := st.initDetailBytes(); err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
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
	if s.db == nil {
		return ErrStoreClosed
	}
	opKey := dedupKey(w.OperationKey, w.OperationID)
	_, err = s.db.Exec(
		`INSERT INTO workers(worker_id, workspace_id, alias, operation_id, state, body, updated_at)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(worker_id) DO UPDATE SET
		   workspace_id=excluded.workspace_id,
		   alias=excluded.alias,
		   state=excluded.state,
		   body=excluded.body,
		   updated_at=excluded.updated_at`,
		w.ID, w.WorkspaceID, w.Alias, opKey, w.State, b, w.UpdatedAt.UnixMilli(),
	)
	if err == nil {
		s.bumpWorkerRevLocked(w.WorkspaceID)
	}
	return err
}

func (s *Store) GetWorker(workerID string) (Worker, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return Worker{}, ErrStoreClosed
	}
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
	if s.db == nil {
		return Worker{}, ErrStoreClosed
	}
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
	items, err := s.ListWorkersByOperationID(operationID, 1)
	if err != nil {
		return Worker{}, err
	}
	if len(items) == 0 {
		return Worker{}, ErrNotFound
	}
	return items[0], nil
}

func (s *Store) ListWorkersByOperationID(operationID string, limit int) ([]Worker, error) {
	if limit <= 0 {
		limit = 2
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, ErrStoreClosed
	}
	rows, err := s.db.Query("SELECT body FROM workers WHERE operation_id=? LIMIT ?", operationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Worker
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

func (s *Store) bumpWorkerRevLocked(workspaceID string) {
	s.workersRev++
	if workspaceID != "" {
		if s.wsRev == nil {
			s.wsRev = map[string]uint64{}
		}
		s.wsRev[workspaceID]++
	}
}

func (s *Store) workerListRevisionLocked(workspaceID string, includeClosed bool) (string, error) {
	query := "SELECT COUNT(*), COALESCE(MAX(updated_at), 0), COALESCE(SUM(updated_at), 0) FROM workers WHERE 1=1"
	args := []any{}
	if workspaceID != "" {
		query += " AND workspace_id=?"
		args = append(args, workspaceID)
	}
	if !includeClosed {
		query += " AND state<>?"
		args = append(args, WorkerClosed)
	}
	var count, maxTS, sumTS int64
	if err := s.db.QueryRow(query, args...).Scan(&count, &maxTS, &sumTS); err != nil {
		return "", err
	}
	var memRev uint64
	if workspaceID != "" {
		memRev = s.wsRev[workspaceID]
	} else {
		memRev = s.workersRev
	}
	return strconv.FormatUint(memRev, 10) + ":" +
		strconv.FormatInt(count, 10) + ":" +
		strconv.FormatInt(maxTS, 10) + ":" +
		strconv.FormatInt(sumTS, 10), nil
}

func EncodeWorkerListAfter(w Worker) string {
	return strconv.FormatInt(w.UpdatedAt.UnixMilli(), 10) + ":" + w.ID
}

func parseWorkerListAfter(raw string) (int64, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, "", nil
	}
	idx := strings.IndexByte(raw, ':')
	if idx <= 0 || idx >= len(raw)-1 {
		return 0, "", ErrInvalidCursor
	}
	ms, err := strconv.ParseInt(raw[:idx], 10, 64)
	if err != nil {
		return 0, "", ErrInvalidCursor
	}
	id := strings.TrimSpace(raw[idx+1:])
	if id == "" {
		return 0, "", ErrInvalidCursor
	}
	return ms, id, nil
}

func (s *Store) ListWorkers(workspaceID string, includeClosed bool, limit int) ([]Worker, error) {
	items, _, _, err := s.ListWorkersPage(workspaceID, includeClosed, "", "", limit)
	return items, err
}

func (s *Store) ListWorkersPage(workspaceID string, includeClosed bool, expectedRevision, after string, limit int) ([]Worker, bool, string, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	curMS, curID, err := parseWorkerListAfter(after)
	if err != nil {
		return nil, false, "", err
	}
	if curID != "" && expectedRevision == "" {
		return nil, false, "", ErrInvalidCursor
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, false, "", ErrStoreClosed
	}
	rev, err := s.workerListRevisionLocked(workspaceID, includeClosed)
	if err != nil {
		return nil, false, "", err
	}
	if expectedRevision != "" && expectedRevision != rev {
		return nil, false, "", ErrListCursorStale
	}
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
	if curID != "" {
		query += " AND (updated_at < ? OR (updated_at = ? AND worker_id < ?))"
		args = append(args, curMS, curMS, curID)
	}
	query += " ORDER BY updated_at DESC, worker_id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, false, "", err
	}
	defer rows.Close()
	out := []Worker{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, false, "", err
		}
		var w Worker
		if err := json.Unmarshal(b, &w); err != nil {
			return nil, false, "", err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, false, "", err
	}
	hasMore := false
	if len(out) > limit {
		hasMore = true
		out = out[:limit]
	}
	return out, hasMore, rev, nil
}

func (s *Store) SaveTurn(t Turn) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrStoreClosed
	}
	opKey := dedupKey(t.OperationKey, t.OperationID)
	_, err = s.db.Exec(
		`INSERT INTO turns(turn_id, worker_id, workspace_id, operation_id, client_correlation_id, state, body, updated_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(turn_id) DO UPDATE SET
		   state=excluded.state,
		   body=excluded.body,
		   updated_at=excluded.updated_at`,
		t.ID, t.WorkerID, t.WorkspaceID, opKey, t.ClientCorrelationID, t.State, b, t.UpdatedAt.UnixMilli(),
	)
	return err
}

// SaveTurnAndWorker persists a turn update and an optional worker update in a
// single SQLite transaction so turn state, write-lock retention, and worker
// ownership never diverge on partial failures.
func (s *Store) SaveTurnAndWorker(t Turn, w *Worker) error {
	tb, err := json.Marshal(t)
	if err != nil {
		return err
	}
	var wb []byte
	if w != nil {
		wb, err = json.Marshal(*w)
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrStoreClosed
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	turnOpKey := dedupKey(t.OperationKey, t.OperationID)
	if _, err := tx.Exec(
		`INSERT INTO turns(turn_id, worker_id, workspace_id, operation_id, client_correlation_id, state, body, updated_at)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(turn_id) DO UPDATE SET
		   state=excluded.state,
		   body=excluded.body,
		   updated_at=excluded.updated_at`,
		t.ID, t.WorkerID, t.WorkspaceID, turnOpKey, t.ClientCorrelationID, t.State, tb, t.UpdatedAt.UnixMilli(),
	); err != nil {
		return err
	}
	if w != nil {
		workerOpKey := dedupKey(w.OperationKey, w.OperationID)
		if _, err := tx.Exec(
			`INSERT INTO workers(worker_id, workspace_id, alias, operation_id, state, body, updated_at)
			 VALUES(?,?,?,?,?,?,?)
			 ON CONFLICT(worker_id) DO UPDATE SET
			   workspace_id=excluded.workspace_id,
			   alias=excluded.alias,
			   state=excluded.state,
			   body=excluded.body,
			   updated_at=excluded.updated_at`,
			w.ID, w.WorkspaceID, w.Alias, workerOpKey, w.State, wb, w.UpdatedAt.UnixMilli(),
		); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if w != nil {
		s.bumpWorkerRevLocked(w.WorkspaceID)
	}
	return nil
}

func (s *Store) GetTurn(turnID string) (Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return Turn{}, ErrStoreClosed
	}
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
	items, err := s.ListTurnsByOperationID(operationID, 1)
	if err != nil {
		return Turn{}, err
	}
	if len(items) == 0 {
		return Turn{}, ErrNotFound
	}
	return items[0], nil
}

func (s *Store) ListTurnsByOperationID(operationID string, limit int) ([]Turn, error) {
	if limit <= 0 {
		limit = 2
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, ErrStoreClosed
	}
	rows, err := s.db.Query("SELECT body FROM turns WHERE operation_id=? LIMIT ?", operationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
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

func (s *Store) ListTurnsByWorker(workerID string, limit int) ([]Turn, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, ErrStoreClosed
	}
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
	if s.db == nil {
		return nil, ErrStoreClosed
	}
	rows, err := s.db.Query(
		"SELECT body FROM turns WHERE state IN (?,?,?,?,?) ORDER BY updated_at ASC",
		TurnPrepared, TurnRunning, TurnWaitingApproval, TurnWaitingInput, TurnReconcile,
	)
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
	if s.db == nil {
		return 0, false, ErrStoreClosed
	}
	if err := s.saveResultWithCapLocked(turnID, raw, now); err != nil {
		return 0, truncated, err
	}
	return len(raw), truncated, nil
}

// HasResultText checks only the turn_results metadata row without touching
// turn_result_chunks or inline payload bytes.
func (s *Store) HasResultText(turnID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return false, ErrStoreClosed
	}
	var exists int
	err := s.db.QueryRow("SELECT 1 FROM turn_results WHERE turn_id=?", turnID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ReadResultText(turnID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return "", false, ErrStoreClosed
	}
	var (
		total int
		b     []byte
	)
	err := s.db.QueryRow("SELECT byte_len, body FROM turn_results WHERE turn_id=?", turnID).Scan(&total, &b)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if len(b) > 0 || total <= 0 {
		return string(b), true, nil
	}
	rows, err := s.db.Query(
		"SELECT body FROM turn_result_chunks WHERE turn_id=? ORDER BY chunk_idx ASC",
		turnID,
	)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	out := make([]byte, 0, total)
	for rows.Next() {
		var chunk []byte
		if err := rows.Scan(&chunk); err != nil {
			return "", false, err
		}
		out = append(out, chunk...)
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	return string(out), true, nil
}

// ReadResultRange reads up to maxBytes raw bytes starting at byte offset from
// turn_result_chunks (with inline fallback for legacy turn_results rows) so
// both SQLite page I/O and Go allocations scale with maxBytes rather than the
// full stored result size.
func (s *Store) ReadResultRange(turnID string, offset, maxBytes int) (string, int, bool, error) {
	if offset < 0 {
		offset = 0
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return "", 0, false, ErrStoreClosed
	}
	var (
		total     int
		inlineLen int
	)
	err := s.db.QueryRow(
		"SELECT byte_len, LENGTH(body) FROM turn_results WHERE turn_id=?",
		turnID,
	).Scan(&total, &inlineLen)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	if total <= 0 || offset >= total || maxBytes == 0 {
		return "", total, true, nil
	}
	if inlineLen > 0 {
		var b []byte
		if err := s.db.QueryRow(
			"SELECT SUBSTR(CAST(body AS BLOB), ?, ?) FROM turn_results WHERE turn_id=?",
			offset+1, maxBytes, turnID,
		).Scan(&b); err != nil {
			return "", 0, false, err
		}
		return string(b), total, true, nil
	}

	rem := total - offset
	want := maxBytes
	if want > rem {
		want = rem
	}
	end := offset + want
	firstChunk := offset / resultChunkBytes
	lastChunk := (end - 1) / resultChunkBytes
	rows, err := s.db.Query(
		`SELECT chunk_idx, body FROM turn_result_chunks
		 WHERE turn_id=? AND chunk_idx>=? AND chunk_idx<=?
		 ORDER BY chunk_idx ASC`,
		turnID, firstChunk, lastChunk,
	)
	if err != nil {
		return "", 0, false, err
	}
	defer rows.Close()

	out := make([]byte, 0, want)
	for rows.Next() {
		var (
			chunkIdx int
			chunk    []byte
		)
		if err := rows.Scan(&chunkIdx, &chunk); err != nil {
			return "", 0, false, err
		}
		chunkStart := chunkIdx * resultChunkBytes
		lo := 0
		if offset > chunkStart {
			lo = offset - chunkStart
		}
		hi := len(chunk)
		if end-chunkStart < hi {
			hi = end - chunkStart
		}
		if lo < hi && lo < len(chunk) {
			out = append(out, chunk[lo:hi]...)
		}
	}
	if err := rows.Err(); err != nil {
		return "", 0, false, err
	}
	return string(out), total, true, nil
}

func (s *Store) AppendLog(turnID string, entry LogEntry) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return 0, ErrStoreClosed
	}
	return s.appendLogWithCapLocked(turnID, entry)
}

func (s *Store) ReadLogs(turnID string, afterSeq int64, limit int) ([]LogEntry, bool, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, false, ErrStoreClosed
	}
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
	return s.ReadLogsTailBefore(turnID, 0, limit)
}

func (s *Store) ReadLogsTailBefore(turnID string, beforeSeq int64, limit int) ([]LogEntry, bool, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, false, ErrStoreClosed
	}
	query := "SELECT body FROM turn_logs WHERE turn_id=?"
	args := []any{turnID}
	if beforeSeq > 0 {
		query += " AND seq<?"
		args = append(args, beforeSeq)
	}
	query += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := s.db.Query(query, args...)
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
