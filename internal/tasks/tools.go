package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/store"
)

const MaxText = 64 * 1024

type ToolCall struct {
	OperationID string          `json:"operation_id"`
	Tool        string          `json:"tool"`
	Arguments   json.RawMessage `json:"arguments"`
}

func (e *Engine) Tool(ctx context.Context, token string, c ToolCall) (result any, err error) {
	e.mu.Lock()
	a := e.active[token]
	e.mu.Unlock()
	if a == nil {
		return nil, i18n.Err("err.tasks.tool_token_expired")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	defer func() {
		if errors.Is(err, store.ErrEventLimit) {
			a.cancel()
		}
	}()
	if err := a.ctx.Err(); err != nil {
		return nil, err
	}
	if c.OperationID == "" || len(c.OperationID) > 200 {
		return nil, i18n.Err("err.tasks.tool_missing_op_id")
	}
	var j model.Job
	if err := e.Store.Get("job", a.job, &j); err != nil {
		return nil, err
	}
	if j.Attempt != a.attempt || model.Terminal(j.State) || j.State == "cancel_requested" {
		return nil, i18n.Err("err.tasks.tool_attempt_closed")
	}
	switch c.Tool {
	case "asterhand_create_text", "asterhand_read_text":
		if a.mode != "execute" {
			return nil, i18n.Err("err.tasks.route_no_file_perm")
		}
		return e.fileTool(a, j, c)
	case "asterhand_ask_user":
		return e.ask(a, j, c)
	case "asterhand_submit":
		if a.mode != "route" || e.SubmitRemote == nil {
			return nil, i18n.Err("err.tasks.execute_no_redelegate")
		}
		var args Submission
		if err := json.Unmarshal(c.Arguments, &args); err != nil {
			return nil, err
		}
		if len(args.Goal) == 0 || len(args.Goal) > 16000 {
			return nil, i18n.Err("err.tasks.invalid_goal")
		}
		if _, err := e.Store.Change(j.ID, "submission_started", func(current *model.Job) (any, error) {
			return nil, activeJob(a, current)
		}); err != nil {
			return nil, err
		}
		if err := a.ctx.Err(); err != nil {
			return nil, err
		}
		o, err := e.SubmitRemote(j, args)
		if err != nil {
			return nil, err
		}
		_, err = e.Store.ChangeControl(j.ID, "submitted", func(j *model.Job) (any, error) {
			if j.RemoteID == o.ID && j.RemoteOwner == o.PeerID {
				return nil, store.ErrUnchanged
			}
			j.RemoteID = o.ID
			j.RemoteOwner = o.PeerID
			return map[string]string{"submission_id": o.ID, "owner": o.PeerID}, nil
		})
		return map[string]string{"submission_id": o.ID, "state": "queued_for_delivery", "owner": o.PeerID}, err
	default:
		return nil, i18n.Err("err.tasks.unauthorized_tool")
	}
}
func safePath(path string) error {
	if path == "" || len(path) > 500 || strings.ContainsAny(path, "\x00:") || filepath.IsAbs(path) || !filepath.IsLocal(path) {
		return i18n.Err("err.tasks.relative_path_required")
	}
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." || part == "." || strings.TrimRight(part, " .") != part {
			return i18n.Err("err.tasks.path_traversal")
		}
	}
	return nil
}
func (e *Engine) fileTool(a *active, j model.Job, c ToolCall) (any, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(c.Arguments, &args); err != nil {
		return nil, err
	}
	if err := safePath(args.Path); err != nil {
		return nil, err
	}
	if len(args.Content) > MaxText || !utf8.ValidString(args.Content) {
		return nil, i18n.Err("err.tasks.text_utf8_limit")
	}
	p, err := e.project(j.Request.ProjectID)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(p.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if c.Tool == "asterhand_read_text" {
		b, err := readText(root, args.Path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": filepath.Join(p.Root, args.Path), "content": string(b), "sha256": model.Digest(b)}, nil
	}
	opID := model.Hash([]string{j.ID, j.Attempt, c.OperationID})
	op := model.Operation{ID: opID, JobID: j.ID, Attempt: j.Attempt, Digest: model.Hash(args), State: "prepared"}
	var existing model.Operation
	err = e.Store.Get("operation", opID, &existing)
	if err == nil {
		if existing.Digest != op.Digest {
			return nil, store.ErrConflict
		}
		if existing.State == "completed" {
			return existing.Artifact, e.Store.CompleteOperation(existing)
		}
		return nil, i18n.Err("err.tasks.op_pending_reconcile")
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if _, err = e.Store.Change(j.ID, "tool_started", func(j *model.Job) (any, error) {
		return map[string]string{"operation": opID, "path": args.Path}, activeJob(a, j)
	}); err != nil {
		return nil, err
	}
	if err = e.Store.Put("operation", opID, op); err != nil {
		return nil, err
	}
	if delay := e.Config.Get().AcceptanceDelay; delay > 0 {
		select {
		case <-a.ctx.Done():
			op.State = "no_effect"
			e.Store.Put("operation", opID, op)
			return nil, a.ctx.Err()
		case <-time.After(time.Duration(delay) * time.Second):
		}
	}
	if err = a.ctx.Err(); err != nil {
		op.State = "no_effect"
		e.Store.Put("operation", opID, op)
		return nil, err
	}
	f, err := root.OpenFile(args.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		op.State = "no_effect"
		e.Store.Put("operation", opID, op)
		return nil, i18n.Wrap("err.tasks.file_create_failed", nil, err)
	}
	_, err = io.WriteString(f, args.Content)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	b, err := readText(root, args.Path)
	if err != nil {
		return nil, err
	}
	if string(b) != args.Content {
		return nil, i18n.Err("err.tasks.readback_mismatch")
	}
	preview := b
	if len(preview) > 8000 {
		preview = preview[:8000]
		for !utf8.Valid(preview) {
			preview = preview[:len(preview)-1]
		}
	}
	art := model.Artifact{ID: model.ID(), Operation: opID, Path: filepath.Join(p.Root, args.Path), Size: len(b), SHA256: model.Digest(b), Preview: string(preview)}
	op.State = "completed"
	op.Artifact = &art
	return art, e.Store.CompleteOperation(op)
}
func readText(root *os.Root, path string) ([]byte, error) {
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, i18n.Err("err.tasks.regular_file_only")
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxText+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxText || !utf8.Valid(b) {
		return nil, i18n.Err("err.tasks.read_utf8_limit")
	}
	return b, nil
}
func (e *Engine) ask(a *active, j model.Job, c ToolCall) (any, error) {
	var args struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(c.Arguments, &args); err != nil {
		return nil, err
	}
	if len(args.Question) == 0 || len(args.Question) > 4000 {
		return nil, i18n.Err("err.tasks.invalid_question_len")
	}
	interaction := model.Interaction{ID: model.Hash([]string{j.ID, j.Attempt, c.OperationID}), Attempt: j.Attempt, NativeID: c.OperationID, Kind: "question", Question: args.Question, Expires: time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339Nano), State: "pending"}
	_, err := e.Store.Change(j.ID, "input_requested", func(j *model.Job) (any, error) {
		if err := activeJob(a, j); err != nil {
			return nil, err
		}
		if j.Interaction != nil && j.Interaction.ID == interaction.ID {
			interaction = *j.Interaction
			return nil, store.ErrUnchanged
		}
		j.Interaction = &interaction
		j.State = "waiting_input"
		return interaction, nil
	})
	if err != nil {
		return nil, err
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return nil, a.ctx.Err()
		case <-tick.C:
		}
		if err = e.Store.Get("job", j.ID, &j); err != nil {
			return nil, err
		}
		if j.Interaction == nil || j.Interaction.ID != interaction.ID {
			return nil, i18n.Err("err.tasks.question_expired")
		}
		if j.Interaction.State == "saved" || j.Interaction.State == "delivered" {
			return e.deliverAnswer(a, interaction.ID)
		}
		deadline, _ := time.Parse(time.RFC3339Nano, interaction.Expires)
		if !time.Now().Before(deadline) {
			return nil, i18n.Err("err.tasks.answer_timeout")
		}
	}
}
func (e *Engine) Answer(id, interactionID, attempt, answerID, answer, actor string) (model.Job, error) {
	if len(answerID) < 8 || len(answer) > 4000 || answer == "" {
		return model.Job{}, i18n.Err("err.tasks.answer_invalid")
	}
	return e.Store.ChangeControl(id, "answer_saved", func(j *model.Job) (any, error) {
		i := j.Interaction
		if i == nil || i.ID != interactionID || i.Attempt != attempt || j.Attempt != attempt {
			return nil, i18n.Err("err.tasks.answer_attempt_mismatch")
		}
		if i.Kind != "question" && actor != "local" {
			return nil, i18n.Err("err.tasks.approval_local_only")
		}
		if i.State == "saved" || i.State == "delivered" {
			if i.AnswerID == answerID && i.Answer == answer {
				return nil, store.ErrUnchanged
			}
			return nil, i18n.Err("err.tasks.answer_already_answered")
		}
		deadline, _ := time.Parse(time.RFC3339Nano, i.Expires)
		if i.State != "pending" || j.State != "waiting_input" || !time.Now().Before(deadline) {
			return nil, i18n.Err("err.tasks.question_expired")
		}
		i.Answer = answer
		i.AnswerID = answerID
		i.AnsweredBy = actor
		i.State = "saved"
		return map[string]string{"interaction_id": i.ID, "state": "saved"}, nil
	})
}

func activeJob(a *active, j *model.Job) error {
	if err := a.ctx.Err(); err != nil {
		return err
	}
	if j.Attempt != a.attempt || j.State != "running" {
		return i18n.Err("err.tasks.attempt_state_changed")
	}
	return nil
}

func (e *Engine) deliverAnswer(a *active, interactionID string) (any, error) {
	var answer string
	_, err := e.Store.ChangeControl(a.job, "answer_delivered", func(j *model.Job) (any, error) {
		if err := a.ctx.Err(); err != nil {
			return nil, err
		}
		i := j.Interaction
		if j.Attempt != a.attempt || i == nil || i.ID != interactionID || i.Attempt != a.attempt {
			return nil, i18n.Err("err.tasks.answer_attempt_mismatch")
		}
		if i.State == "delivered" && j.State == "running" {
			answer = i.Answer
			return nil, store.ErrUnchanged
		}
		if j.State != "waiting_input" || i.State != "saved" {
			return nil, i18n.Err("err.tasks.question_expired")
		}
		answer = i.Answer
		i.State = "delivered"
		j.State = "running"
		return map[string]string{"interaction_id": interactionID}, nil
	})
	return map[string]string{"answer": answer}, err
}
