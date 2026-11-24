package workers

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrPreStartFailed marks an adapter failure that occurred before any prompt
// was transmitted to the underlying native session, allowing Service to fail
// the turn cleanly without trapping the worker in reconciliation_required.
var ErrPreStartFailed = errors.New("pre_start_failed")

const (
	WorkerIdle      = "idle"
	WorkerRunning   = "running"
	WorkerReconcile = "reconciliation_required"
	WorkerClosed    = "closed"
)

const (
	TurnPrepared         = "prepared"
	TurnRunning          = "running"
	TurnWaitingApproval  = "waiting_approval"
	TurnWaitingInput     = "waiting_input"
	TurnReconcile        = "reconciliation_required"
	TurnCompleted        = "completed"
	TurnInterrupted      = "interrupted"
	TurnCancelled        = "cancelled"
	TurnCancelledRevoked = "cancelled_authorization_revoked"
	TurnFailed           = "failed"
	TurnBlockedApproval  = "blocked_unsupported_approval"
	TurnBlockedUserInput = "blocked_unsupported_user_input"
)

const (
	ApprovalModeAuto           = "auto"
	ApprovalModeAskHuman       = "ask_human"
	ApprovalModeStrictDeny     = "strict_deny"
	ApprovalModeDenyEscalation = "deny_escalation"
)

const (
	InteractionApproval = "approval"
	InteractionInput    = "input"
)

const (
	DecisionAllowOnce    = "allow_once"
	DecisionAllowSession = "allow_session"
	DecisionDeny         = "deny"
	DecisionCancelTurn   = "cancel_turn"
	DecisionAnswer       = "answer"
)

const (
	AnswerSourceFinal    = "final_answer"
	AnswerSourceFallback = "fallback_last_candidate_assistant_message"
	AnswerSourceNone     = "none"
)

func IsTurnTerminal(state string) bool {
	switch state {
	case TurnCompleted, TurnInterrupted, TurnCancelled, TurnCancelledRevoked,
		TurnFailed, TurnBlockedApproval, TurnBlockedUserInput:
		return true
	default:
		return false
	}
}

type Worker struct {
	ID                    string     `json:"worker_id"`
	WorkspaceID           string     `json:"workspace_id"`
	WorkspaceRoot         string     `json:"workspace_root"`
	WorktreeKey           string     `json:"worktree_key"`
	Alias                 string     `json:"alias,omitempty"`
	HarnessID             string     `json:"harness_id"`
	AccessMode            string     `json:"access_mode"`
	ApprovalMode          string     `json:"approval_mode,omitempty"`
	EffectiveApprovalMode string     `json:"effective_approval_mode,omitempty"`
	SessionAutoApprove    bool       `json:"session_auto_approve,omitempty"`
	ProtectionLevel       string     `json:"protection_level"`
	RoleHint              string     `json:"role_hint,omitempty"`
	Model                 string     `json:"model,omitempty"`
	ReasoningEffort       string     `json:"reasoning_effort,omitempty"`
	State                 string     `json:"state"`
	SessionEpoch          int        `json:"session_epoch"`
	NativeSessionID       string     `json:"native_session_id,omitempty"`
	ActiveTurnID          string     `json:"active_turn_id,omitempty"`
	OperationID           string     `json:"operation_id,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	ClosedAt              *time.Time `json:"closed_at,omitempty"`
	Warnings              []string   `json:"warnings,omitempty"`
}

type InteractionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type InteractionQuestion struct {
	ID       string              `json:"id"`
	Header   string              `json:"header,omitempty"`
	Question string              `json:"question"`
	Multi    bool                `json:"multi_select,omitempty"`
	IsOther  bool                `json:"is_other,omitempty"`
	IsSecret bool                `json:"is_secret,omitempty"`
	Options  []InteractionOption `json:"options,omitempty"`
}

type InteractionView struct {
	RequestID        string                `json:"request_id"`
	Kind             string                `json:"kind"` // approval | input
	WorkerID         string                `json:"worker_id"`
	WorkerAlias      string                `json:"worker_alias,omitempty"`
	WorkspaceID      string                `json:"workspace_id"`
	WorkspaceRoot    string                `json:"workspace_root,omitempty"`
	HarnessID        string                `json:"harness_id"`
	TurnID           string                `json:"turn_id"`
	ToolName         string                `json:"tool_name,omitempty"`
	Summary          string                `json:"summary"`
	Command          string                `json:"command,omitempty"`
	Paths            []string              `json:"paths,omitempty"`
	Reason           string                `json:"reason,omitempty"`
	RiskLevel        string                `json:"risk_level,omitempty"` // low | medium | high | critical
	HardRedline      bool                  `json:"hard_redline,omitempty"`
	OutsideWorkspace bool                  `json:"outside_workspace,omitempty"`
	AllowSessionOK   bool                  `json:"allow_session_ok,omitempty"`
	Questions        []InteractionQuestion `json:"questions,omitempty"`
	VisibleToHuman   bool                  `json:"visible_to_human"`
	ClaimedByCaller  bool                  `json:"claimed_by_caller,omitempty"`
	EscalateAt       *time.Time            `json:"escalate_at,omitempty"`
	EscalationNote   string                `json:"escalation_note,omitempty"`
	DeferCount       int                   `json:"defer_count,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
}

