package harnesses

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/launch"
	"github.com/jackgan90/Asterhand/internal/model"
)

type codexObserveCache struct {
	exe        string
	observed   string
	observedAt time.Time
}

type Service struct {
	home       string
	executable string
	// Override paths/runners for isolated tests.
	AntigravityConfigPath string
	ClaudeConfigPath      string
	CmdRunner             CommandRunner
	DisableVersionProbe   bool
	// MaxAccessMode caps allowed worker access mode (defaults to workspace_write in M2).
	MaxAccessMode       string
	OnRevokeWorker      func(harnessID string)
	OnRevokeWorkerWrite func(harnessID string)

	mu           sync.RWMutex
	state        State
	now          func() time.Time
	cacheMu      sync.Mutex
	versionCache map[string]versionCacheEntry
	codexObs     codexObserveCache
}

func Open(home, executable string) (*Service, error) {
	absHome, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	s := &Service{
		home:       absHome,
		executable: executable,
		now:        time.Now,
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) statePath() string { return filepath.Join(s.home, "harnesses.json") }
func (s *Service) callerTokenPath(harnessID string) string {
	return filepath.Join(s.home, "harness-callers", harnessID+".token")
}

func (s *Service) load() error {
	b, err := os.ReadFile(s.statePath())
	if errors.Is(err, os.ErrNotExist) {
		s.state = defaultState()
		return nil
	}
	if err != nil {
		return err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	if st.Schema > 1 {
		return automation.Fail("configuration", "Newer harnesses configuration schema is unsupported")
	}
	if st.Schema == 0 {
		st.Schema = 1
	}
	if st.Revision == "" {
		st.Revision = model.ID()
	}
	if st.Harnesses == nil {
		st.Harnesses = map[string]HarnessPolicy{}
	}
	for _, id := range KnownHarnessIDs() {
		p, ok := st.Harnesses[id]
		if !ok {
			p = HarnessPolicy{ID: id}
		}
		if caps, _ := StaticCapabilities(id); !caps.SupportsWorker {
			p.CollabWorkerEnabled = false
			p.AllowWorkspaceWrite = false
		}
		st.Harnesses[id] = p
	}
	s.state = st
	return nil
}

func defaultState() State {
	hm := map[string]HarnessPolicy{}
	for _, id := range KnownHarnessIDs() {
		hm[id] = HarnessPolicy{ID: id}
	}
	return State{Schema: 1, Revision: model.ID(), Harnesses: hm}
}

func (s *Service) saveLocked() error {
	s.state.Revision = model.ID()
	b, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return config.AtomicFile(s.statePath(), b, 0600)
}

// mcpSpecForCaller resolves the stable versionless <InstallRoot>\asterhand-mcp.exe
// entry when running from an installed node, falling back to direct asterhand.exe mcp
// in development mode.
func (s *Service) mcpSpecForCaller(callerID string) MCPEntrySpec {
	spec := MCPEntrySpec{
		CallerID: callerID,
		NodeHome: s.home,
		Command:  s.executable,
		Args:     []string{"mcp", "--home", s.home, "--caller", callerID},
	}
	if s.executable == "" {
		return spec
	}
	if installPath := launch.FindInstallation(s.executable, s.home); installPath != "" {
		stable := filepath.Join(filepath.Dir(installPath), "asterhand-mcp.exe")
		if info, err := os.Stat(stable); err == nil && !info.IsDir() {
			spec.StableCommand = stable
			spec.Command = stable
			spec.Args = []string{"--caller", callerID}
		}
	}
	return spec
}

// AuthenticateCaller maps a revocable managed-harness token to a live Principal
// reflecting the harness's current general/collab switches.
func (s *Service) AuthenticateCaller(token string) (automation.Principal, bool, error) {
	if len(token) < 32 {
		return automation.Principal{}, false, nil
	}
	sum := sha256.Sum256([]byte(token))
	hexHash := hex.EncodeToString(sum[:])

	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range KnownHarnessIDs() {
		p := s.state.Harnesses[id]
		if p.CredentialHash == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(hexHash), []byte(p.CredentialHash)) != 1 {
			continue
		}
		ts := map[string]bool{}
		scopes := map[string][]string{}
		if p.GeneralToolsEnabled {
			ts[automation.ToolsetGeneral] = true
			scopes["commands"] = []string{"*"}
			scopes["projects"] = []string{"*"}
			scopes["conversations"] = []string{"*"}
			scopes["workspaces"] = []string{"*"}
			scopes["node"] = []string{"*"}
		}
		if p.CollabCallerEnabled {
			ts[automation.ToolsetCollab] = true
			scopes["workers"] = []string{"*"}
		}
		roles := []string{}
		if len(ts) > 0 {
			roles = []string{"read", "edit", "execute"}
		}
		return automation.Principal{
			ID:             "harness:" + id,
			Name:           "Managed harness " + id,
			CallerID:       id,
			PolicyRevision: s.state.Revision,
			Roles:          roles,
			Scopes:         scopes,
			Toolsets:       ts,
		}, true, nil
	}
	return automation.Principal{}, false, nil
}

func (s *Service) Status() StatusView {
	s.mu.RLock()
	rev := s.state.Revision
	policies := make(map[string]HarnessPolicy, len(s.state.Harnesses))
	for k, v := range s.state.Harnesses {
		policies[k] = v
	}
	s.mu.RUnlock()

	views := make([]HarnessView, 0, len(KnownHarnessIDs()))
	for _, id := range KnownHarnessIDs() {
		views = append(views, s.buildHarnessView(id, policies[id]))
	}
	return StatusView{Revision: rev, Harnesses: views}
}

func (s *Service) Get(harnessID string) (HarnessView, bool) {
	if _, ok := StaticCapabilities(harnessID); !ok {
		return HarnessView{}, false
	}
	s.mu.RLock()
	p := s.state.Harnesses[harnessID]
	s.mu.RUnlock()
	return s.buildHarnessView(harnessID, p), true
}

func (s *Service) buildHarnessView(id string, p HarnessPolicy) HarnessView {
	caps, _ := StaticCapabilities(id)
	exe, detected := s.resolveExecutable(id, p.ManualPath)
	ver := ""
	if detected {
		ver = s.probeVersion(id, exe)
	}
	observed, mcpPath := s.observeHarnessMCP(id, p, exe, detected)
	return HarnessView{
		ID:                  id,
		Capabilities:        caps,
		Detected:            detected,
		Version:             ver,
		ExecutablePath:      exe,
		ManualPath:          p.ManualPath,
		GeneralToolsEnabled: p.GeneralToolsEnabled,
		CollabCallerEnabled: p.CollabCallerEnabled,
		CollabWorkerEnabled: p.CollabWorkerEnabled,
		AllowWorkspaceWrite:    p.AllowWorkspaceWrite,
		CallerCredentialActive: p.CredentialHash != "",
		MCPDesired:             p.GeneralToolsEnabled || p.CollabCallerEnabled,
		MCPObserved:         observed,
		MCPConfigPath:       mcpPath,
		LastReconcileError:  p.LastReconcileError,
	}
}

func (s *Service) observeHarnessMCP(id string, p HarnessPolicy, exe string, detected bool) (string, string) {
	spec := s.mcpSpecForCaller(id)
	switch id {
	case HarnessAntigravity:
		agPath := s.antigravityConfigPath()
		if agPath == "" {
			return MCPObservedNotConfigured, ""
		}
		if obs, err := ObserveAntigravityMCP(agPath, spec); err == nil {
			return obs, agPath
		}
		return MCPObservedError, agPath
	case HarnessClaudeCode:
		clPath := s.claudeConfigPath()
		if clPath == "" {
			return MCPObservedNotConfigured, ""
		}
		if obs, err := ObserveClaudeMCP(clPath, spec); err == nil {
			return obs, clPath
		}
		return MCPObservedError, clPath
	case HarnessCodex:
		cfgPath, _ := DefaultCodexConfigPath()
		if !detected || exe == "" {
			if p.LastObservedMCP != "" {
				return p.LastObservedMCP, cfgPath
			}
			return MCPObservedNotConfigured, cfgPath
		}
		s.cacheMu.Lock()
		cached := s.codexObs
		s.cacheMu.Unlock()
		if SamePath(cached.exe, exe) && cached.observed != "" && s.now().Sub(cached.observedAt) < 5*time.Second {
			return cached.observed, cfgPath
		}
		obs, err := ObserveCodexMCP(context.Background(), s.runner(), exe, spec)
		if err != nil {
			obs = MCPObservedError
		}
		s.cacheMu.Lock()
		s.codexObs = codexObserveCache{exe: exe, observed: obs, observedAt: s.now()}
		s.cacheMu.Unlock()
		return obs, cfgPath
	default:
		if p.LastObservedMCP != "" {
			return p.LastObservedMCP, ""
		}
		return MCPObservedNotConfigured, ""
	}
}

func (s *Service) invalidateCodexObserveCache() {
	s.cacheMu.Lock()
	s.codexObs = codexObserveCache{}
	s.cacheMu.Unlock()
}

func (s *Service) effectiveMaxAccessMode() string {
	if AccessModeRank(s.MaxAccessMode) > 0 {
		return s.MaxAccessMode
	}
	return AccessWorkspaceWrite
}

// ResolveWorkspace validates any local project directory against the built-in
// sensitive-path denylist, checks access_mode against the node's cap, and
// derives a deterministic per-project workspace identity without manual registration.
func (s *Service) ResolveWorkspace(rawPath, requestedAccess string) (DelegatedWorkspace, string, error) {
	if AccessModeRank(requestedAccess) == 0 {
		return DelegatedWorkspace{}, "", automation.Fail("invalid_parameters", "access_mode must be read_only or workspace_write")
	}
	maxMode := s.effectiveMaxAccessMode()
	if AccessModeRank(requestedAccess) > AccessModeRank(maxMode) {
		return DelegatedWorkspace{}, "", automation.Fail("unsupported_access_mode", "Requested access_mode is not enabled on this node")
	}
	canon, err := ValidateWorkspacePath(rawPath, s.home)
	if err != nil {
		return DelegatedWorkspace{}, "", err
	}
	ws := DelegatedWorkspace{
		ID:            DeriveWorkspaceID(canon),
		Name:          filepath.Base(canon),
		RootPath:      canon,
		MaxAccessMode: maxMode,
	}
	return ws, canon, nil
}

// CheckArtifactPath verifies that rawArtifact resolves inside ws.RootPath.
func CheckArtifactPath(ws DelegatedWorkspace, rawArtifact string) (string, error) {
	trimmed := strings.TrimSpace(rawArtifact)
	if trimmed == "" || strings.ContainsAny(trimmed, "\x00\r\n") {
		return "", automation.Fail("invalid_parameters", "Invalid artifact path")
	}
	target := trimmed
	if !filepath.IsAbs(target) {
		target = filepath.Join(ws.RootPath, target)
	}
	canon, err := CanonicalPath(target)
	if err != nil {
		return "", automation.Fail("invalid_parameters", "Cannot resolve artifact path")
	}
	if !WithinRoot(ws.RootPath, canon) {
		return "", automation.Fail("forbidden", "Artifact path escapes authorized workspace root")
	}
	return canon, nil
}

type UpdatePolicyInput struct {
	HarnessID           string  `json:"harness_id"`
	GeneralToolsEnabled *bool   `json:"general_tools_enabled,omitempty"`
	CollabCallerEnabled *bool   `json:"collab_caller_enabled,omitempty"`
	CollabWorkerEnabled *bool   `json:"collab_worker_enabled,omitempty"`
	AllowWorkspaceWrite *bool   `json:"allow_workspace_write,omitempty"`
	ManualPath          *string `json:"manual_path,omitempty"`
	ReconcileMCP        bool    `json:"reconcile_mcp,omitempty"`
	TakeOverConflict    bool    `json:"take_over_conflict,omitempty"`
}

func (s *Service) UpdatePolicy(in UpdatePolicyInput) (StatusView, error) {
	caps, ok := StaticCapabilities(in.HarnessID)
	if !ok {
		return StatusView{}, automation.Fail("invalid_parameters", "Unknown harness_id")
	}
	s.mu.Lock()
	p := s.state.Harnesses[in.HarnessID]
	wasWorker := p.CollabWorkerEnabled
	wasWrite := p.AllowWorkspaceWrite
	if in.GeneralToolsEnabled != nil {
		p.GeneralToolsEnabled = *in.GeneralToolsEnabled
	}
	if in.CollabCallerEnabled != nil {
		p.CollabCallerEnabled = *in.CollabCallerEnabled
	}
	if in.CollabWorkerEnabled != nil {
		if *in.CollabWorkerEnabled && !caps.SupportsWorker {
			s.mu.Unlock()
			return StatusView{}, automation.Fail("unsupported_worker", "This harness cannot act as a managed worker")
		}
		p.CollabWorkerEnabled = *in.CollabWorkerEnabled
	}
	if in.AllowWorkspaceWrite != nil {
		if *in.AllowWorkspaceWrite && !caps.SupportsWorker {
			s.mu.Unlock()
			return StatusView{}, automation.Fail("unsupported_worker", "This harness cannot act as a managed worker")
		}
		p.AllowWorkspaceWrite = *in.AllowWorkspaceWrite
	}
	if in.ManualPath != nil {
		p.ManualPath = strings.TrimSpace(*in.ManualPath)
	}

	mcpDesired := p.GeneralToolsEnabled || p.CollabCallerEnabled
	if mcpDesired && p.CredentialHash == "" {
		if err := s.issueCredentialLocked(&p); err != nil {
			s.mu.Unlock()
			return StatusView{}, err
		}
	} else if !mcpDesired && p.CredentialHash != "" {
		s.revokeCredentialLocked(&p)
	}
	s.state.Harnesses[in.HarnessID] = p
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return StatusView{}, err
	}
	revokeWorkerCB := s.OnRevokeWorker
	revokeWriteCB := s.OnRevokeWorkerWrite
	s.mu.Unlock()

	if wasWorker && !p.CollabWorkerEnabled && revokeWorkerCB != nil {
		revokeWorkerCB(in.HarnessID)
	} else if wasWrite && !p.AllowWorkspaceWrite && revokeWriteCB != nil {
		revokeWriteCB(in.HarnessID)
	}

	if in.ReconcileMCP && caps.CallerManaged && s.executable != "" {
		s.reconcileHarnessMCP(in.HarnessID, p.ManualPath, mcpDesired, in.TakeOverConflict)
	}

	return s.Status(), nil
}

