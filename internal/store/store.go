package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/jackgan90/Asterhand/internal/model"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("相同请求 ID 的内容不同")
var ErrNotFound = errors.New("记录不存在")
var ErrEventLimit = errors.New("任务普通事件预算已满，停止新操作")
var ErrUnchanged = errors.New("记录已处于目标状态")

const EventLimit = 4096
const ControlEventReserve = 16

type Store struct {
	mu sync.Mutex
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	var schema int
	if err = db.QueryRow("PRAGMA user_version").Scan(&schema); err != nil {
		db.Close()
		return nil, err
	}
	if schema > 1 {
		db.Close()
		return nil, fmt.Errorf("数据库版本 %d 高于当前支持版本 1，请使用较新的 Asterhand", schema)
	}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA busy_timeout=5000;
	CREATE TABLE IF NOT EXISTS records(kind TEXT NOT NULL, id TEXT NOT NULL, body BLOB NOT NULL, PRIMARY KEY(kind,id));
	CREATE TABLE IF NOT EXISTS events(job TEXT NOT NULL, seq INTEGER NOT NULL, body BLOB NOT NULL, PRIMARY KEY(job,seq));
	CREATE TABLE IF NOT EXISTS dedup(origin TEXT NOT NULL, request TEXT NOT NULL, digest TEXT NOT NULL, job TEXT NOT NULL, PRIMARY KEY(origin,request));
	PRAGMA user_version=1;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }

type queryer interface{ QueryRow(string, ...any) *sql.Row }
type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

func get(q queryer, kind, id string, v any) error {
	var b []byte
	err := q.QueryRow("SELECT body FROM records WHERE kind=? AND id=?", kind, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func put(q execer, kind, id string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = q.Exec("INSERT INTO records(kind,id,body) VALUES(?,?,?) ON CONFLICT(kind,id) DO UPDATE SET body=excluded.body", kind, id, b)
	return err
}
func (s *Store) Get(kind, id string, v any) error { return get(s.db, kind, id, v) }
func (s *Store) Put(kind, id string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return put(s.db, kind, id, v)
}
func (s *Store) Modify(kind, id string, fn func([]byte) (any, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var b []byte
	if err = tx.QueryRow("SELECT body FROM records WHERE kind=? AND id=?", kind, id).Scan(&b); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	v, err := fn(b)
	if err != nil {
		return err
	}
	if err = put(tx, kind, id, v); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) List(kind string, v any) error {
	rows, err := s.db.Query("SELECT body FROM records WHERE kind=? ORDER BY rowid DESC", kind)
	if err != nil {
		return err
	}
	defer rows.Close()
	all := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return err
		}
		all = append(all, b)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func event(tx *sql.Tx, j *model.Job, typ string, data any) error {
	j.Seq++
	j.Updated = model.Now()
	e := model.Event{Seq: j.Seq, JobID: j.ID, Attempt: j.Attempt, Type: typ, Time: j.Updated, Data: data}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(b) > 64*1024 {
		return errors.New("事件超过 64 KiB 上限")
	}
	_, err = tx.Exec("INSERT INTO events(job,seq,body) VALUES(?,?,?)", j.ID, j.Seq, b)
	if err != nil {
		return err
	}
	return put(tx, "job", j.ID, j)
}
func (s *Store) Accept(owner, origin string, r model.Request) (model.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	var id, digest string
	err = tx.QueryRow("SELECT job,digest FROM dedup WHERE origin=? AND request=?", origin, r.RequestID).Scan(&id, &digest)
	if err == nil {
		var j model.Job
		if digest != model.Hash(r) {
			return j, ErrConflict
		}
		err = get(tx, "job", id, &j)
		return j, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Job{}, err
	}
	j := model.Job{ID: model.ID(), Origin: origin, Owner: owner, Request: r, Digest: model.Hash(r), State: "queued", Created: model.Now(), Execution: "not_run", Verification: "not_run", Acceptance: "pending"}
	var tomb bool
	if err = get(tx, "cancel", model.Hash([]string{origin, r.RequestID}), &tomb); err != nil && !errors.Is(err, ErrNotFound) {
		return j, err
	}
	if tomb {
		j.State = "cancelled"
		j.Execution = "not_started"
	}
	_, err = tx.Exec("INSERT INTO dedup(origin,request,digest,job) VALUES(?,?,?,?)", origin, r.RequestID, j.Digest, j.ID)
	if err != nil {
		return j, err
	}
	if err = event(tx, &j, "accepted", map[string]string{"state": j.State}); err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *Store) Change(id, typ string, fn func(*model.Job) (any, error)) (model.Job, error) {
	return s.change(id, typ, false, fn)
}

// ChangeControl is only for completing accepted work or ending/reconciling a job.
// Callers must make repeated requests idempotent to preserve the bounded reserve.
func (s *Store) ChangeControl(id, typ string, fn func(*model.Job) (any, error)) (model.Job, error) {
	return s.change(id, typ, true, fn)
}
func (s *Store) change(id, typ string, control bool, fn func(*model.Job) (any, error)) (model.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return model.Job{}, err
	}
	defer tx.Rollback()
	var j model.Job
	if err = get(tx, "job", id, &j); err != nil {
		return j, err
	}
	data, err := fn(&j)
	if errors.Is(err, ErrUnchanged) {
		// Return the durable snapshot, never mutations made by a no-op callback.
		var current model.Job
		err = get(tx, "job", id, &current)
		return current, err
	}
	if err != nil {
		return j, err
	}
	if err = checkEventBudget(j.Seq, control); err != nil {
		return j, err
	}
	if err = event(tx, &j, typ, data); err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func checkEventBudget(seq int64, control bool) error {
	limit := int64(EventLimit)
	if control {
		limit += ControlEventReserve
	}
	if seq >= limit {
		return ErrEventLimit
	}
	return nil
}
func (s *Store) Cancel(origin, request string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return put(s.db, "cancel", model.Hash([]string{origin, request}), true)
}
func (s *Store) Events(id string, after int64) ([]model.Event, error) {
	if after < 0 {
		return nil, fmt.Errorf("无效游标")
	}
	rows, err := s.db.Query("SELECT body FROM events WHERE job=? AND seq>? ORDER BY seq LIMIT 200", id, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var b []byte
		var e model.Event
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