type CommentaryEntry struct {
	Seq       int64     `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Text      string    `json:"text"`
}

type SteerInput struct {
	TurnID      string
	WorkspaceID string
	Text        string
}

type WaitOptions struct {
	WaitDuration       time.Duration
	AfterCommentarySeq int64
	WakeOnCommentary   bool
}

type InteractionRequest struct {
	Kind          string
	ToolName      string
	Summary       string
	Command       string
	Paths         []string
	Reason        string
	NativeOptions []string
	Questions     []InteractionQuestion
}

type InteractionDecision struct {
	Decision string              `json:"decision"` // allow_once | allow_session | deny | cancel_turn | answer
	Message  string              `json:"message,omitempty"`
	Answers  map[string][]string `json:"answers,omitempty"`
	Text     string              `json:"text,omitempty"`
	Actor    string              `json:"actor,omitempty"` // human | caller | policy | timeout
}

type InteractionHandler func(ctx context.Context, req InteractionRequest) (InteractionDecision, error)

type WaitAnyItem struct {
	TurnID             string           `json:"turn_id"`
	WorkerID           string           `json:"worker_id"`
	WorkspaceID        string           `json:"workspace_id"`
	HarnessID          string           `json:"harness_id"`
	State              string           `json:"state"`
	Progress           string           `json:"progress,omitempty"`
	AnswerAvailable    bool             `json:"answer_available"`
	AnswerSource       string           `json:"answer_source,omitempty"`
	AnswerPreview      string           `json:"answer_preview,omitempty"`
	Truncated           bool              `json:"truncated,omitempty"`
	ResultBytes         int               `json:"result_bytes"`
	NextCursor          string            `json:"next_cursor,omitempty"`
	ElapsedSeconds      int               `json:"elapsed_seconds,omitempty"`
	IdleSeconds        int              `json:"idle_seconds,omitempty"`
	StreamEvents       int64            `json:"stream_events,omitempty"`
	StreamBytes        int64            `json:"stream_bytes,omitempty"`
	ErrorCode          string           `json:"error_code,omitempty"`
	ErrorMessage       string           `json:"error_message,omitempty"`
	AfterVCS            *VCSSnapshot      `json:"after_vcs,omitempty"`
	PendingInteraction  *InteractionView  `json:"pending_interaction,omitempty"`
	LatestCommentarySeq int64             `json:"latest_commentary_seq,omitempty"`
	Commentaries        []CommentaryEntry `json:"commentaries,omitempty"`
}

type VCSSnapshot struct {
	VCS                     string   `json:"vcs"`
	Branch                  string   `json:"branch,omitempty"`
	HeadCommit              string   `json:"head_commit,omitempty"`
	DirtyBaseline           bool     `json:"dirty_baseline"`
	ExternalChangesPossible bool     `json:"external_changes_possible"`
	StagedCount             int      `json:"staged_count"`
	UnstagedCount           int      `json:"unstaged_count"`
	UntrackedCount          int      `json:"untracked_count"`
	ChangedFiles            []string `json:"changed_files,omitempty"`
}

type Turn struct {
	ID                  string       `json:"turn_id"`
	WorkerID            string       `json:"worker_id"`
	WorkspaceID         string       `json:"workspace_id"`
	HarnessID           string       `json:"harness_id"`
	SessionEpoch        int          `json:"session_epoch"`
	OperationID         string       `json:"operation_id"`
	CallerID            string       `json:"caller_id,omitempty"`
	ClientCorrelationID string       `json:"client_correlation_id"`
	NativeSessionID     string       `json:"native_session_id,omitempty"`
	NativeTurnID        string       `json:"native_turn_id,omitempty"`
	AccessMode          string       `json:"access_mode"`
	Model               string       `json:"model,omitempty"`
	ReasoningEffort     string       `json:"reasoning_effort,omitempty"`
	State               string       `json:"state"`
	RevocationRequested bool         `json:"revocation_requested,omitempty"`
	RevocationReason    string       `json:"revocation_reason,omitempty"`
	AnswerSource        string       `json:"answer_source,omitempty"`
	AnswerAvailable     bool         `json:"answer_available"`
	PartialOutput       string       `json:"partial_output,omitempty"`
	ResultBytes         int          `json:"result_bytes"`
	Progress            string       `json:"progress,omitempty"`
	ErrorCode           string       `json:"error_code,omitempty"`
	ErrorMessage        string       `json:"error_message,omitempty"`
	GitRef              string       `json:"git_ref,omitempty"`
	DiffBase            string       `json:"diff_base,omitempty"`
	BaselineVCS         *VCSSnapshot `json:"baseline_vcs,omitempty"`
	AfterVCS            *VCSSnapshot `json:"after_vcs,omitempty"`
	StartedAt           time.Time    `json:"started_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
	FinishedAt          *time.Time   `json:"finished_at,omitempty"`
	ElapsedSeconds     int              `json:"elapsed_seconds,omitempty"`
	IdleSeconds        int              `json:"idle_seconds,omitempty"`
	StreamEvents        int64            `json:"stream_events,omitempty"`
	StreamBytes         int64            `json:"stream_bytes,omitempty"`
	LatestCommentarySeq int64            `json:"latest_commentary_seq,omitempty"`
	PendingInteraction  *InteractionView `json:"pending_interaction,omitempty"`
}