func (s *Service) reconcileHarnessMCP(harnessID, manualPath string, desired, takeOver bool) {
	spec := s.mcpSpecForCaller(harnessID)
	var (
		obs  string
		rec  *ManagedMCPRecord
		rErr error
	)
	switch harnessID {
	case HarnessAntigravity:
		agPath := s.antigravityConfigPath()
		if agPath == "" {
			return
		}
		obs, rec, rErr = ReconcileAntigravityMCP(agPath, spec, desired, takeOver)
	case HarnessCodex:
		exe, ok := s.resolveExecutable(HarnessCodex, manualPath)
		if !ok {
			obs, rErr = MCPObservedError, errors.New("codex executable not found")
		} else {
			obs, rec, rErr = ReconcileCodexMCP(context.Background(), s.runner(), exe, spec, desired, takeOver)
		}
		s.invalidateCodexObserveCache()
	case HarnessClaudeCode:
		clPath := s.claudeConfigPath()
		exe, ok := s.resolveExecutable(HarnessClaudeCode, manualPath)
		if !ok {
			obs, rErr = MCPObservedError, errors.New("claude executable not found")
		} else {
			obs, rec, rErr = ReconcileClaudeMCP(context.Background(), s.runner(), exe, clPath, spec, desired, takeOver)
		}
	default:
		return
	}

	s.mu.Lock()
	cur := s.state.Harnesses[harnessID]
	cur.LastObservedMCP = obs
	cur.LastReconcileAt = s.now().UTC()
	if rErr != nil {
		cur.LastReconcileError = rErr.Error()
	} else {
		cur.LastReconcileError = ""
		cur.ManagedMCP = rec
	}
	s.state.Harnesses[harnessID] = cur
	_ = s.saveLocked()
	s.mu.Unlock()
}

