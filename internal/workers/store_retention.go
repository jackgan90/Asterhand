package workers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
)

func (s *Store) initDetailBytes() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrStoreClosed
	}
	total, err := s.detailUsageLocked()
	if err != nil {
		return err
	}
	s.detailBytes = total
	s.detailBytesLoaded = true
	return nil
}

func (s *Store) cachedDetailUsageLocked() (int64, error) {
	if s.detailBytesLoaded {
		return s.detailBytes, nil
	}
	total, err := s.detailUsageLocked()
	if err != nil {
		return 0, err
	}
	s.detailBytes = total
	s.detailBytesLoaded = true
	return total, nil
}

func (s *Store) detailUsageLocked() (int64, error) {
	var total int64
	err := s.db.QueryRow(
		`SELECT (SELECT COALESCE(SUM(byte_len),0) FROM turn_results) +
		        (SELECT COALESCE(SUM(byte_len),0) FROM turn_logs)`,
	).Scan(&total)
	return total, err
}

func detailUsageTx(tx *sql.Tx) (int64, error) {
	var total int64
	err := tx.QueryRow(
		`SELECT (SELECT COALESCE(SUM(byte_len),0) FROM turn_results) +
		        (SELECT COALESCE(SUM(byte_len),0) FROM turn_logs)`,
	).Scan(&total)
	return total, err
}

// evictTerminalDetailsForCapTx evicts result and log details of the oldest
// terminal turns until total node detail bytes (turn_results + turn_logs) are
// at or below targetMaxBytes. Active and reconciliation_required turns, as well
// as keepTurnID, are never evicted.
func evictTerminalDetailsForCapTx(tx *sql.Tx, keepTurnID string, targetMaxBytes int64) (int64, error) {
	total, err := detailUsageTx(tx)
	if err != nil || total <= targetMaxBytes {
		return total, err
	}
	type candidate struct {
		turnID      string
		detailBytes int64
	}
	rows, err := tx.Query(
		`SELECT t.turn_id,
		        COALESCE((SELECT byte_len FROM turn_results r WHERE r.turn_id = t.turn_id), 0) +
		        COALESCE((SELECT SUM(byte_len) FROM turn_logs l WHERE l.turn_id = t.turn_id), 0) AS detail_bytes
		 FROM turns t
		 WHERE t.state NOT IN (?,?,?,?,?)
		   AND t.turn_id <> ?
		   AND (
		     EXISTS (SELECT 1 FROM turn_results r WHERE r.turn_id = t.turn_id)
		     OR EXISTS (SELECT 1 FROM turn_logs l WHERE l.turn_id = t.turn_id)
		   )
		 ORDER BY t.updated_at ASC, t.turn_id ASC`,
		TurnPrepared, TurnRunning, TurnWaitingApproval, TurnWaitingInput, TurnReconcile,
		keepTurnID,
	)
	if err != nil {
		return total, err
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if sErr := rows.Scan(&c.turnID, &c.detailBytes); sErr != nil {
			rows.Close()
			return total, sErr
		}
		cands = append(cands, c)
	}
	if rErr := rows.Err(); rErr != nil {
		rows.Close()
		return total, rErr
	}
	rows.Close()

	for _, c := range cands {
		if total <= targetMaxBytes {
			break
		}
		if _, err := tx.Exec(`DELETE FROM turn_result_chunks WHERE turn_id=?`, c.turnID); err != nil {
			return total, err
		}
		if _, err := tx.Exec(`DELETE FROM turn_results WHERE turn_id=?`, c.turnID); err != nil {
			return total, err
		}
		if _, err := tx.Exec(`DELETE FROM turn_logs WHERE turn_id=?`, c.turnID); err != nil {
			return total, err
		}
		total -= c.detailBytes
	}
	return detailUsageTx(tx)
}