type LogEntry struct {
	Seq       int64     `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"`
	ItemID    string    `json:"item_id,omitempty"`
	Phase     string    `json:"phase,omitempty"`
	Summary   string    `json:"summary"`
}

type NativeItem struct {
	ItemID   string `json:"item_id"`
	Type     string `json:"type"`
	ClientID string `json:"client_id,omitempty"`
	Phase    string `json:"phase,omitempty"`
	Text     string `json:"text,omitempty"`
}

type NativeTurnRecord struct {
	NativeTurnID string       `json:"native_turn_id"`
	Status       string       `json:"status"`
	Items        []NativeItem `json:"items"`
}

type AdapterEvent struct {
	Kind    string
	ItemID  string
	Phase   string
	Summary string
	Bytes   int
	Item    *NativeItem
}

type TurnOutcome struct {
	State        string
	NativeTurnID string
	Items        []NativeItem
	ErrorCode    string
	ErrorMessage string
}

type ThreadOpenSpec struct {
	WorkerID           string
	Executable         string
	WorkspaceRoot      string
	AccessMode         string
	ApprovalMode       string
	SessionAutoApprove bool
	Model              string
	ReasoningEffort    string
	RoleHint           string
	NativeSessionID    string
}

type TurnStartSpec struct {
	ThreadID            string
	WorkspaceRoot       string
	AccessMode          string
	ApprovalMode        string
	SessionAutoApprove  bool
	Prompt              string
	ClientCorrelationID string
	Model               string
	ReasoningEffort     string
	OnInteraction       InteractionHandler
}

