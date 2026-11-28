package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseWaitArgsOrderIndependenceAndValidation(t *testing.T) {
	// Flags after positional turn_id must parse identically to flags before turn_id.
	o1, err := parseWaitArgs([]string{"turn_1", "--timeout", "900", "--caller", "claude-code", "--wake-on-commentary"})
	if err != nil {
		t.Fatalf("parseWaitArgs trailing flags: %v", err)
	}
	o2, err := parseWaitArgs([]string{"--timeout=900", "--caller", "claude-code", "--wake-on-commentary", "turn_1"})
	if err != nil {
		t.Fatalf("parseWaitArgs leading flags: %v", err)
	}
	if o1.TimeoutSeconds != 900 || o1.Caller != "claude-code" || !o1.WakeOnCommentary || len(o1.TurnIDs) != 1 || o1.TurnIDs[0] != "turn_1" {
		t.Fatalf("unexpected o1: %+v", o1)
	}
	if o2.TimeoutSeconds != o1.TimeoutSeconds || o2.Caller != o1.Caller || o2.WakeOnCommentary != o1.WakeOnCommentary {
		t.Fatalf("leading vs trailing flags diverged: o1=%+v o2=%+v", o1, o2)
	}

	// Reject wake-on-commentary or after-commentary-seq with multiple turns.
	if _, err := parseWaitArgs([]string{"turn_1", "turn_2", "--wake-on-commentary"}); err == nil {
		t.Fatal("expected multi-turn with --wake-on-commentary rejected")
	}
	if _, err := parseWaitArgs([]string{"turn_1", "turn_2", "--after-commentary-seq", "3"}); err == nil {
		t.Fatal("expected multi-turn with --after-commentary-seq rejected")
	}
	// Reject out-of-range bounds and missing turn_ids.
	for _, bad := range [][]string{
		{},
		{"turn_1", "--timeout", "0"},
		{"turn_1", "--poll-seconds", "31"},
		{"turn_1", "--disconnect-grace", "-1"},
	} {
		if _, err := parseWaitArgs(bad); err == nil {
			t.Fatalf("expected validation error for %v", bad)
		}
	}
}

type stubWaitCaller struct {
	fn func(ctx context.Context, op, id string, args json.RawMessage) (Response, error)
}

func (s stubWaitCaller) Call(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
	return s.fn(ctx, op, id, args)
}

