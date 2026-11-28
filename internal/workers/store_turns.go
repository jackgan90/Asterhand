package workers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// sqlExecer is satisfied by both *sql.DB and *sql.Tx so turn/worker upserts
// share one statement definition across single writes and transactions.
type sqlExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// activeTurnStates lists states whose duration is still growing.
var activeTurnStates = []string{TurnPrepared, TurnRunning, TurnWaitingApproval, TurnWaitingInput}

// attentionTurnStates lists states surfaced by the "attention" timeline filter:
// non-success terminal outcomes, reconciliation, and turns waiting on a person.
var attentionTurnStates = []string{
	TurnFailed, TurnCancelled, TurnCancelledRevoked, TurnInterrupted, TurnReconcile,
	TurnBlockedApproval, TurnBlockedUserInput, TurnWaitingApproval, TurnWaitingInput,
}

func timeMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func finishedMillis(t Turn) int64 {
	if t.FinishedAt == nil {
		return 0
	}
	return timeMillis(*t.FinishedAt)
}

func upsertTurnExec(ex sqlExecer, t Turn, body []byte) error {
	opKey := dedupKey(t.OperationKey, t.OperationID)
	_, err := ex.Exec(
		`INSERT INTO turns(turn_id, worker_id, workspace_id, operation_id, client_correlation_id, state, body, updated_at, turn_seq, started_at, finished_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(turn_id) DO UPDATE SET
		   state=excluded.state,
		   body=excluded.body,
		   updated_at=excluded.updated_at,
		   turn_seq=CASE WHEN excluded.turn_seq > 0 THEN excluded.turn_seq ELSE turns.turn_seq END,
		   started_at=excluded.started_at,
		   finished_at=excluded.finished_at`,
		t.ID, t.WorkerID, t.WorkspaceID, opKey, t.ClientCorrelationID, t.State, body, t.UpdatedAt.UnixMilli(),
		t.TurnSeq, timeMillis(t.StartedAt), finishedMillis(t),
	)
	return err
}

func upsertWorkerExec(ex sqlExecer, w Worker, body []byte) error {
	opKey := dedupKey(w.OperationKey, w.OperationID)
	_, err := ex.Exec(
		`INSERT INTO workers(worker_id, workspace_id, alias, operation_id, state, body, updated_at)
		 VALUES(?,?,?,?,?,?,?)
		 ON CONFLICT(worker_id) DO UPDATE SET
		   workspace_id=excluded.workspace_id,
		   alias=excluded.alias,
		   state=excluded.state,
		   body=excluded.body,
		   updated_at=excluded.updated_at`,
		w.ID, w.WorkspaceID, w.Alias, opKey, w.State, body, w.UpdatedAt.UnixMilli(),
	)
	return err
}

// decodeTurnRow restores a turn body and prefers the indexed turn_seq column
// when the JSON body lost it (for example after an older binary re-saved it).
func decodeTurnRow(seq int, body []byte) (Turn, error) {
	var t Turn
	if err := json.Unmarshal(body, &t); err != nil {
		return Turn{}, err
	}
	if t.TurnSeq == 0 && seq > 0 {
		t.TurnSeq = seq
	}
	return t, nil
}

