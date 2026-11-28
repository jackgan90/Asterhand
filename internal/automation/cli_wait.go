package automation

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
)

type waitCLIOptions struct {
	Home               string
	Caller             string
	WorkspaceID        string
	TimeoutSeconds        int
	PollSeconds           int
	AfterCommentarySeq    int64
	AfterCommentarySeqSet bool
	WakeOnCommentary      bool
	DisconnectGraceSec int
	Quiet              bool
	TurnIDs            []string
}

// RunWaitCLI implements client-side blocking wait over workers_wait / workers_wait_any
// so long worker turns can be monitored in a background shell without consuming
// caller LLM context on 30-second polling loops.
func RunWaitCLI(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	opts, err := parseWaitArgs(args)
	if err != nil {
		return cliError(stdout, PublicError(err))
	}
	var client *Client
	if opts.Caller != "" {
		client, err = OpenCallerClient(opts.Home, opts.Caller)
	} else {
		client, err = OpenClient(opts.Home)
	}
	if err != nil {
		return cliError(stdout, PublicError(err))
	}
	defer client.Close()
	return runWaitLoop(ctx, client, opts, time.Now, sleepWithContext, stdout, stderr)
}

func parseWaitArgs(args []string) (waitCLIOptions, error) {
	reordered, err := reorderWaitFlags(args)
	if err != nil {
		return waitCLIOptions{}, err
	}
	f := flag.NewFlagSet("wait", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	home := f.String("home", config.DefaultHome(), "Existing node data directory")
	caller := f.String("caller", "", "Optional managed coding harness caller identifier")
	wsID := f.String("workspace-id", "", "Optional expected workspace_id to guard against cross-project mismatch")
	timeoutSec := f.Int("timeout", 1800, "Total wait timeout in seconds (1..7200)")
	pollSec := f.Int("poll-seconds", 30, "Per-request slice duration in seconds (1..30)")
	afterSeq := f.Int64("after-commentary-seq", 0, "Only wake on commentary seq greater than this value (single turn only)")
	wakeComment := f.Bool("wake-on-commentary", false, "Exit as soon as a new commentary update arrives (single turn only)")
	graceSec := f.Int("disconnect-grace", 60, "Retry grace window in seconds during transient daemon restarts (0..600)")
	quiet := f.Bool("quiet", false, "Suppress progress lines on stderr")

	if err := f.Parse(reordered); err != nil {
		return waitCLIOptions{}, Fail("invalid_parameters", "Usage: asterhand wait <turn_id> [turn_id2 ...] [--home DIR] [--caller ID] [--workspace-id ID] [--timeout 1800] [--poll-seconds 30] [--after-commentary-seq N] [--wake-on-commentary] [--disconnect-grace 60] [--quiet]")
	}
	turnIDs, err := normalizeWaitTurnIDs(f.Args())
	if err != nil {
		return waitCLIOptions{}, err
	}
	if *timeoutSec < 1 || *timeoutSec > 7200 {
		return waitCLIOptions{}, Fail("invalid_parameters", "timeout must be between 1 and 7200 seconds")
	}
	if *pollSec < 1 || *pollSec > 30 {
		return waitCLIOptions{}, Fail("invalid_parameters", "poll-seconds must be between 1 and 30 seconds")
	}
	if *graceSec < 0 || *graceSec > 600 {
		return waitCLIOptions{}, Fail("invalid_parameters", "disconnect-grace must be between 0 and 600 seconds")
	}
	if *afterSeq < 0 {
		return waitCLIOptions{}, Fail("invalid_parameters", "after-commentary-seq must be non-negative")
	}
	afterSeqSet := false
	f.Visit(func(fl *flag.Flag) {
		if fl.Name == "after-commentary-seq" {
			afterSeqSet = true
		}
	})
	if len(turnIDs) > 1 && (*wakeComment || afterSeqSet) {
		return waitCLIOptions{}, Fail("invalid_parameters", "--wake-on-commentary and --after-commentary-seq only apply to a single turn_id")
	}
	return waitCLIOptions{
		Home:                  *home,
		Caller:                strings.TrimSpace(*caller),
		WorkspaceID:           strings.TrimSpace(*wsID),
		TimeoutSeconds:        *timeoutSec,
		PollSeconds:           *pollSec,
		AfterCommentarySeq:    *afterSeq,
		AfterCommentarySeqSet: afterSeqSet,
		WakeOnCommentary:      *wakeComment,
		DisconnectGraceSec:    *graceSec,
		Quiet:                 *quiet,
		TurnIDs:               turnIDs,
	}, nil
}

func reorderWaitFlags(args []string) ([]string, error) {
	valueFlags := map[string]bool{
		"home": true, "caller": true, "workspace-id": true,
		"timeout": true, "poll-seconds": true, "after-commentary-seq": true,
		"disconnect-grace": true,
	}
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		trimmed := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		if strings.Contains(trimmed, "=") {
			continue
		}
		if valueFlags[trimmed] {
			if i+1 >= len(args) {
				return nil, Fail("invalid_parameters", fmt.Sprintf("Missing value for flag %s", a))
			}
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...), nil
}

func normalizeWaitTurnIDs(raw []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		id := strings.TrimSpace(r)
		if id == "" {
			return nil, Fail("invalid_parameters", "turn_id arguments must be non-empty")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 || len(out) > 16 {
		return nil, Fail("invalid_parameters", "Provide between 1 and 16 turn_id arguments")
	}
	return out, nil
}

func runWaitLoop(
	ctx context.Context,
	caller Caller,
	opts waitCLIOptions,
	nowFn func() time.Time,
	sleepFn func(context.Context, time.Duration) error,
	stdout, stderr io.Writer,
) error {
	deadline := nowFn().Add(time.Duration(opts.TimeoutSeconds) * time.Second)
	commentarySeq := opts.AfterCommentarySeq
	var (
		lastState     string
		lastProgress  string
		lastResp      Response
		disconnectAt  time.Time
		retryReported bool
	)

	for {
		rem := deadline.Sub(nowFn())
		if rem <= 0 {
			return writeWaitTimeout(stdout, lastResp, opts.TimeoutSeconds)
		}
		sliceSec := opts.PollSeconds
		if remSec := int(math.Ceil(rem.Seconds())); remSec < sliceSec {
			sliceSec = max(remSec, 1)
		}

		opName, argsJSON := buildWaitPayload(opts, sliceSec, commentarySeq)
		callCtx, cancelCall := context.WithTimeout(ctx, rem)
		resp, err := caller.Call(callCtx, opName, "", argsJSON)
		callTimedOut := callCtx.Err() != nil && ctx.Err() == nil
		cancelCall()
		if ctx.Err() != nil {
			return cliError(stdout, PublicError(ctx.Err()))
		}
		if callTimedOut || deadline.Sub(nowFn()) <= 0 {
			return writeWaitTimeout(stdout, lastResp, opts.TimeoutSeconds)
		}
		if transient, publicErr := classifyWaitError(err, resp.Error); publicErr != nil {
			if !transient {
				outResp := resp
				if outResp.Error == nil {
					outResp = Response{Version: APIVersion, Error: publicErr}
				}
				return writeWaitResponse(stdout, outResp)
			}
			now := nowFn()
			if disconnectAt.IsZero() {
				disconnectAt = now
			}
			grace := time.Duration(opts.DisconnectGraceSec) * time.Second
			if now.Sub(disconnectAt) >= grace {
				return writeWaitResponse(stdout, Response{Version: APIVersion, Error: publicErr})
			}
			if !opts.Quiet && !retryReported {
				fmt.Fprintf(stderr, "[asterhand wait] node temporarily unavailable (%s); retrying up to %ds...\n", publicErr.Code, opts.DisconnectGraceSec)
				retryReported = true
			}
			waitStep := minDurationWait(2*time.Second, deadline.Sub(now))
			if waitStep > 0 {
				if sErr := sleepFn(ctx, waitStep); sErr != nil {
					return cliError(stdout, PublicError(sErr))
				}
			}
			continue
		}

		disconnectAt = time.Time{}
		retryReported = false
		lastResp = resp

		done, nextSeq, statusLine := evaluateWaitStep(opts, resp, lastState, lastProgress)
		if nextSeq > commentarySeq {
			commentarySeq = nextSeq
		}
		if done {
			return writeWaitResponse(stdout, resp)
		}
		if statusLine != "" {
			lastState, lastProgress = extractWaitSummaryState(opts, resp)
			if !opts.Quiet {
				fmt.Fprintln(stderr, statusLine)
			}
		}
	}
}

func buildWaitPayload(opts waitCLIOptions, sliceSec int, commentarySeq int64) (string, json.RawMessage) {
	if len(opts.TurnIDs) == 1 {
		payload := map[string]any{
			"turn_id":      opts.TurnIDs[0],
			"wait_seconds": sliceSec,
		}
		if opts.WorkspaceID != "" {
			payload["workspace_id"] = opts.WorkspaceID
		}
		if opts.AfterCommentarySeqSet || commentarySeq > 0 {
			payload["after_commentary_seq"] = commentarySeq
		}
		if opts.WakeOnCommentary {
			payload["wake_on_commentary"] = true
		}
		b, _ := json.Marshal(payload)
		return "workers_wait", b
	}
	payload := map[string]any{
		"turn_ids":     opts.TurnIDs,
		"wait_seconds": sliceSec,
	}
	if opts.WorkspaceID != "" {
		payload["workspace_id"] = opts.WorkspaceID
	}
	b, _ := json.Marshal(payload)
	return "workers_wait_any", b
}

func classifyWaitError(err error, respErr *Error) (transient bool, out *Error) {
	if err != nil {
		pub := PublicError(err)
		return isTransientWaitCode(pub.Code), pub
	}
	if respErr != nil {
		return isTransientWaitCode(respErr.Code), respErr
	}
	return false, nil
}

func isTransientWaitCode(code string) bool {
	switch code {
	case "node_unavailable", "unavailable", "wait_timeout":
		return true
	default:
		return false
	}
}

func evaluateWaitStep(opts waitCLIOptions, resp Response, prevState, prevProgress string) (done bool, nextSeq int64, statusLine string) {
	dataMap, ok := toStringMap(resp.Data)
	if !ok {
		return false, 0, ""
	}
	if len(opts.TurnIDs) > 1 {
		if trig, _ := dataMap["triggered_turn_id"].(string); trig != "" {
			return true, 0, ""
		}
		state, progress := extractWaitSummaryState(opts, resp)
		if state != prevState || progress != prevProgress {
			return false, 0, fmt.Sprintf("[asterhand wait] watching %d turns: %s", len(opts.TurnIDs), state)
		}
		return false, 0, ""
	}

	state, _ := dataMap["state"].(string)
	progress, _ := dataMap["progress"].(string)
	if delivered, ok := dataMap["delivered_commentary_seq"]; ok && delivered != nil {
		nextSeq = readInt64Value(delivered)
	} else {
		nextSeq = readInt64Value(dataMap["latest_commentary_seq"])
	}

	if isWaitWakeTerminalState(state) {
		return true, nextSeq, ""
	}
	if state == "waiting_input" && !isInteractionVisibleToHuman(dataMap["pending_interaction"]) {
		return true, nextSeq, ""
	}
	if opts.WakeOnCommentary {
		if comms, ok := dataMap["commentaries"].([]any); ok && len(comms) > 0 {
			return true, nextSeq, ""
		}
	}
	if state != prevState || progress != prevProgress {
		elapsed := readInt64Value(dataMap["elapsed_seconds"])
		if progress != "" {
			statusLine = fmt.Sprintf("[asterhand wait] %s state=%s elapsed=%ds progress=%s", opts.TurnIDs[0], state, elapsed, progress)
		} else {
			statusLine = fmt.Sprintf("[asterhand wait] %s state=%s elapsed=%ds", opts.TurnIDs[0], state, elapsed)
		}
	}
	return false, nextSeq, statusLine
}

func extractWaitSummaryState(opts waitCLIOptions, resp Response) (string, string) {
	dataMap, ok := toStringMap(resp.Data)
	if !ok {
		return "", ""
	}
	if len(opts.TurnIDs) > 1 {
		items, _ := dataMap["items"].([]any)
		var parts []string
		for _, raw := range items {
			if m, ok := raw.(map[string]any); ok {
				tid, _ := m["turn_id"].(string)
				st, _ := m["state"].(string)
				parts = append(parts, tid+"="+st)
			}
		}
		return strings.Join(parts, ", "), ""
	}
	state, _ := dataMap["state"].(string)
	progress, _ := dataMap["progress"].(string)
	return state, progress
}

func isWaitWakeTerminalState(state string) bool {
	switch state {
	case "completed", "interrupted", "cancelled", "cancelled_authorization_revoked",
		"failed", "blocked_unsupported_approval", "blocked_unsupported_user_input",
		"reconciliation_required":
		return true
	default:
		return false
	}
}

func isInteractionVisibleToHuman(raw any) bool {
	m, ok := raw.(map[string]any)
	if !ok || m == nil {
		return false
	}
	vis, _ := m["visible_to_human"].(bool)
	return vis
}

func readInt64Value(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		val, _ := n.Int64()
		return val
	case string:
		val, _ := strconv.ParseInt(n, 10, 64)
		return val
	default:
		return 0
	}
}

func writeWaitTimeout(stdout io.Writer, lastResp Response, timeoutSec int) error {
	out := Response{
		Version:  APIVersion,
		Node:     lastResp.Node,
		Instance: lastResp.Instance,
		Data:     lastResp.Data,
		Error:    Fail("wait_timeout", fmt.Sprintf("Wait timeout (%ds) reached while turn is still active", timeoutSec)),
	}
	if lastResp.Version != "" {
		out.Version = lastResp.Version
	}
	return writeWaitResponse(stdout, out)
}

func writeWaitResponse(stdout io.Writer, resp Response) error {
	if resp.Version == "" {
		resp.Version = APIVersion
	}
	if err := json.NewEncoder(stdout).Encode(resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return &ExitError{Code: ErrorExit(resp.Error.Code)}
	}
	return nil
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func minDurationWait(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
