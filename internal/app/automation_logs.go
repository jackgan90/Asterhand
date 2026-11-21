package app

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/localcommands"
)

type logPosition struct {
	Offset   string                                `json:"offset"`
	Fragment int                                   `json:"fragment,omitempty"`
	Before   bool                                  `json:"before,omitempty"`
	Pending  map[string]localcommands.DecoderState `json:"pending,omitempty"`
}

type publicLogChunk struct {
	Step   string    `json:"step_id"`
	Stream string    `json:"stream"`
	Time   time.Time `json:"time,omitempty"`
	Text   string    `json:"text,omitempty"`
	Raw    []byte    `json:"raw,omitempty"`
	Final  bool      `json:"final,omitempty"`
}

type publicLogPage struct {
	Run      localcommands.RunSummary `json:"run"`
	Chunks   []publicLogChunk         `json:"chunks"`
	Next     string                   `json:"next"`
	Previous string                   `json:"previous,omitempty"`
	AtEnd    bool                     `json:"at_end"`
	Finished bool                     `json:"finished"`
	Encoding string                   `json:"encoding"`
}

func (a *App) registerAutomationLogs(r *automation.Registry) {
	automation.Register(r, automation.Definition{Name: "runs_list", Description: "Page retained run summaries, including expired logs and deleted commands", Role: "read"}, func(v automation.RunList) string { return v.CommandID }, func(p automation.Principal, v automation.RunList) error { return p.Require("commands", v.CommandID) }, a.automationRunList)
	for _, name := range []string{"logs_read", "logs_tail", "logs_wait"} {
		op := name
		automation.Register(r, automation.Definition{Name: name, Description: "Read bounded raw or decoded graph logs with independent cursors; waits never stop execution", Role: "read", Wait: name == "logs_wait"}, func(v automation.LogRead) string { return v.CommandID }, func(p automation.Principal, v automation.LogRead) error { return p.Require("commands", v.CommandID) }, func(ctx context.Context, call automation.Call, v automation.LogRead) (automation.Outcome, error) {
			return a.automationLogs(ctx, call, v, op)
		})
	}
}

func (a *App) automationRunList(ctx context.Context, call automation.Call, v automation.RunList) (automation.Outcome, error) {
	limit, err := automation.PageLimit(v.Limit)
	if err != nil {
		return automation.Outcome{}, err
	}
	binding := []any{"runs", call.Principal.ID, call.Principal.Scopes["commands"], v.CommandID}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	items, next, err := a.Commands.RunHistory(v.CommandID, cursor.After, min(limit, 25))
	return automation.Outcome{Data: map[string]any{"items": items, "next": automation.EncodeCursor(binding, "", next)}}, commandError(err)
}

func (a *App) logCursor(binding any, p logPosition) string {
	b, _ := json.Marshal(p)
	return a.Automation.Registry.SealCursor(binding, string(b))
}