// ReconcileManagedOnStartup refreshes already-enabled managed MCP entries when
// the node starts (e.g. after upgrading from versioned asterhand.exe to stable
// asterhand-mcp.exe), without touching unmanaged conflicting entries or disabled harnesses.
func (s *Service) ReconcileManagedOnStartup() {
	if s.executable == "" {
		return
	}
	for _, id := range KnownHarnessIDs() {
		caps, ok := StaticCapabilities(id)
		if !ok || !caps.CallerManaged {
			continue
		}
		s.mu.RLock()
		p := s.state.Harnesses[id]
		s.mu.RUnlock()
		mcpDesired := p.GeneralToolsEnabled || p.CollabCallerEnabled
		if !mcpDesired {
			continue
		}
		spec := s.mcpSpecForCaller(id)
		exe, detected := s.resolveExecutable(id, p.ManualPath)
		if (id == HarnessCodex || id == HarnessClaudeCode) && !detected {
			continue
		}
		obs, _ := s.observeHarnessMCP(id, p, exe, detected)
		if obs == MCPObservedConflict {
			continue
		}
		if obs == MCPObservedRegistered && p.ManagedMCP != nil &&
			SamePath(p.ManagedMCP.Command, spec.Command) &&
			slices.Equal(p.ManagedMCP.Args, spec.Args) {
			continue
		}
		s.reconcileHarnessMCP(id, p.ManualPath, true, false)
	}
}