// migrateSchema performs a one-time forward upgrade to DBSchemaVersion (v2).
// Fresh databases (fromSchema == 0) already created all v2 tables/columns above
// and only record user_version=2; v1 databases add missing timeline columns,
// drop any interim trigger, backfill start/finish timestamps once, and set
// user_version=2 so normal startups incur zero migration work.
func (s *Store) migrateSchema(fromSchema int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrStoreClosed
	}
	if fromSchema >= 1 {
		cols := map[string]bool{}
		rows, err := s.db.Query(`PRAGMA table_info(turns)`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var (
				cid     int
				name    string
				ctype   string
				notnull int
				dflt    sql.NullString
				pk      int
			)
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			cols[name] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, name := range []string{"turn_seq", "started_at", "finished_at"} {
			if cols[name] {
				continue
			}
			if _, err := s.db.Exec(`ALTER TABLE turns ADD COLUMN ` + name + ` INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
		if _, err := s.db.Exec(`
			DROP TRIGGER IF EXISTS trg_turns_after_delete;
			CREATE INDEX IF NOT EXISTS idx_turns_worker_started ON turns(worker_id, started_at DESC, turn_seq DESC, turn_id DESC);
		`); err != nil {
			return err
		}
		if err := s.backfillTurnTimelineLocked(); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(`PRAGMA user_version = ` + strconv.Itoa(DBSchemaVersion))
	return err
}

// backfillTurnTimelineLocked populates timeline columns once during v1 -> v2 upgrade.
func (s *Store) backfillTurnTimelineLocked() error {
	rows, err := s.db.Query(
		`SELECT turn_id, body FROM turns
		 WHERE started_at = 0 OR (finished_at = 0 AND state NOT IN (?,?,?,?,?))`,
		TurnPrepared, TurnRunning, TurnWaitingApproval, TurnWaitingInput, TurnReconcile,
	)
	if err != nil {
		return err
	}
	type fix struct {
		id                string
		seq               int
		started, finished int64
	}
	var fixes []fix
	for rows.Next() {
		var (
			id   string
			body []byte
		)
		if err := rows.Scan(&id, &body); err != nil {
			rows.Close()
			return err
		}
		var t Turn
		if err := json.Unmarshal(body, &t); err != nil {
			continue
		}
		fixes = append(fixes, fix{id: id, seq: t.TurnSeq, started: timeMillis(t.StartedAt), finished: finishedMillis(t)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(fixes) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, f := range fixes {
		if _, err := tx.Exec(
			`UPDATE turns SET started_at=?, finished_at=?, turn_seq=CASE WHEN turn_seq=0 THEN ? ELSE turn_seq END WHERE turn_id=?`,
			f.started, f.finished, f.seq, f.id,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NextTurnSeq returns the next per-worker sequence number. It never reuses a
// number even after older turns were pruned, and continues after legacy turns
// that predate sequence numbers.
func (s *Store) NextTurnSeq(workerID string, workerLast int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return 0, ErrStoreClosed
	}
	var maxSeq, count, pruned int
	if err := s.db.QueryRow(
		`SELECT COALESCE(MAX(turn_seq),0), COUNT(*),
		        COALESCE((SELECT pruned_turns FROM worker_history_pruned WHERE worker_id=?), 0)
		 FROM turns WHERE worker_id=?`,
		workerID, workerID,
	).Scan(&maxSeq, &count, &pruned); err != nil {
		return 0, err
	}
	next := workerLast
	if maxSeq > next {
		next = maxSeq
	}
	if count+pruned > next {
		next = count + pruned
	}
	return next + 1, nil
}

// PrunedTurnCount returns how many terminal turns belonging to workerID have
// been deleted by summary retention.
func (s *Store) PrunedTurnCount(workerID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return 0, ErrStoreClosed
	}
	var pruned int
	err := s.db.QueryRow(`SELECT pruned_turns FROM worker_history_pruned WHERE worker_id=?`, workerID).Scan(&pruned)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return pruned, err
}

// SaveNewTurn atomically persists a newly admitted turn, its worker update, and
// the caller's original prompt. The prompt counts toward the node detail
// budget; when even eviction cannot make room the turn is still admitted and
// only the summary preview remains.
func (s *Store) SaveNewTurn(t Turn, w *Worker, prompt string, now time.Time) (bool, error) {
	tb, err := json.Marshal(t)
	if err != nil {
		return false, err
	}
	var wb []byte
	if w != nil {
		if wb, err = json.Marshal(*w); err != nil {
			return false, err
		}
	}
	raw := []byte(prompt)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return false, ErrStoreClosed
	}
	curTotal, err := s.cachedDetailUsageLocked()
	if err != nil {
		return false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertTurnExec(tx, t, tb); err != nil {
		return false, err
	}
	if w != nil {
		if err := upsertWorkerExec(tx, *w, wb); err != nil {
			return false, err
		}
	}
	stored := false
	newTotal := curTotal
	size := int64(len(raw))
	if size > 0 {
		if curTotal+size > MaxTotalDetailBytes {
			if newTotal, err = evictTerminalDetailsForCapTx(tx, t.ID, MaxTotalDetailBytes-size); err != nil {
				return false, err
			}
		}
		if newTotal+size <= MaxTotalDetailBytes {
			if _, err := tx.Exec(
				`INSERT INTO turn_prompts(turn_id, body, byte_len, updated_at) VALUES(?,?,?,?)
				 ON CONFLICT(turn_id) DO NOTHING`,
				t.ID, raw, len(raw), now.UnixMilli(),
			); err != nil {
				return false, err
			}
			newTotal += size
			stored = true
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	s.detailBytes = newTotal
	s.detailBytesLoaded = true
	if w != nil {
		s.bumpWorkerRevLocked(w.WorkspaceID)
	}
	return stored, nil
}

// ReadPromptRange reads up to maxBytes raw prompt bytes starting at offset.
// ok is false when no prompt row exists (never recorded or evicted).
func (s *Store) ReadPromptRange(turnID string, offset, maxBytes int) (string, int, bool, error) {
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
		total int
		b     []byte
	)
	err := s.db.QueryRow(
		`SELECT byte_len, SUBSTR(CAST(body AS BLOB), ?, ?) FROM turn_prompts WHERE turn_id=?`,
		offset+1, maxBytes, turnID,
	).Scan(&total, &b)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	if offset >= total {
		return "", total, true, nil
	}
	return string(b), total, true, nil
}

// EncodeTurnListAfter encodes a timeline keyset position (start time, stored
// sequence number, turn ID). Pass 0 as seq for legacy turns without one.
func EncodeTurnListAfter(startedAt time.Time, seq int, turnID string) string {
	return strconv.FormatInt(timeMillis(startedAt), 10) + ":" + strconv.Itoa(seq) + ":" + turnID
}

func parseTurnListAfter(raw string) (int64, int, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0, "", nil
	}
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 || strings.TrimSpace(parts[2]) == "" {
		return 0, 0, "", ErrInvalidCursor
	}
	ms, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, "", ErrInvalidCursor
	}
	seq, err := strconv.Atoi(parts[1])
	if err != nil || seq < 0 {
		return 0, 0, "", ErrInvalidCursor
	}
	return ms, seq, strings.TrimSpace(parts[2]), nil
}

// turnTimelineOrder orders a worker's turns newest first. Sequence numbers
// break start-time ties so two turns admitted within one millisecond still
// list in admission order.
const turnTimelineOrder = " ORDER BY started_at DESC, turn_seq DESC, turn_id DESC"

// ListTurnsPage returns a worker's turns in timeline order strictly after the
// given keyset position.
func (s *Store) ListTurnsPage(workerID, after string, limit int, attentionOnly bool) ([]Turn, bool, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	curMS, curSeq, curID, err := parseTurnListAfter(after)
	if err != nil {
		return nil, false, err
	}
	query := "SELECT turn_seq, body FROM turns WHERE worker_id=?"
	args := []any{workerID}
	if attentionOnly {
		query += " AND state IN (" + strings.TrimSuffix(strings.Repeat("?,", len(attentionTurnStates)), ",") + ")"
		for _, st := range attentionTurnStates {
			args = append(args, st)
		}
	}
	if curID != "" {
		query += " AND (started_at, turn_seq, turn_id) < (?, ?, ?)"
		args = append(args, curMS, curSeq, curID)
	}
	query += turnTimelineOrder + " LIMIT ?"
	args = append(args, limit+1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, false, ErrStoreClosed
	}
	out, err := queryTurnRows(s.db, query, args...)
	if err != nil {
		return nil, false, err
	}
	hasMore := false
	if len(out) > limit {
		hasMore = true
		out = out[:limit]
	}
	return out, hasMore, nil
}

func queryTurnRows(db *sql.DB, query string, args ...any) ([]Turn, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Turn{}
	for rows.Next() {
		var (
			seq int
			b   []byte
		)
		if err := rows.Scan(&seq, &b); err != nil {
			return nil, err
		}
		t, err := decodeTurnRow(seq, b)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TurnRanks computes 1-based chronological positions across the worker's full
// retained history for the requested turn IDs in a single covering-index query,
// preserving global turn numbers even under an attention-filtered view.
func (s *Store) TurnRanks(workerID string, turnIDs []string) (map[string]int, error) {
	if len(turnIDs) == 0 {
		return nil, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(turnIDs)), ",")
	query := `WITH ranked AS (
		SELECT turn_id,
		       COUNT(*) OVER () + 1 - ROW_NUMBER() OVER (` + turnTimelineOrder + `) AS rk
		FROM turns
		WHERE worker_id = ?
	)
	SELECT turn_id, rk FROM ranked WHERE turn_id IN (` + placeholders + `)`
	args := make([]any, 0, 1+len(turnIDs))
	args = append(args, workerID)
	for _, id := range turnIDs {
		args = append(args, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, ErrStoreClosed
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]int, len(turnIDs))
	for rows.Next() {
		var (
			id string
			rk int
		)
		if err := rows.Scan(&id, &rk); err != nil {
			return nil, err
		}
		out[id] = rk
	}
	return out, rows.Err()
}

// TurnRank returns the 1-based chronological position of a single turn within
// its worker's retained history.
func (s *Store) TurnRank(workerID string, _ time.Time, turnID string) (int, error) {
	ranks, err := s.TurnRanks(workerID, []string{turnID})
	if err != nil {
		return 0, err
	}
	return ranks[turnID], nil
}

type turnStateAggregate struct {
	State       string
	Count       int
	MinStarted  int64
	MaxFinished int64
	MaxSeq      int
	TotalMillis int64
}

// AggregateTurns groups a worker's retained turns by state with duration sums:
// finished turns use finish-start, active turns run to nowMS, and unfinished
// non-active turns (for example awaiting reconciliation) use their last update.
func (s *Store) AggregateTurns(workerID string, nowMS int64) ([]turnStateAggregate, error) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(activeTurnStates)), ",")
	query := `SELECT state, COUNT(*), COALESCE(MIN(started_at),0), COALESCE(MAX(finished_at),0), COALESCE(MAX(turn_seq),0),
		COALESCE(SUM(CASE
			WHEN finished_at > 0 THEN MAX(finished_at - started_at, 0)
			WHEN state IN (` + placeholders + `) THEN MAX(? - started_at, 0)
			ELSE MAX(updated_at - started_at, 0) END), 0)
		FROM turns WHERE worker_id=? AND started_at > 0 GROUP BY state`
	args := []any{}
	for _, st := range activeTurnStates {
		args = append(args, st)
	}
	args = append(args, nowMS, workerID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, ErrStoreClosed
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []turnStateAggregate
	for rows.Next() {
		var a turnStateAggregate
		if err := rows.Scan(&a.State, &a.Count, &a.MinStarted, &a.MaxFinished, &a.MaxSeq, &a.TotalMillis); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