func TestRunWaitLoopSingleMultiCommentaryAndTimeout(t *testing.T) {
	now := time.Unix(1700000000, 0)
	nowFn := func() time.Time { return now }
	noopSleep := func(context.Context, time.Duration) error {
		now = now.Add(2 * time.Second)
		return nil
	}

	// 1. Single turn: running -> waiting_approval (keeps waiting) -> completed (exits 0).
	step := 0
	caller := stubWaitCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
		if op != "workers_wait" {
			t.Fatalf("expected workers_wait, got %s", op)
		}
		step++
		now = now.Add(10 * time.Second)
		switch step {
		case 1:
			return Response{Version: APIVersion, Data: map[string]any{"turn_id": "turn_1", "state": "running", "elapsed_seconds": 10, "progress": "reading files"}}, nil
		case 2:
			return Response{Version: APIVersion, Data: map[string]any{"turn_id": "turn_1", "state": "waiting_approval", "elapsed_seconds": 20, "progress": "needs approval"}}, nil
		default:
			return Response{Version: APIVersion, Data: map[string]any{"turn_id": "turn_1", "state": "completed", "answer_available": true, "answer": "DONE"}}, nil
		}
	}}
	var stdout, stderr bytes.Buffer
	opts := waitCLIOptions{TurnIDs: []string{"turn_1"}, TimeoutSeconds: 120, PollSeconds: 30, DisconnectGraceSec: 10}
	if err := runWaitLoop(context.Background(), caller, opts, nowFn, noopSleep, &stdout, &stderr); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(stdout.String(), `"answer":"DONE"`) {
		t.Fatalf("expected completed JSON on stdout, got %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "state=running") || !strings.Contains(stderr.String(), "state=waiting_approval") {
		t.Fatalf("expected status transitions on stderr, got %s", stderr.String())
	}

	// 2. waiting_input (not visible_to_human) wakes immediately with exit 0.
	stdout.Reset()
	stderr.Reset()
	callerInput := stubWaitCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
		return Response{Version: APIVersion, Data: map[string]any{
			"turn_id":             "turn_inp",
			"state":               "waiting_input",
			"pending_interaction": map[string]any{"visible_to_human": false},
		}}, nil
	}}
	if err := runWaitLoop(context.Background(), callerInput, waitCLIOptions{TurnIDs: []string{"turn_inp"}, TimeoutSeconds: 60, PollSeconds: 30, Quiet: true}, nowFn, noopSleep, &stdout, &stderr); err != nil {
		t.Fatalf("expected waiting_input to wake cleanly, got %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("expected quiet mode to suppress stderr, got %s", stderr.String())
	}

	// 3. wake_on_commentary wakes when commentary arrives.
	stdout.Reset()
	callerComment := stubWaitCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
		return Response{Version: APIVersion, Data: map[string]any{
			"turn_id":               "turn_c",
			"state":                 "running",
			"latest_commentary_seq": 2,
			"commentaries":          []any{map[string]any{"seq": 2, "text": "Found bug in foo.go"}},
		}}, nil
	}}
	if err := runWaitLoop(context.Background(), callerComment, waitCLIOptions{TurnIDs: []string{"turn_c"}, TimeoutSeconds: 60, PollSeconds: 30, WakeOnCommentary: true}, nowFn, noopSleep, &stdout, &stderr); err != nil {
		t.Fatalf("expected wake_on_commentary to exit 0, got %v", err)
	}
	if !strings.Contains(stdout.String(), "Found bug in foo.go") {
		t.Fatalf("expected commentary in stdout JSON, got %s", stdout.String())
	}

	// 4. Multi-turn calls workers_wait_any and wakes when triggered_turn_id is set.
	stdout.Reset()
	multiCaller := stubWaitCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
		if op != "workers_wait_any" {
			t.Fatalf("expected workers_wait_any, got %s", op)
		}
		return Response{Version: APIVersion, Data: map[string]any{"triggered_turn_id": "turn_b", "items": []any{}}}, nil
	}}
	if err := runWaitLoop(context.Background(), multiCaller, waitCLIOptions{TurnIDs: []string{"turn_a", "turn_b"}, TimeoutSeconds: 60, PollSeconds: 30}, nowFn, noopSleep, &stdout, &stderr); err != nil {
		t.Fatalf("expected multi-turn wait to exit 0, got %v", err)
	}

	// 5. Total timeout exits 8 (wait_timeout) while preserving latest snapshot in data.
	stdout.Reset()
	timeoutCaller := stubWaitCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
		now = now.Add(15 * time.Second)
		return Response{Version: APIVersion, Node: "node-1", Instance: "inst-1", Data: map[string]any{"turn_id": "turn_slow", "state": "running", "progress": "still working"}}, nil
	}}
	err := runWaitLoop(context.Background(), timeoutCaller, waitCLIOptions{TurnIDs: []string{"turn_slow"}, TimeoutSeconds: 20, PollSeconds: 15, Quiet: true}, nowFn, noopSleep, &stdout, &stderr)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 8 {
		t.Fatalf("expected ExitError code 8 on wait_timeout, got %v", err)
	}
	if !strings.Contains(stdout.String(), `"code":"wait_timeout"`) || !strings.Contains(stdout.String(), `"still working"`) {
		t.Fatalf("expected wait_timeout error with latest snapshot on stdout, got %s", stdout.String())
	}
}

func TestRunWaitLoopTransientRetryAndImmediateAuthFail(t *testing.T) {
	now := time.Unix(1700000000, 0)
	nowFn := func() time.Time { return now }
	noopSleep := func(context.Context, time.Duration) error {
		now = now.Add(5 * time.Second)
		return nil
	}

	// 1. Transient node_unavailable recovers within disconnect-grace.
	calls := 0
	recoverCaller := stubWaitCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
		calls++
		if calls <= 2 {
			return Response{}, Fail("node_unavailable", "Daemon restarting")
		}
		return Response{Version: APIVersion, Data: map[string]any{"turn_id": "turn_rec", "state": "completed"}}, nil
	}}
	var stdout, stderr bytes.Buffer
	err := runWaitLoop(context.Background(), recoverCaller, waitCLIOptions{TurnIDs: []string{"turn_rec"}, TimeoutSeconds: 120, PollSeconds: 30, DisconnectGraceSec: 30}, nowFn, noopSleep, &stdout, &stderr)
	if err != nil || calls != 3 {
		t.Fatalf("expected recovery after 2 transient failures, calls=%d err=%v", calls, err)
	}

	// 2. Exceeding disconnect-grace exits 7.
	stdout.Reset()
	stderr.Reset()
	downCaller := stubWaitCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
		return Response{}, Fail("node_unavailable", "Still down")
	}}
	errDown := runWaitLoop(context.Background(), downCaller, waitCLIOptions{TurnIDs: []string{"turn_down"}, TimeoutSeconds: 120, PollSeconds: 30, DisconnectGraceSec: 10}, nowFn, noopSleep, &stdout, &stderr)
	var exitDown *ExitError
	if !errors.As(errDown, &exitDown) || exitDown.Code != 7 {
		t.Fatalf("expected exit 7 after grace exhausted, got %v", errDown)
	}

	// 3. Explicit forbidden does NOT retry and exits 3 immediately.
	authCalls := 0
	authCaller := stubWaitCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
		authCalls++
		return Response{Version: APIVersion, Error: Fail("forbidden", "Revoked")}, nil
	}}
	stdout.Reset()
	errAuth := runWaitLoop(context.Background(), authCaller, waitCLIOptions{TurnIDs: []string{"turn_auth"}, TimeoutSeconds: 120, PollSeconds: 30, DisconnectGraceSec: 60}, nowFn, noopSleep, &stdout, &stderr)
	var exitAuth *ExitError
	if !errors.As(errAuth, &exitAuth) || exitAuth.Code != 3 || authCalls != 1 {
		t.Fatalf("expected immediate exit 3 without retry on forbidden, calls=%d err=%v", authCalls, errAuth)
	}
}