// RevokeCallerCredential immediately invalidates the harness's caller token
// without changing worker authorization.
func (s *Service) RevokeCallerCredential(harnessID string) (StatusView, error) {
	if _, ok := StaticCapabilities(harnessID); !ok {
		return StatusView{}, automation.Fail("invalid_parameters", "Unknown harness_id")
	}
	s.mu.Lock()
	p := s.state.Harnesses[harnessID]
	s.revokeCredentialLocked(&p)
	p.GeneralToolsEnabled = false
	p.CollabCallerEnabled = false
	s.state.Harnesses[harnessID] = p
	err := s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return StatusView{}, err
	}
	return s.Status(), nil
}

// IssueCallerCredential rotates or creates a fresh revocable token for harnessID
// and returns the caller token path (never the raw secret in status endpoints).
func (s *Service) IssueCallerCredential(harnessID string) (string, error) {
	if _, ok := StaticCapabilities(harnessID); !ok {
		return "", automation.Fail("invalid_parameters", "Unknown harness_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.state.Harnesses[harnessID]
	if err := s.issueCredentialLocked(&p); err != nil {
		return "", err
	}
	s.state.Harnesses[harnessID] = p
	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return s.callerTokenPath(harnessID), nil
}

func (s *Service) CallerTokenForTest(harnessID string) (string, error) {
	b, err := os.ReadFile(s.callerTokenPath(harnessID))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (s *Service) IsWorkerEnabled(harnessID string) (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.state.Harnesses[harnessID]
	if !ok || !p.CollabWorkerEnabled {
		return false, ""
	}
	exe, found := s.resolveExecutable(harnessID, p.ManualPath)
	if !found {
		return true, ""
	}
	return true, exe
}

// IsWorkerAccessEnabled checks both worker authorization and per-harness
// workspace_write opt-in, returning the resolved executable path when allowed.
func (s *Service) IsWorkerAccessEnabled(harnessID, accessMode string) (string, error) {
	s.mu.RLock()
	p, ok := s.state.Harnesses[harnessID]
	s.mu.RUnlock()
	if !ok || !p.CollabWorkerEnabled {
		return "", automation.Fail("forbidden", "Target harness worker authorization is disabled")
	}
	if accessMode == AccessWorkspaceWrite && !p.AllowWorkspaceWrite {
		return "", automation.Fail("forbidden", "Workspace write mode is not enabled for this harness")
	}
	exe, found := s.resolveExecutable(harnessID, p.ManualPath)
	if !found {
		return "", nil
	}
	return exe, nil
}

func (s *Service) issueCredentialLocked(p *HarnessPolicy) error {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	path := s.callerTokenPath(p.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := config.AtomicFile(path, []byte(token), 0600); err != nil {
		return err
	}
	p.CredentialHash = hex.EncodeToString(sum[:])
	p.CredentialIssuedAt = s.now().UTC()
	return nil
}

func (s *Service) revokeCredentialLocked(p *HarnessPolicy) {
	p.CredentialHash = ""
	p.CredentialIssuedAt = time.Time{}
	_ = os.Remove(s.callerTokenPath(p.ID))
}

func (s *Service) antigravityConfigPath() string {
	if s.AntigravityConfigPath != "" {
		return s.AntigravityConfigPath
	}
	p, err := DefaultAntigravityMCPConfigPath()
	if err != nil {
		return ""
	}
	return p
}

func (s *Service) claudeConfigPath() string {
	if s.ClaudeConfigPath != "" {
		return s.ClaudeConfigPath
	}
	p, err := DefaultClaudeConfigPath()
	if err != nil {
		return ""
	}
	return p
}
