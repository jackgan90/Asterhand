package app

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/model"
	"github.com/jackgan90/Asterhand/internal/peer"
)

func (a *App) automationTaskEvents(ctx context.Context, call automation.Call, v automation.TaskEvents) (automation.Outcome, error) {
	j, outbox, err := a.loadAutomationTask(call.Principal, v.Task)
	if err != nil {
		return automation.Outcome{}, err
	}
	after := int64(0)
	if v.After != "" {
		after, err = strconv.ParseInt(v.After, 10, 64)
		if err != nil || after < 0 {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid event sequence")
		}
	}
	binding := []any{"task-events", v.Task, v.After, call.Principal.ID}
	cursor, err := automation.ParseCursor(v.Cursor, binding)
	if err != nil {
		return automation.Outcome{}, err
	}
	offset := 0
	if cursor.After != "" {
		parts := strings.Split(cursor.After, ":")
		if len(parts) > 2 {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid event cursor")
		}
		after, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil || after < 0 {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid event sequence")
		}
		if len(parts) == 2 {
			offset, err = strconv.Atoi(parts[1])
			if err != nil || offset < 1 || after < 1 {
				return automation.Outcome{}, automation.Fail("invalid_cursor", "Invalid event fragment")
			}
			after--
		}
	}
	var events []model.Event
	watermark := j.Seq
	if outbox == nil {
		if after > j.Seq {
			return automation.Outcome{}, automation.Fail("invalid_cursor", "Event sequence exceeds watermark")
		}
		events, err = a.Store.EventsLimit(j.ID, after, 1)
	} else {
		if outbox.Job == nil {
			return automation.Outcome{}, automation.Fail("not_ready", "The remote task has not been accepted yet")
		}
		p, peerErr := a.Peers.Get(outbox.PeerID)
		if peerErr != nil || p.Revoked {
			return automation.Outcome{}, automation.Fail("forbidden", "The original peer is unavailable or revoked")
		}
		var result struct {
			Events    []model.Event `json:"events"`
			Watermark int64         `json:"watermark"`
		}
		err = peer.Call(ctx, p, "GET", "/v1/jobs/"+url.PathEscape(j.ID)+"/events?after="+strconv.FormatInt(after, 10)+"&limit=1", nil, &result)
		events, watermark = result.Events, result.Watermark
	}
	if err != nil {
		return automation.Outcome{}, taskError(err)
	}
	data := map[string]any{"watermark": strconv.FormatInt(watermark, 10), "end": len(events) == 0}
	if len(events) == 0 {
		data["next"] = automation.EncodeCursor(binding, "", strconv.FormatInt(after, 10))
		return automation.Outcome{Data: data}, nil
	}
	event := events[0]
	public := map[string]any{"seq": strconv.FormatInt(event.Seq, 10), "job_id": event.JobID, "attempt": event.Attempt, "type": event.Type, "time": event.Time, "data": event.Data}
	raw, _ := json.Marshal(public)
	if len(raw) <= 32<<10 && offset == 0 {
		data["event"] = public
		data["next"] = automation.EncodeCursor(binding, "", strconv.FormatInt(event.Seq, 10))
	} else {
		fragment, end, err := textFragment(string(raw), offset, 8192)
		if err != nil {
			return automation.Outcome{}, err
		}
		data["event_fragment"] = map[string]any{"sequence": strconv.FormatInt(event.Seq, 10), "format": "json", "text": fragment, "offset": strconv.Itoa(offset), "complete": end == len(raw)}
		next := strconv.FormatInt(event.Seq, 10)
		if end < len(raw) {
			next += ":" + strconv.Itoa(end)
		}
		data["next"] = automation.EncodeCursor(binding, "", next)
	}
	return automation.Outcome{Data: data}, nil
}