type ThreadHandle struct {
	Key             string
	NativeSessionID string
	ProcessRef      any
}

type Adapter interface {
	HarnessID() string
	OpenThread(ctx context.Context, spec ThreadOpenSpec) (ThreadHandle, error)
	StartTurn(ctx context.Context, handle ThreadHandle, spec TurnStartSpec, onEvent func(AdapterEvent)) (string, <-chan TurnOutcome, error)
	InterruptTurn(ctx context.Context, handle ThreadHandle, nativeTurnID string) error
	ForceKill(handle ThreadHandle) error
	InspectThreadTurns(ctx context.Context, handle ThreadHandle) ([]NativeTurnRecord, error)
	CloseThread(handle ThreadHandle) error
}

// SteerAdapter is optionally implemented by adapters whose native protocol
// supports injecting guidance into a currently running turn without cancelling it.
type SteerAdapter interface {
	SteerTurn(ctx context.Context, handle ThreadHandle, nativeTurnID, text string) error
}

// SelectFinalAnswer applies the strict M1 candidate selection rule:
//   - Only status == "completed" may publish an authoritative final answer.
//   - On "completed": collect all agentMessage items with phase == "final_answer"
//     in deduplicated order; if none exist, exclude known commentary/tool items
//     and pick the LAST remaining agentMessage with empty phase or partial_answer.
//   - On any non-completed state (interrupted, cancelled, failed, reconciliation_required):
//     never publish a final answer (available=false), returning any assistant text
//     strictly as explicit partialOutput.
func SelectFinalAnswer(status string, items []NativeItem) (answer string, source string, available bool, partialOutput string) {
	dedup := dedupeItems(items)
	if status != TurnCompleted {
		for i := len(dedup) - 1; i >= 0; i-- {
			it := dedup[i]
			if it.Type != "agentMessage" || it.Phase == "commentary" {
				continue
			}
			if txt := strings.TrimSpace(it.Text); txt != "" {
				return "", AnswerSourceNone, false, txt
			}
		}
		return "", AnswerSourceNone, false, ""
	}

	var finals []string
	for _, it := range dedup {
		if it.Type == "agentMessage" && it.Phase == "final_answer" {
			if txt := strings.TrimSpace(it.Text); txt != "" {
				finals = append(finals, txt)
			}
		}
	}
	if len(finals) > 0 {
		return strings.Join(finals, "\n\n"), AnswerSourceFinal, true, ""
	}

	for i := len(dedup) - 1; i >= 0; i-- {
		it := dedup[i]
		if it.Type != "agentMessage" {
			continue
		}
		if it.Phase == "commentary" {
			continue
		}
		if it.Phase == "" || it.Phase == "partial_answer" {
			if txt := strings.TrimSpace(it.Text); txt != "" {
				return txt, AnswerSourceFallback, true, ""
			}
		}
	}
	return "", AnswerSourceNone, false, ""
}

func dedupeItems(items []NativeItem) []NativeItem {
	out := make([]NativeItem, 0, len(items))
	idxByID := map[string]int{}
	for _, it := range items {
		if it.ItemID != "" {
			if idx, ok := idxByID[it.ItemID]; ok {
				out[idx] = it
				continue
			}
			idxByID[it.ItemID] = len(out)
		}
		out = append(out, it)
	}
	return out
}