func writeResultChunksTx(tx *sql.Tx, turnID string, raw []byte, now time.Time) error {
	if _, err := tx.Exec(
		`INSERT INTO turn_results(turn_id, body, byte_len, updated_at)
		 VALUES(?,?,?,?)
		 ON CONFLICT(turn_id) DO UPDATE SET body=excluded.body, byte_len=excluded.byte_len, updated_at=excluded.updated_at`,
		turnID, []byte{}, len(raw), now.UnixMilli(),
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM turn_result_chunks WHERE turn_id=?`, turnID); err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	stmt, err := tx.Prepare(`INSERT INTO turn_result_chunks(turn_id, chunk_idx, body, byte_len) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for idx, off := 0, 0; off < len(raw); idx, off = idx+1, off+resultChunkBytes {
		end := off + resultChunkBytes
		if end > len(raw) {
			end = len(raw)
		}
		chunk := raw[off:end]
		if _, err := stmt.Exec(turnID, idx, chunk, len(chunk)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) saveResultWithCapLocked(turnID string, raw []byte, now time.Time) error {
	var existingLen int64
	err := s.db.QueryRow(`SELECT byte_len FROM turn_results WHERE turn_id=?`, turnID).Scan(&existingLen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	curTotal, err := s.cachedDetailUsageLocked()
	if err != nil {
		return err
	}
	delta := int64(len(raw)) - existingLen

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if curTotal+delta <= MaxTotalDetailBytes {
		if err := writeResultChunksTx(tx, turnID, raw, now); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.detailBytes += delta
		return nil
	}

	targetBeforeWrite := int64(MaxTotalDetailBytes) - delta
	remaining, err := evictTerminalDetailsForCapTx(tx, turnID, targetBeforeWrite)
	if err != nil {
		return err
	}
	if remaining+delta > MaxTotalDetailBytes {
		return automation.Fail("capacity", "Node worker detail budget reached")
	}
	if err := writeResultChunksTx(tx, turnID, raw, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.detailBytes = remaining + delta
	s.detailBytesLoaded = true
	return nil
}

func (s *Store) appendLogWithCapLocked(turnID string, entry LogEntry) (int64, error) {
	var (
		total   int
		lastSeq int64
	)
	_ = s.db.QueryRow(
		"SELECT COALESCE(SUM(byte_len),0), COALESCE(MAX(seq),0) FROM turn_logs WHERE turn_id=?",
		turnID,
	).Scan(&total, &lastSeq)
	if total >= MaxTurnLogBytes {
		return 0, automation.Fail("capacity", "Turn log budget reached")
	}
	entry.Seq = lastSeq + 1
	b, err := json.Marshal(entry)
	if err != nil {
		return 0, err
	}
	if total+len(b) > MaxTurnLogBytes {
		return 0, automation.Fail("capacity", "Turn log budget reached")
	}

	curTotal, err := s.cachedDetailUsageLocked()
	if err != nil {
		return 0, err
	}
	if curTotal+int64(len(b)) <= MaxTotalDetailBytes {
		_, err = s.db.Exec("INSERT INTO turn_logs(turn_id, seq, body, byte_len) VALUES(?,?,?,?)", turnID, entry.Seq, b, len(b))
		if err == nil {
			s.detailBytes += int64(len(b))
		}
		return entry.Seq, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	remaining, err := evictTerminalDetailsForCapTx(tx, turnID, int64(MaxTotalDetailBytes)-int64(len(b)))
	if err != nil {
		return 0, err
	}
	if remaining+int64(len(b)) > MaxTotalDetailBytes {
		return 0, automation.Fail("capacity", "Node worker detail budget reached")
	}
	if _, err = tx.Exec("INSERT INTO turn_logs(turn_id, seq, body, byte_len) VALUES(?,?,?,?)", turnID, entry.Seq, b, len(b)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.detailBytes = remaining + int64(len(b))
	s.detailBytesLoaded = true
	return entry.Seq, nil
}

// PruneDetailsAndCap evicts terminal turn result/log details older than
// DetailRetention (7 days) and enforces MaxTotalDetailBytes (64 MiB), without
// deleting terminal turn summaries. Active and reconciliation_required turns
// are never evicted.
func (s *Store) PruneDetailsAndCap(now time.Time) {
	detailCutoff := now.Add(-DetailRetention).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return
	}
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()

	res1, err := tx.Exec(
		`DELETE FROM turn_results
		 WHERE EXISTS (
		   SELECT 1 FROM turns t
		   WHERE t.turn_id = turn_results.turn_id
		     AND t.state NOT IN (?,?,?,?,?)
		     AND (t.updated_at < ? OR turn_results.updated_at < ?)
		 )`,
		TurnPrepared, TurnRunning, TurnWaitingApproval, TurnWaitingInput, TurnReconcile,
		detailCutoff, detailCutoff,
	)
	if err != nil {
		return
	}
	res2, err := tx.Exec(
		`DELETE FROM turn_logs
		 WHERE EXISTS (
		   SELECT 1 FROM turns t
		   WHERE t.turn_id = turn_logs.turn_id
		     AND t.state NOT IN (?,?,?,?,?)
		     AND t.updated_at < ?
		 )`,
		TurnPrepared, TurnRunning, TurnWaitingApproval, TurnWaitingInput, TurnReconcile,
		detailCutoff,
	)
	if err != nil {
		return
	}
	n1, _ := res1.RowsAffected()
	if n1 > 0 {
		if _, err := tx.Exec(`DELETE FROM turn_result_chunks WHERE NOT EXISTS (SELECT 1 FROM turn_results r WHERE r.turn_id = turn_result_chunks.turn_id)`); err != nil {
			return
		}
	}
	n2, _ := res2.RowsAffected()
	needRecount := n1 > 0 || n2 > 0 || !s.detailBytesLoaded || s.detailBytes > MaxTotalDetailBytes
	var newUsage int64
	if needRecount {
		newUsage, err = evictTerminalDetailsForCapTx(tx, "", MaxTotalDetailBytes)
		if err != nil {
			return
		}
	}
	if err := tx.Commit(); err != nil {
		return
	}
	if needRecount {
		s.detailBytes = newUsage
		s.detailBytesLoaded = true
	}
}

// ListExpiredTerminalTurns returns terminal turns older than SummaryRetention
// (30 days) with OperationKey hydrated from the indexed operation_id column.
func (s *Store) ListExpiredTerminalTurns(now time.Time) ([]Turn, error) {
	summaryCutoff := now.Add(-SummaryRetention).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil, ErrStoreClosed
	}
	rows, err := s.db.Query(
		`SELECT operation_id, body FROM turns
		 WHERE state NOT IN (?,?,?,?,?)
		   AND updated_at < ?
		 ORDER BY updated_at ASC, turn_id ASC`,
		TurnPrepared, TurnRunning, TurnWaitingApproval, TurnWaitingInput, TurnReconcile,
		summaryCutoff,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Turn
	for rows.Next() {
		var (
			opKey string
			b     []byte
		)
		if err := rows.Scan(&opKey, &b); err != nil {
			return nil, err
		}
		var t Turn
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		t.OperationKey = opKey
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteExpiredTerminalTurns removes terminal turn summaries older than
// SummaryRetention (30 days), excluding any turn IDs in keepTurnIDs.
func (s *Store) DeleteExpiredTerminalTurns(now time.Time, keepTurnIDs []string) error {
	summaryCutoff := now.Add(-SummaryRetention).UnixMilli()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return ErrStoreClosed
	}
	args := []any{
		TurnPrepared, TurnRunning, TurnWaitingApproval, TurnWaitingInput, TurnReconcile,
		summaryCutoff,
	}
	query := `DELETE FROM turns WHERE state NOT IN (?,?,?,?,?) AND updated_at < ?`
	if len(keepTurnIDs) > 0 {
		placeholders := make([]string, 0, len(keepTurnIDs))
		for _, id := range keepTurnIDs {
			if id == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		if len(placeholders) > 0 {
			query += " AND turn_id NOT IN (" + strings.Join(placeholders, ",") + ")"
		}
	}
	_, err := s.db.Exec(query, args...)
	return err
}

// PruneExpiredDetails enforces worker history retention and node capacity rules:
// 1. Evict terminal turn result/log details older than DetailRetention (7 days).
// 2. Enforce MaxTotalDetailBytes (64 MiB) across retained results and logs.
// 3. Evict terminal turn summaries older than SummaryRetention (30 days).
// Active and reconciliation_required turns are never evicted.
func (s *Store) PruneExpiredDetails(now time.Time) {
	s.PruneDetailsAndCap(now)
	_ = s.DeleteExpiredTerminalTurns(now, nil)
}