func TestRunCLIArgsInputMutualExclusion(t *testing.T) {
	var out, errOut bytes.Buffer
	defs := []Definition{
		{
			Name:   "workers_list",
			Role:   "read",
			Schema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false},
		},
	}
	// Fake normalizer on definition.
	defs[0].normalize = func(raw json.RawMessage) (any, error) { return Empty{}, nil }

	err := RunCLI(context.Background(), []string{"workers_list", "--input", "-", "--args", `{}`}, strings.NewReader("{}"), &out, &errOut, defs)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 || !strings.Contains(out.String(), "cannot be combined") {
		t.Fatalf("expected exit 2 mutual exclusion error, got err=%v out=%s", err, out.String())
	}
}

func TestWaitCLIExplicitZeroAfterCommentarySeq(t *testing.T) {
	optsOmitted, err := parseWaitArgs([]string{"turn_1"})
	if err != nil || optsOmitted.AfterCommentarySeqSet {
		t.Fatalf("expected omitted after-commentary-seq unset, got %+v err=%v", optsOmitted, err)
	}
	if _, raw := buildWaitPayload(optsOmitted, 30, 0); strings.Contains(string(raw), "after_commentary_seq") {
		t.Fatalf("expected omitted flag to omit after_commentary_seq from JSON, got %s", string(raw))
	}

	optsZero, err := parseWaitArgs([]string{"turn_1", "--after-commentary-seq", "0", "--wake-on-commentary"})
	if err != nil || !optsZero.AfterCommentarySeqSet || optsZero.AfterCommentarySeq != 0 {
		t.Fatalf("expected explicit --after-commentary-seq 0 captured, got %+v err=%v", optsZero, err)
	}
	if _, raw := buildWaitPayload(optsZero, 30, 0); !strings.Contains(string(raw), `"after_commentary_seq":0`) {
		t.Fatalf("expected explicit 0 in workers_wait JSON payload, got %s", string(raw))
	}

	if _, err := parseWaitArgs([]string{"turn_1", "turn_2", "--after-commentary-seq", "0"}); err == nil {
		t.Fatal("expected multi-turn wait with explicit --after-commentary-seq 0 to be rejected")
	}

	// evaluateWaitStep must advance using delivered_commentary_seq (not latest_commentary_seq)
	// so paginated commentaries are never skipped between polls.
	optsPaginated := waitCLIOptions{TurnIDs: []string{"turn_1"}, AfterCommentarySeqSet: true, AfterCommentarySeq: 0}
	_, seq12, _ := evaluateWaitStep(optsPaginated, Response{Data: map[string]any{
		"turn_id":                  "turn_1",
		"state":                    "running",
		"latest_commentary_seq":    float64(17),
		"delivered_commentary_seq": float64(12),
		"commentaries_truncated":   true,
	}}, "", "")
	if seq12 != 12 {
		t.Fatalf("expected nextSeq=12 from delivered_commentary_seq, got %d", seq12)
	}
	_, seq0, _ := evaluateWaitStep(optsPaginated, Response{Data: map[string]any{
		"turn_id":                  "turn_1",
		"state":                    "running",
		"latest_commentary_seq":    float64(3),
		"delivered_commentary_seq": float64(0),
		"commentaries_truncated":   true,
	}}, "", "")
	if seq0 != 0 {
		t.Fatalf("expected nextSeq=0 when delivered_commentary_seq is explicit 0, got %d", seq0)
	}
}

