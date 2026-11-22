package harnesses

import "time"

const (
	HarnessAntigravity = "antigravity"
	HarnessCodex       = "codex"
	HarnessClaudeCode  = "claude-code"
	HarnessDeepSeek    = "deepseek-harness"
)

const (
	AccessReadOnly       = "read_only"
	AccessWorkspaceWrite = "workspace_write"
)

const (
	ProtectionOSSandbox       = "os_sandbox_enforced"
	ProtectionHarnessRestrict = "harness_tool_restricted"
	ProtectionUnsupported     = "unsupported"
)

type Capabilities struct {
	SupportsCaller       bool     `json:"supports_caller"`
	SupportsWorker       bool     `json:"supports_worker"`
	CallerManaged        bool     `json:"caller_managed"`
	WorkerManaged        bool     `json:"worker_managed"`
	ReadOnlyLevel        string   `json:"read_only_level"`
	WorkspaceWriteLevel  string   `json:"workspace_write_level"`
	HasNativeFileTools   bool     `json:"has_native_file_tools,omitempty"`
	SupportsAutoApproval bool     `json:"supports_auto_approval"`
	ReasoningEfforts     []string `json:"reasoning_efforts,omitempty"`
	WorkerNoticeKey      string   `json:"worker_notice_key,omitempty"`
}

func StaticCapabilities(harnessID string) (Capabilities, bool) {
	switch harnessID {
	case HarnessAntigravity:
		return Capabilities{
			SupportsCaller:       true,
			SupportsWorker:       false,
			CallerManaged:        true,
			WorkerManaged:        false,
			ReadOnlyLevel:        ProtectionUnsupported,
			WorkspaceWriteLevel:  ProtectionUnsupported,
			SupportsAutoApproval: false,
			WorkerNoticeKey:      "harnesses.notice.antigravity_caller_only",
		}, true
	case HarnessCodex:
		return Capabilities{
			SupportsCaller:       true,
			SupportsWorker:       true,
			CallerManaged:        true,
			WorkerManaged:        true,
			ReadOnlyLevel:        ProtectionOSSandbox,
			WorkspaceWriteLevel:  ProtectionOSSandbox,
			HasNativeFileTools:   true,
			SupportsAutoApproval: true,
			ReasoningEfforts:     []string{"low", "medium", "high", "xhigh"},
		}, true
	case HarnessClaudeCode:
		return Capabilities{
			SupportsCaller:       true,
			SupportsWorker:       true,
			CallerManaged:        true,
			WorkerManaged:        true,
			ReadOnlyLevel:        ProtectionHarnessRestrict,
			WorkspaceWriteLevel:  ProtectionHarnessRestrict,
			HasNativeFileTools:   true,
			SupportsAutoApproval: true,
			ReasoningEfforts:     []string{"low", "medium", "high", "xhigh", "max"},
		}, true
	case HarnessDeepSeek:
		return Capabilities{
			SupportsCaller:       true,
			SupportsWorker:       true,
			CallerManaged:        false,
			WorkerManaged:        true,
			ReadOnlyLevel:        ProtectionHarnessRestrict,
			WorkspaceWriteLevel:  ProtectionHarnessRestrict,
			HasNativeFileTools:   true,
			SupportsAutoApproval: false,
			ReasoningEfforts:     []string{"off", "low", "high", "max"},
		}, true
	default:
		return Capabilities{}, false
	}
}

func KnownHarnessIDs() []string {
	return []string{HarnessAntigravity, HarnessCodex, HarnessClaudeCode, HarnessDeepSeek}
}

type DelegatedWorkspace struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	RootPath      string    `json:"root_path"`
	MaxAccessMode string    `json:"max_access_mode"`
	CreatedAt     time.Time `json:"created_at"`
}

type ManagedMCPRecord struct {
	ConfigPath string    `json:"config_path,omitempty"`
	Command    string    `json:"command,omitempty"`
	Args       []string  `json:"args,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

type HarnessPolicy struct {
	ID                  string            `json:"id"`
	ManualPath          string            `json:"manual_path,omitempty"`
	GeneralToolsEnabled bool              `json:"general_tools_enabled"`
	CollabCallerEnabled bool              `json:"collab_caller_enabled"`
	CollabWorkerEnabled bool              `json:"collab_worker_enabled"`
	AllowWorkspaceWrite bool              `json:"allow_workspace_write"`
	CredentialHash      string            `json:"credential_hash,omitempty"`
	CredentialIssuedAt  time.Time         `json:"credential_issued_at,omitempty"`
	ManagedMCP          *ManagedMCPRecord `json:"managed_mcp,omitempty"`
	LastObservedMCP     string            `json:"last_observed_mcp,omitempty"`
	LastReconcileError  string            `json:"last_reconcile_error,omitempty"`
	LastReconcileAt     time.Time         `json:"last_reconcile_at,omitempty"`
}

type State struct {
	Schema    int                      `json:"schema"`
	Revision  string                   `json:"revision"`
	Harnesses map[string]HarnessPolicy `json:"harnesses"`
}

type HarnessView struct {
	ID                  string       `json:"id"`
	Capabilities        Capabilities `json:"capabilities"`
	Detected            bool         `json:"detected"`
	Version             string       `json:"version,omitempty"`
	ExecutablePath      string       `json:"executable_path,omitempty"`
	ManualPath          string       `json:"manual_path,omitempty"`
	GeneralToolsEnabled bool         `json:"general_tools_enabled"`
	CollabCallerEnabled bool         `json:"collab_caller_enabled"`
	CollabWorkerEnabled bool         `json:"collab_worker_enabled"`
	AllowWorkspaceWrite    bool         `json:"allow_workspace_write"`
	CallerCredentialActive bool         `json:"caller_credential_active" description:"Whether Asterhand has issued a local MCP caller token for this harness to call Asterhand (true only when general_tools_enabled or collab_caller_enabled is enabled; unrelated to the harness's own LLM credentials)"`
	MCPDesired             bool         `json:"mcp_desired"`
	MCPObserved         string       `json:"mcp_observed"`
	MCPConfigPath       string       `json:"mcp_config_path,omitempty"`
	LastReconcileError  string       `json:"last_reconcile_error,omitempty"`
}

type StatusView struct {
	Revision  string        `json:"revision"`
	Harnesses []HarnessView `json:"harnesses"`
}

func AccessModeRank(mode string) int {
	switch mode {
	case AccessReadOnly:
		return 1
	case AccessWorkspaceWrite:
		return 2
	default:
		return 0
	}
}
