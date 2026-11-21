package harnesses

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/model"
)

type Service struct {
	home       string
	executable string
	// Override path for tests; defaults to DefaultAntigravityMCPConfigPath().
	AntigravityConfigPath string
	// MaxAccessMode caps allowed worker access mode (M1 defaults to read_only).
	MaxAccessMode  string
	OnRevokeWorker func(harnessID string)
	mu             sync.RWMutex
	state          State
	now            func() time.Time
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
	agPath := s.antigravityConfigPath()
	s.mu.RUnlock()

	views := make([]HarnessView, 0, len(KnownHarnessIDs()))
	for _, id := range KnownHarnessIDs() {
		p := policies[id]
		caps, _ := StaticCapabilities(id)
		exe, detected := s.resolveExecutable(id, p.ManualPath)
		observed := p.LastObservedMCP
		mcpPath := ""
		if id == HarnessAntigravity && agPath != "" {
			mcpPath = agPath
			if obs, err := ObserveAntigravityMCP(agPath, s.home); err == nil {
				observed = obs
			} else {
				observed = MCPObservedError
			}
		} else if observed == "" {
			observed = MCPObservedNotConfigured
		}
		views = append(views, HarnessView{
			ID:                  id,
			Capabilities:        caps,
			Detected:            detected,
			ExecutablePath:      exe,
			ManualPath:          p.ManualPath,
			GeneralToolsEnabled: p.GeneralToolsEnabled,
			CollabCallerEnabled: p.CollabCallerEnabled,
			CollabWorkerEnabled: p.CollabWorkerEnabled,
			CredentialActive:    p.CredentialHash != "",
			MCPDesired:          p.GeneralToolsEnabled || p.CollabCallerEnabled,
			MCPObserved:         observed,
			MCPConfigPath:       mcpPath,
			LastReconcileError:  p.LastReconcileError,
		})
	}
	return StatusView{Revision: rev, Harnesses: views}
}

func (s *Service) effectiveMaxAccessMode() string {
	if AccessModeRank(s.MaxAccessMode) > 0 {
		return s.MaxAccessMode
	}
	return AccessReadOnly
}

// ResolveWorkspace validates any local project directory against the built-in
// sensitive-path denylist, checks access_mode against the node's allowed level,
// and derives a deterministic per-project workspace identity without manual registration.
func (s *Service) ResolveWorkspace(rawPath, requestedAccess string) (DelegatedWorkspace, string, error) {
	if AccessModeRank(requestedAccess) == 0 {
		return DelegatedWorkspace{}, "", automation.Fail("invalid_parameters", "access_mode must be read_only or workspace_write")
	}
	maxMode := s.effectiveMaxAccessMode()
	if AccessModeRank(requestedAccess) > AccessModeRank(maxMode) {
		return DelegatedWorkspace{}, "", automation.Fail("unsupported_access_mode", "Requested access_mode is not enabled in this milestone")
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
	agPath := s.antigravityConfigPath()
	exe := s.executable
	s.mu.Unlock()

	if wasWorker && !p.CollabWorkerEnabled && revokeWorkerCB != nil {
		revokeWorkerCB(in.HarnessID)
	}

	if in.ReconcileMCP && in.HarnessID == HarnessAntigravity && agPath != "" && exe != "" {
		obs, rec, rErr := ReconcileAntigravityMCP(agPath, exe, s.home, mcpDesired, in.TakeOverConflict)
		s.mu.Lock()
		cur := s.state.Harnesses[in.HarnessID]
		cur.LastObservedMCP = obs
		cur.LastReconcileAt = s.now().UTC()
		if rErr != nil {
			cur.LastReconcileError = rErr.Error()
		} else {
			cur.LastReconcileError = ""
			cur.ManagedMCP = rec
		}
		s.state.Harnesses[in.HarnessID] = cur
		_ = s.saveLocked()
		s.mu.Unlock()
	}

	return s.Status(), nil
}

// ReconcileManagedOnStartup refreshes an already-enabled Antigravity managed MCP
// entry when the node starts from a newly installed version directory, without
// touching unmanaged conflicting entries or inactive test homes.
func (s *Service) ReconcileManagedOnStartup() {
	s.mu.RLock()
	p := s.state.Harnesses[HarnessAntigravity]
	mcpDesired := p.GeneralToolsEnabled || p.CollabCallerEnabled
	agPath := s.antigravityConfigPath()
	exe := s.executable
	prevCmd := ""
	if p.ManagedMCP != nil {
		prevCmd = p.ManagedMCP.Command
	}
	s.mu.RUnlock()

	if !mcpDesired || agPath == "" || exe == "" {
		return
	}
	obs, err := ObserveAntigravityMCP(agPath, s.home)
	if err == nil && obs == MCPObservedRegistered && SamePath(prevCmd, exe) {
		return
	}
	if err == nil && obs == MCPObservedConflict {
		return
	}
	newObs, rec, rErr := ReconcileAntigravityMCP(agPath, exe, s.home, true, false)
	s.mu.Lock()
	cur := s.state.Harnesses[HarnessAntigravity]
	cur.LastObservedMCP = newObs
	cur.LastReconcileAt = s.now().UTC()
	if rErr != nil {
		cur.LastReconcileError = rErr.Error()
	} else {
		cur.LastReconcileError = ""
		cur.ManagedMCP = rec
	}
	s.state.Harnesses[HarnessAntigravity] = cur
	_ = s.saveLocked()
	s.mu.Unlock()
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

func (s *Service) resolveExecutable(harnessID, manual string) (string, bool) {
	if manual != "" {
		if info, err := os.Stat(manual); err == nil && !info.IsDir() {
			return manual, true
		}
		return manual, false
	}
	candidates := map[string][]string{
		HarnessAntigravity: {"agy.exe", "agy"},
		HarnessCodex:       {"codex.exe", "codex.cmd", "codex"},
		HarnessClaudeCode:  {"claude.exe", "claude.cmd", "claude"},
		HarnessDeepSeek:    {"dsh.exe", "dsh.cmd", "dsh"},
	}[harnessID]
	for _, name := range candidates {
		if p, err := exec.LookPath(name); err == nil {
			return p, true
		}
	}
	return "", false
}