func TestFormatMCPTextWorkersResultOverBudgetDoesNotSkipPage(t *testing.T) {
	resp := Response{
		Version: APIVersion,
		Data: map[string]any{
			"turn_id":     "turn_big_res",
			"worker_id":   "wkr_1",
			"state":       "completed",
			"offset":      20587,
			"total_bytes": 58516,
			"complete":    false,
			"next_cursor": "45163",
			"text":        strings.Repeat("<", 10000), // 60 KiB after JSON HTML escaping (> 48 KiB)
		},
	}
	txt := formatMCPText("workers_result", resp)
	if !strings.Contains(txt, `"text_truncated":true`) || !strings.Contains(txt, `"offset":20587`) {
		t.Fatalf("expected truncated fallback preserving offset=20587, got %s", txt)
	}
	if strings.Contains(txt, "45163") || strings.Contains(txt, `"next_cursor"`) {
		t.Fatalf("workers_result fallback must not advertise skipped next_cursor=45163, got %s", txt)
	}
	if !strings.Contains(txt, `cursor=\"20587\"`) || !strings.Contains(txt, `max_bytes=8192`) {
		t.Fatalf("workers_result fallback should instruct retrying from offset 20587 with smaller max_bytes, got %s", txt)
	}
}

func TestRunWaitLoopBoundsInFlightCallAndRejectsLateSuccess(t *testing.T) {
	// 1. In-flight call receives a context deadline derived from remaining total timeout
	//    and aborts near 1s with exit code 8 (wait_timeout) preserving the last running snapshot.
	calls := 0
	var secondCallHasDeadline bool
	slowRespectingCtx := stubWaitCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
		calls++
		if calls == 1 {
			return Response{
				Version: APIVersion, Node: "node-1", Instance: "inst-1",
				Data: map[string]any{"turn_id": "turn_slow_ctx", "state": "running", "progress": "phase-1"},
			}, nil
		}
		_, secondCallHasDeadline = ctx.Deadline()
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(1500 * time.Millisecond):
			return Response{
				Version: APIVersion,
				Data:    map[string]any{"turn_id": "turn_slow_ctx", "state": "completed", "answer_available": true, "answer": "late"},
			}, nil
		}
	}}
	var stdout, stderr bytes.Buffer
	start := time.Now()
	err := runWaitLoop(context.Background(), slowRespectingCtx, waitCLIOptions{
		TurnIDs: []string{"turn_slow_ctx"}, TimeoutSeconds: 1, PollSeconds: 1, Quiet: true,
	}, time.Now, sleepWithContext, &stdout, &stderr)
	elapsed := time.Since(start)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 8 {
		t.Fatalf("expected ExitError code 8 on in-flight timeout, got err=%v out=%s", err, stdout.String())
	}
	if !secondCallHasDeadline {
		t.Fatal("expected in-flight caller.Call context to carry a deadline")
	}
	if elapsed > 1400*time.Millisecond {
		t.Fatalf("expected timeout near 1s, took %v", elapsed)
	}
	if !strings.Contains(stdout.String(), `"code":"wait_timeout"`) || !strings.Contains(stdout.String(), `"phase-1"`) || strings.Contains(stdout.String(), `"completed"`) {
		t.Fatalf("expected wait_timeout with last running snapshot, got %s", stdout.String())
	}

	// 2. Even if a stub ignores ctx.Done() and returns completed past the deadline,
	//    runWaitLoop still enforces the overall deadline and rejects the late response.
	stdout.Reset()
	fakeNow := time.Unix(1700000000, 0)
	lateCalls := 0
	lateCaller := stubWaitCaller{fn: func(ctx context.Context, op, id string, args json.RawMessage) (Response, error) {
		lateCalls++
		if lateCalls == 1 {
			return Response{Version: APIVersion, Data: map[string]any{"turn_id": "turn_late", "state": "running", "progress": "before-deadline"}}, nil
		}
		fakeNow = fakeNow.Add(1500 * time.Millisecond)
		return Response{Version: APIVersion, Data: map[string]any{"turn_id": "turn_late", "state": "completed"}}, nil
	}}
	errLate := runWaitLoop(context.Background(), lateCaller, waitCLIOptions{
		TurnIDs: []string{"turn_late"}, TimeoutSeconds: 1, PollSeconds: 1, Quiet: true,
	}, func() time.Time { return fakeNow }, sleepWithContext, &stdout, &stderr)
	var exitLate *ExitError
	if !errors.As(errLate, &exitLate) || exitLate.Code != 8 {
		t.Fatalf("expected ExitError code 8 for late completed response, got err=%v out=%s", errLate, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"before-deadline"`) || strings.Contains(stdout.String(), `"completed"`) {
		t.Fatalf("expected last running snapshot instead of late completed response, got %s", stdout.String())
	}
}
