package app

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/drafts"
	"github.com/jackgan90/Asterhand/internal/editors"
	"github.com/jackgan90/Asterhand/internal/quickinput/hostipc"
)

type liveSnapshot struct {
	ID       string          `json:"draft_id"`
	Kind     string          `json:"kind"`
	Target   drafts.Target   `json:"target"`
	Revision string          `json:"revision,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
}
type liveWrite struct {
	ID       string `json:"draft_id"`
	Revision string `json:"expected_revision"`
}
type liveUpdate struct {
	ID       string          `json:"draft_id"`
	Revision string          `json:"expected_revision"`
	Content  json.RawMessage `json:"content"`
}
type liveList struct {
	Kind         string `json:"kind,omitempty"`
	Command      string `json:"command_id,omitempty"`
	Conversation string `json:"conversation_id,omitempty"`
	Project      string `json:"project_id,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

func (a *App) serveNativeEditors(ctx context.Context, page string, send func(hostipc.EditorRequest) editors.Reply) {
	commands := a.Editors.Commands(ctx, "native", page)
	if commands == nil {
		return
	}
	for command := range commands {
		reply := send(hostipc.EditorRequest{Node: a.Config.Get().NodeID, Command: command})
		_ = a.Editors.Respond("native", editors.Poll{Page: page, Reply: &reply})
	}
}

func editorError(err error) error {
	code := err.Error()
	switch code {
	case "busy", "conflict", "editor_closed", "result_unknown", "invalid_parameters", "forbidden", "capacity", "node_unavailable", "submission_pending":
	default:
		code = "operation_failed"
	}
	return automation.Fail(code, "Live editor: "+code)
}

func (a *App) checkEditor(p automation.Principal, s liveSnapshot) error {
	if s.Kind != "task_goal" && s.Kind != "task_answer" && s.Target.Node != a.Config.Get().NodeID {
		return automation.Fail("wrong_node", "Editor belongs to another node")
	}
	if err := a.validateLiveTarget(s.Kind, s.Target); err != nil {
		return err
	}
	switch s.Kind {
	case "command":
		return p.Require("commands", s.Target.Command)
	case "settings":
		return p.Require("node", s.Target.Node)
	case "chat":
		return p.Require("conversations", s.Target.Conversation)
	default:
		return p.Require("projects", s.Target.Node+":"+s.Target.Project)
	}
}
func (a *App) liveGet(ctx context.Context, p automation.Principal, id string) (liveSnapshot, error) {
	var s liveSnapshot
	page, key, ok := strings.Cut(id, ":")
	if !ok || key == "" {
		return s, automation.Fail("invalid_parameters", "Use the editor ID returned by drafts.list")
	}
	data, err := a.Editors.Call(ctx, page, editors.Command{Action: "get", Editor: key}, nil)
	if err != nil {
		return s, editorError(err)
	}
	if err = json.Unmarshal(data, &s); err != nil || s.ID != key || s.Revision == "" {
		return s, automation.Fail("invalid_response", "Invalid live editor response")
	}
	if err = a.checkEditor(p, s); err != nil {
		return s, err
	}
	if err = automation.ValidateDraftContent(s.Kind, s.Target, s.Content); err != nil {
		return s, err
	}
	s.ID = id
	return s, nil
}
func (a *App) listLive(ctx context.Context, p automation.Principal, query liveList) (automation.Outcome, error) {
	limit, err := automation.PageLimit(query.Limit)
	if err != nil {
		return automation.Outcome{}, err
	}
	binding := []string{p.ID, query.Kind, query.Command, query.Conversation, query.Project}
	cursor, err := automation.ParseCursor(query.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	type result struct {
		items []liveSnapshot
		err   error
	}
	pages := a.Editors.Pages()
	results := make(chan result, len(pages))
	var wg sync.WaitGroup
	for _, id := range pages {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			data, err := a.Editors.Call(ctx, id, editors.Command{Action: "list"}, nil)
			var rows []liveSnapshot
			if err == nil {
				err = json.Unmarshal(data, &rows)
			}
			items := []liveSnapshot{}
			for _, s := range rows {
				if s.ID != "" && a.checkEditor(p, s) == nil && (query.Kind == "" || s.Kind == query.Kind) &&
					(query.Command == "" || s.Target.Command == query.Command) && (query.Conversation == "" || s.Target.Conversation == query.Conversation) &&
					(query.Project == "" || s.Target.Project == query.Project) {
					s.ID = id + ":" + s.ID
					s.Content = nil
					items = append(items, s)
				}
			}
			results <- result{items, err}
		}(id)
	}
	wg.Wait()
	close(results)
	items := []liveSnapshot{}
	unavailable := 0
	for result := range results {
		items = append(items, result.items...)
		if result.err != nil {
			unavailable++
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	page := make([]liveSnapshot, 0, limit)
	next := ""
	used := 0
	for _, s := range items {
		if s.ID <= cursor.After {
			continue
		}
		size := len(compactResult(s)) + 1
		if len(page) > 0 && (len(page) >= limit || used+size > automation.MaxResponse-2048) {
			next = automation.EncodeCursor(binding, "", page[len(page)-1].ID)
			break
		}
		page = append(page, s)
		used += size
	}
	return automation.Outcome{Data: map[string]any{"items": page, "next": next, "unavailable_windows": unavailable}}, nil
}

func (a *App) writeLive(ctx context.Context, call automation.Call, name string, v liveWrite, content json.RawMessage) (automation.Outcome, error) {
	s, err := a.liveGet(ctx, call.Principal, v.ID)
	if err != nil {
		return automation.Outcome{}, err
	}
	if s.Revision != v.Revision {
		return automation.Outcome{}, automation.Fail("conflict", "Editor target or content changed")
	}
	if name == "update" {
		if err = automation.ValidateDraftContent(s.Kind, s.Target, content); err != nil {
			return automation.Outcome{}, err
		}
	}
	if name == "commit" && (s.Kind == "chat" || strings.HasPrefix(s.Kind, "task_")) && !call.Principal.Role("execute") {
		return automation.Outcome{}, automation.Fail("forbidden", "Execution access required")
	}
	operation := call.Identity.ID
	var pending any
	if name == "commit" {
		ref, err := a.prepareEditorCommit(call.Identity, s)
		if err != nil {
			return automation.Outcome{}, err
		}
		operation, pending = ref.ID, ref
	}
	page, key, _ := strings.Cut(v.ID, ":")
	data, err := a.Editors.Call(ctx, page, editors.Command{Action: name, Editor: key, Revision: v.Revision, Content: content, Operation: operation}, call.Check)
	if err != nil {
		return automation.Outcome{Summary: pending, Unknown: err.Error() == "result_unknown"}, editorError(err)
	}
	var ref struct {
		Revision string `json:"revision"`
		State    string `json:"state"`
	}
	if json.Unmarshal(data, &ref) != nil || ref.State != "editable" && ref.State != "saved" && ref.State != "accepted" && ref.State != "queued" {
		return automation.Outcome{Summary: pending, Unknown: true}, automation.Fail("result_unknown", "Missing editor acknowledgement")
	}
	response := map[string]string{"draft_id": v.ID, "revision": ref.Revision, "state": ref.State}
	return automation.Outcome{Data: response, Summary: response, Accepted: true}, nil
}
func (a *App) registerLiveEditors(r *automation.Registry) {
	automation.Register(r, automation.Definition{Name: "drafts.list", Description: "Find open editors by business target; each window has a distinct editor ID", Role: "read"}, nil, nil, func(ctx context.Context, c automation.Call, query liveList) (automation.Outcome, error) {
		return a.listLive(ctx, c.Principal, query)
	})
	automation.Register(r, automation.Definition{Name: "drafts.get", Description: "Read the current unsaved ordinary fields of an open editor", Role: "read", Large: true}, func(v automation.DraftGet) string { return v.ID }, nil, func(ctx context.Context, c automation.Call, v automation.DraftGet) (automation.Outcome, error) {
		s, e := a.liveGet(ctx, c.Principal, v.ID)
		if e != nil {
			return automation.Outcome{}, e
		}
		return automationObject(s, v.Format, v.Cursor, []string{v.ID, s.Revision})
	})
	automation.Register(r, automation.Definition{Name: "drafts.update", Description: "Apply edits in the original editor at the given revision", Role: "edit", Write: true}, func(v liveUpdate) string { return v.ID }, nil, func(ctx context.Context, c automation.Call, v liveUpdate) (automation.Outcome, error) {
		return a.writeLive(ctx, c, "update", liveWrite{v.ID, v.Revision}, v.Content)
	})
	automation.Register(r, automation.Definition{Name: "drafts.commit", Description: "Save or send the specified live revision through its ordinary business action", Role: "edit", Write: true}, func(v liveWrite) string { return v.ID }, nil, func(ctx context.Context, c automation.Call, v liveWrite) (automation.Outcome, error) {
		return a.writeLive(ctx, c, "commit", v, nil)
	})
}