func (a *App) automationLogs(ctx context.Context, call automation.Call, v automation.LogRead, op string) (automation.Outcome, error) {
	encoding, err := localcommands.NormalizeLogEncoding(v.Encoding)
	if err != nil {
		return automation.Outcome{}, commandError(err)
	}
	if v.Format == "" {
		v.Format = "text"
	}
	if v.Format != "raw" && v.Format != "text" || v.Stream != "" && v.Stream != "stdout" && v.Stream != "stderr" {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "Unknown log format or stream")
	}
	if op == "logs_tail" && v.Cursor != "" {
		return automation.Outcome{}, automation.Fail("invalid_parameters", "Tail starts from the latest retained page")
	}
	binding := []any{"logs", call.Principal.ID, call.Principal.Scopes["commands"], v.CommandID, v.RunID, v.StepID, v.Stream, v.Format, encoding}
	cursor, err := a.Automation.Registry.OpenCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	p := logPosition{Offset: "0", Pending: map[string]localcommands.DecoderState{}}
	if cursor.After != "" {
		if err = json.Unmarshal([]byte(cursor.After), &p); err != nil {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Malformed log position")
		}
	}
	if op == "logs_tail" {
		p.Offset = "-1"
	}
	if p.Pending == nil {
		p.Pending = map[string]localcommands.DecoderState{}
	}
	if len(p.Pending) > 128 || p.Fragment < 0 || p.Fragment > 3*16*1024 {
		return automation.Outcome{}, automation.Fail("invalid_cursor", "Log position exceeds limits")
	}
	for k, s := range p.Pending {
		parts := strings.Split(k, ":")
		if len(parts) != 2 || len(parts[0]) > 64 || (parts[1] != "stdout" && parts[1] != "stderr") || len(s.Pending) > 3 {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid decoder checkpoint")
		}
	}
	duration, err := boundedWait(v.Seconds)
	if err != nil {
		return automation.Outcome{}, err
	}
	deadline := time.Now().Add(duration)
	for {
		if err = call.Check(); err != nil {
			return automation.Outcome{}, err
		}
		page, err := a.readAutomationLog(v, encoding, binding, p)
		if err != nil {
			return automation.Outcome{}, commandError(err)
		}
		if op != "logs_wait" || len(page.Chunks) > 0 || page.Finished || time.Now().After(deadline) {
			return automation.Outcome{Data: page}, nil
		}
		// Sparse empty pages still advance, but return immediately for the caller
		// to decide how much historical scanning it wants to perform.
		if !page.AtEnd {
			return automation.Outcome{Data: page}, nil
		}
		select {
		case <-ctx.Done():
			return automation.Outcome{}, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func (a *App) readAutomationLog(v automation.LogRead, encoding string, binding any, p logPosition) (publicLogPage, error) {
	pos, err := strconv.ParseInt(p.Offset, 10, 64)
	if err != nil || pos < -1 {
		return publicLogPage{}, automation.Fail("invalid_cursor", "Invalid byte offset")
	}
	meta, err := a.Commands.RunDetails(v.CommandID, v.RunID)
	if err != nil {
		return publicLogPage{}, err
	}
	if meta.Details != "retained" {
		return publicLogPage{}, automation.Fail("record_expired", "Detailed logs are no longer retained: "+meta.Details)
	}
	page, err := a.Commands.LogPage(v.CommandID, v.RunID, v.StepID, pos, p.Before)
	if err != nil {
		return publicLogPage{}, err
	}
	result := publicLogPage{Run: meta, Chunks: []publicLogChunk{}, Encoding: encoding}
	if page.Run != nil {
		result.Run.Phase = page.Run.Phase
		result.Run.Result = page.Run.Result
		result.Run.LogComplete = page.Run.LogComplete
		result.Run.Ended = page.Run.Ended
	}
	if page.Start > 0 {
		result.Previous = a.logCursor(binding, logPosition{Offset: strconv.FormatInt(page.Start, 10), Before: true})
	}
	for _, seed := range page.Context[encoding] {
		p.Pending[seed.NodeID+":"+seed.Stream] = localcommands.DecoderState{Pending: seed.Data, IgnoreBOM: seed.IgnoreBOM}
	}
	p.Before = false
	for i, chunk := range page.Chunks {
		if v.Stream != "" && v.Stream != chunk.Stream {
			p.Offset = strconv.FormatInt(chunk.End, 10)
			continue
		}
		out := publicLogChunk{Step: chunk.NodeID, Stream: chunk.Stream, Time: chunk.Time}
		key := chunk.NodeID + ":" + chunk.Stream
		if v.Format == "raw" {
			out.Raw = chunk.Data
			p.Fragment = 0
		} else {
			text, state, err := localcommands.DecodeLogText(encoding, p.Pending[key], chunk.Data, false)
			if err != nil {
				return result, err
			}
			if p.Fragment > len(text) || p.Fragment > 0 && p.Fragment < len(text) && !utf8.RuneStart(text[p.Fragment]) {
				return result, automation.Fail("invalid_cursor", "Invalid text fragment")
			}
			part := text[p.Fragment:]
			n := min(len(part), 16*1024)
			for n > 0 && !utf8.ValidString(part[:n]) {
				n--
			}
			for {
				b, _ := json.Marshal(part[:n])
				if len(b) <= 24*1024 {
					break
				}
				n /= 2
				for n > 0 && !utf8.ValidString(part[:n]) {
					n--
				}
			}
			out.Text = part[:n]
			if p.Fragment+n < len(text) {
				p.Offset = strconv.FormatInt(chunk.Start, 10)
				p.Fragment += n
				result.Chunks = append(result.Chunks, out)
				result.Next = a.logCursor(binding, p)
				return result, nil
			}
			p.Fragment = 0
			if len(state.Pending) > 0 {
				p.Pending[key] = state
			} else {
				delete(p.Pending, key)
			}
		}
		p.Offset = strconv.FormatInt(chunk.End, 10)
		result.Chunks = append(result.Chunks, out)
		if i < len(page.Chunks)-1 {
			result.Next = a.logCursor(binding, p)
			return result, nil
		}
	}
	p.Offset = strconv.FormatInt(page.Next, 10)
	result.AtEnd = page.End
	if page.End && page.Run != nil && page.Run.Phase == "finished" {
		if v.Format == "text" {
			for key, state := range p.Pending {
				if len(state.Pending) == 0 {
					continue
				}
				parts := strings.Split(key, ":")
				if v.Stream != "" && parts[1] != v.Stream {
					continue
				}
				text, _, err := localcommands.DecodeLogText(encoding, state, nil, true)
				if err != nil {
					return result, err
				}
				if text != "" {
					result.Chunks = append(result.Chunks, publicLogChunk{Step: parts[0], Stream: parts[1], Text: text, Final: true})
				}
			}
		}
		p.Pending = nil
		result.Finished = true
	}
	result.Next = a.logCursor(binding, p)
	return result, nil
}
