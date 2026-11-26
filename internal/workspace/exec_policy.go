package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/platform/sandbox"
)

type policyDecisionKind int

const (
	decisionTier1 policyDecisionKind = iota
	decisionAllowTier2
	decisionAsk
	decisionDeny
)

type policyDecision struct {
	kind                 policyDecisionKind
	code                 string
	approvalMode         string
	scopeKind            string // "folder" | "command" | "workspace_escalated"
	scopeTarget          string
	scopeTargets         []string
	reason               string
	allFolders           []string
	externalDirs         []string
	allowAnyNonProtected bool
	canSession           bool
	canAlways            bool
	inv                  commandInvocation
}

func (s *Service) ConfigureTrust(mode, trustRev string, rules []config.TrustRule) {
	if s == nil || s.exec == nil {
		return
	}
	s.exec.configureTrust(mode, trustRev, rules)
}

func (s *Service) SetTrustCallbacks(persistFn func([]config.TrustRule) error, onApprovalPending func()) {
	if s == nil || s.exec == nil {
		return
	}
	s.exec.setTrustCallbacks(persistFn, onApprovalPending)
}

func (m *execManager) setTrustCallbacks(persistFn func([]config.TrustRule) error, onApprovalPending func()) {
	m.policyMu.Lock()
	m.persistTrustFn = persistFn
	m.onApprovalPending = onApprovalPending
	m.policyMu.Unlock()
}

func (m *execManager) configureTrust(mode, trustRev string, rules []config.TrustRule) {
	if mode != config.ExecutionModeTurbo {
		mode = config.ExecutionModeStandard
	}
	cloned := append([]config.TrustRule(nil), rules...)
	m.policyMu.Lock()
	prevMode := m.execMode
	prevAllows := filterTrustRules(m.trustRules, "allow")
	m.execMode = mode
	m.trustRev = trustRev
	m.trustRules = cloned
	m.pruneInvalidatedSessionTrustLocked(prevMode, mode, prevAllows, cloned)
	m.policyMu.Unlock()
}

func (m *execManager) pruneInvalidatedSessionTrustLocked(prevMode, nextMode string, prevAllows, nextRules []config.TrustRule) {
	if prevMode == config.ExecutionModeTurbo && nextMode == config.ExecutionModeStandard {
		m.sessionRules = map[string][]config.TrustRule{}
		m.sessionWorkspaceEscalated = map[string]bool{}
		return
	}
	nextAllows := filterTrustRules(nextRules, "allow")
	removedAllows := make([]config.TrustRule, 0)
	for _, oldRule := range prevAllows {
		if !trustRuleExists(nextAllows, oldRule) {
			removedAllows = append(removedAllows, oldRule)
		}
	}
	restrictive := make([]config.TrustRule, 0)
	for _, r := range nextRules {
		if r.Action == "deny" || r.Action == "ask" {
			restrictive = append(restrictive, r)
		}
	}
	if len(removedAllows) == 0 && len(restrictive) == 0 {
		return
	}
	for conv, list := range m.sessionRules {
		kept := list[:0]
		for _, sr := range list {
			if ruleInvalidated(sr, removedAllows, restrictive) {
				continue
			}
			kept = append(kept, sr)
		}
		if len(kept) == 0 {
			delete(m.sessionRules, conv)
		} else {
			m.sessionRules[conv] = kept
		}
	}
	for _, r := range restrictive {
		if r.Kind == "command" && (r.Pattern == "*" || genericShellOrHostSet[strings.ToLower(r.Pattern)]) {
			m.sessionWorkspaceEscalated = map[string]bool{}
			break
		}
	}
}

func (m *execManager) trustSnapshot() (string, string, int, []TrustItemView) {
	if m == nil {
		return config.ExecutionModeStandard, "", 0, nil
	}
	m.policyMu.RLock()
	defer m.policyMu.RUnlock()
	mode := m.execMode
	if mode == "" {
		mode = config.ExecutionModeStandard
	}
	var allowed []TrustItemView
	for _, r := range m.trustRules {
		if r.Action != "allow" {
			continue
		}
		label := strings.TrimSpace(r.Label)
		if label == "" {
			label = r.Pattern
		}
		allowed = append(allowed, TrustItemView{ID: r.ID, Kind: r.Kind, Label: label})
	}
	return mode, m.trustRev, len(m.trustRules), allowed
}

func filterTrustRules(rules []config.TrustRule, action string) []config.TrustRule {
	var out []config.TrustRule
	for _, r := range rules {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func trustRuleExists(rules []config.TrustRule, target config.TrustRule) bool {
	for _, r := range rules {
		if r.Kind == target.Kind && r.Action == target.Action && strings.EqualFold(r.Pattern, target.Pattern) {
			return true
		}
	}
	return false
}

func ruleInvalidated(sr config.TrustRule, removedAllows, restrictive []config.TrustRule) bool {
	for _, rem := range removedAllows {
		if sr.Kind == rem.Kind && trustPatternsOverlap(sr.Kind, sr.Pattern, rem.Pattern) {
			return true
		}
	}
	for _, res := range restrictive {
		if sr.Kind == res.Kind && trustPatternsOverlap(sr.Kind, sr.Pattern, res.Pattern) {
			return true
		}
	}
	return false
}

func trustPatternsOverlap(kind, a, b string) bool {
	if kind == "folder" {
		return sandbox.PathWithin(a, b) || sandbox.PathWithin(b, a)
	}
	if strings.EqualFold(a, b) {
		return true
	}
	return commandToolStem(a) != "" && strings.EqualFold(commandToolStem(a), commandToolStem(b))
}

func commandToolStem(raw string) string {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(raw)))
	for _, ext := range []string{".exe", ".com", ".cmd", ".bat", ".ps1"} {
		base = strings.TrimSuffix(base, ext)
	}
	return base
}

func (s *Service) evaluateExecPolicy(convID, summary string, req ExecStartRequest, homeRoot, nodeDir string) (policyDecision, error) {
	protected := sandbox.DefaultProtectedDirs(homeRoot)
	if sandbox.CheckElevationVerbs(req.Command) {
		return policyDecision{}, fail("elevation_verb_denied")
	}
	if _, bad := sandbox.CheckSensitiveReferences(req.Command, s.root, s.exec.tempRoot, protected); bad {
		return policyDecision{}, fail("sensitive_path_denied")
	}
	if err := checkEnvProtectedPaths(req.Env, s.root, s.exec.tempRoot, protected); err != nil {
		return policyDecision{}, err
	}
	if err := inspectReferencedScripts(s.root, s.exec.tempRoot, req.Workdir, req.Command, protected); err != nil {
		return policyDecision{}, err
	}

	inv := parseCommandInvocation(req.Command, req.Workdir, s.root, nodeDir)

	s.exec.policyMu.RLock()
	mode := s.exec.execMode
	if mode == "" {
		mode = config.ExecutionModeStandard
	}
	rules := append([]config.TrustRule(nil), s.exec.trustRules...)
	sessionList := append([]config.TrustRule(nil), s.exec.sessionRules[convID]...)
	wsEscalated := s.exec.sessionWorkspaceEscalated[convID]
	s.exec.policyMu.RUnlock()

	extWorkdir, err := validateRequestedWorkdir(s.root, s.exec.tempRoot, req, mode, protected)
	if err != nil {
		return policyDecision{}, err
	}
	dirs, err := deriveExternalDirectories(s.root, s.exec.tempRoot, req, inv, protected)
	if err != nil {
		return policyDecision{}, err
	}
	if len(dirs) > MaxExternalPaths {
		return policyDecision{}, fail("broad_scope_denied")
	}
	checkDirs := dirs
	if extWorkdir != "" && !allDirsWithin([]string{extWorkdir}, dirs) {
		checkDirs = append(append([]string(nil), dirs...), extWorkdir)
	}

	if matchesRestrictiveRules(rules, "deny", checkDirs, inv) {
		return policyDecision{kind: decisionDeny, code: "policy_denied"}, nil
	}

	forcedAsk := matchesRestrictiveRules(rules, "ask", checkDirs, inv)
	combinedAllows := append(append([]config.TrustRule(nil), rules...), sessionList...)
	allAllowed := scopeSatisfiedByAllow(dirs, inv, wsEscalated, rules, sessionList)
	scopeKind, scopeTarget, scopeTargets, canSession, canAlways := deriveScopeMetadata(dirs, inv, combinedAllows)

	if mode == config.ExecutionModeStandard && !req.Escalated && !forcedAsk && len(req.ExternalPaths) == 0 && !(len(dirs) > 0 && allAllowed) {
		return policyDecision{kind: decisionTier1, externalDirs: []string{}}, nil
	}
	if err := checkBroadDirectories(checkDirs, req, s.root, mode); err != nil {
		return policyDecision{}, err
	}
	if req.Escalated && strings.TrimSpace(req.EscalationReason) == "" && (mode == config.ExecutionModeStandard && !allAllowed || forcedAsk) {
		return policyDecision{}, fail("escalation_reason_required")
	}
	if forcedAsk || (mode == config.ExecutionModeStandard && !allAllowed) {
		return policyDecision{
			kind:         decisionAsk,
			scopeKind:    scopeKind,
			scopeTarget:  scopeTarget,
			scopeTargets: scopeTargets,
			reason:       sanitizeApprovalReason(req.EscalationReason, summary, req.Command, scopeKind, scopeTarget),
			allFolders:   append([]string(nil), dirs...),
			externalDirs: ensureNonNilStrings(dirs),
			canSession:   canSession,
			canAlways:    canAlways,
			inv:          inv,
		}, nil
	}
	return buildAllowDecision(mode, convID, scopeKind, scopeTarget, dirs, inv, rules, sessionList, s.exec), nil
}

func validateRequestedWorkdir(wsRoot, tempRoot string, req ExecStartRequest, mode string, protected []sandbox.ProtectedDir) (string, error) {
	wd := strings.TrimSpace(req.Workdir)
	if wd == "" || wd == "." {
		return "", nil
	}
	wsReal := sandbox.ResolveRealishPath(wsRoot)
	if !req.Escalated && mode == config.ExecutionModeStandard && len(req.ExternalPaths) == 0 {
		// Standard non-escalated requests keep external workdirs on the Tier 1 path so
		// callers receive the structured outside_workspace escalation hint.
		return "", nil
	}
	wdReal, err := sandbox.ResolveEscalatedWorkdir(wsRoot, wd, nil, true, protected)
	if err != nil {
		return "", err
	}
	if sandbox.PathWithin(wdReal, wsReal) {
		return "", nil
	}
	if mode == config.ExecutionModeStandard && (req.Escalated || len(req.ExternalPaths) > 0) {
		declared, err := deriveDeclaredExternalPaths(wsRoot, tempRoot, req.Workdir, req.ExternalPaths, protected)
		if err != nil {
			return "", err
		}
		if !allDirsWithin([]string{wdReal}, declared) {
			return "", fail("undeclared_external_workdir")
		}
	}
	return wdReal, nil
}

func buildAllowDecision(mode, convID, scopeKind, scopeTarget string, dirs []string, inv commandInvocation, rules, sessionList []config.TrustRule, m *execManager) policyDecision {
	approvalMode := "session_allow"
	extSpec := ensureNonNilStrings(dirs)
	allowAny := false
	if mode == config.ExecutionModeTurbo {
		approvalMode = "turbo_auto"
		if len(dirs) == 0 {
			extSpec = nil
			allowAny = true
		}
	} else if !scopeCoveredBy(scopeKind, dirs, inv, sessionList) {
		_ = convID
		_ = m
		if scopeCoveredBy(scopeKind, dirs, inv, rules) {
			approvalMode = "rule_allow"
		}
	}
	return policyDecision{
		kind:                 decisionAllowTier2,
		approvalMode:         approvalMode,
		scopeKind:            scopeKind,
		scopeTarget:          scopeTarget,
		allFolders:           append([]string(nil), dirs...),
		externalDirs:         extSpec,
		allowAnyNonProtected: allowAny,
		inv:                  inv,
	}
}

func checkEnvProtectedPaths(env map[string]string, wsRoot, tempRoot string, protected []sandbox.ProtectedDir) error {
	for _, val := range env {
		if val == "" {
			continue
		}
		if _, bad := sandbox.PathTargetsProtected(val, wsRoot, tempRoot, protected); bad {
			return fail("sensitive_path_denied")
		}
		if _, bad := sandbox.CheckSensitiveReferences(val, wsRoot, tempRoot, protected); bad {
			return fail("sensitive_path_denied")
		}
	}
	return nil
}

func normalizeExternalCandidateDir(wsRoot, tempRoot, workdir, raw string, treatAsDir bool, protected []sandbox.ProtectedDir) (string, bool, error) {
	wsReal := sandbox.ResolveRealishPath(wsRoot)
	tmpReal := ""
	if tempRoot != "" {
		tmpReal = sandbox.ResolveRealishPath(tempRoot)
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "." {
		return "", false, nil
	}
	if strings.ContainsAny(trimmed, "\x00\r\n\t") || strings.HasPrefix(trimmed, `\\?\`) || strings.HasPrefix(trimmed, `\\.\`) || strings.HasPrefix(trimmed, `\\`) || strings.HasPrefix(trimmed, "~") {
		return "", false, fail("sensitive_path_denied")
	}
	candidate := trimmed
	if !filepath.IsAbs(candidate) {
		if strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, `\`) {
			return "", false, fail("sensitive_path_denied")
		}
		base := wsReal
		if !treatAsDir && workdir != "" && filepath.IsAbs(workdir) {
			base = workdir
		}
		candidate = filepath.Join(base, candidate)
	}
	resolved := sandbox.ResolveRealishPath(candidate)
	if sandbox.PathWithin(resolved, wsReal) || (tmpReal != "" && sandbox.PathWithin(resolved, tmpReal)) {
		return "", false, nil
	}
	if _, bad := sandbox.PathTargetsProtected(resolved, wsRoot, tempRoot, protected); bad {
		return "", false, fail("sensitive_path_denied")
	}
	dir := nearestExternalDir(resolved, trimmed, treatAsDir)
	if _, bad := sandbox.PathTargetsProtected(dir, wsRoot, tempRoot, protected); bad {
		return "", false, fail("sensitive_path_denied")
	}
	return dir, true, nil
}

func deriveDeclaredExternalPaths(wsRoot, tempRoot, workdir string, externalPaths []string, protected []sandbox.ProtectedDir) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, p := range externalPaths {
		dir, ok, err := normalizeExternalCandidateDir(wsRoot, tempRoot, workdir, p, false, protected)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		key := strings.ToLower(filepath.ToSlash(dir))
		if !seen[key] {
			seen[key] = true
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out, nil
}

func shouldSkipExeCandidate(inv commandInvocation) bool {
	if inv.resolvedExe == "" {
		return false
	}
	if inv.fromPathSearch || isSystemCriticalOrRoot(filepath.Dir(inv.resolvedExe)) {
		return true
	}
	return !inv.compound && !inv.genericHost
}

func deriveExternalDirectories(wsRoot, tempRoot string, req ExecStartRequest, inv commandInvocation, protected []sandbox.ProtectedDir) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	addPath := func(raw string, treatAsDir bool) error {
		dir, ok, err := normalizeExternalCandidateDir(wsRoot, tempRoot, req.Workdir, raw, treatAsDir, protected)
		if err != nil || !ok {
			return err
		}
		key := strings.ToLower(filepath.ToSlash(dir))
		if !seen[key] {
			seen[key] = true
			out = append(out, dir)
		}
		return nil
	}

	declared, err := deriveDeclaredExternalPaths(wsRoot, tempRoot, req.Workdir, req.ExternalPaths, protected)
	if err != nil {
		return nil, err
	}
	for _, dir := range declared {
		key := strings.ToLower(filepath.ToSlash(dir))
		seen[key] = true
		out = append(out, dir)
	}
	if wd := strings.TrimSpace(req.Workdir); wd != "" && wd != "." {
		wdDir, ok, err := normalizeExternalCandidateDir(wsRoot, tempRoot, req.Workdir, wd, true, protected)
		if err != nil {
			return nil, err
		}
		if ok && !allDirsWithin([]string{wdDir}, out) {
			key := strings.ToLower(filepath.ToSlash(wdDir))
			if !seen[key] {
				seen[key] = true
				out = append(out, wdDir)
			}
		}
	}
	if inv.rawTarget != "" && !inv.insideWS && (inv.resolvedExe == "" || !shouldSkipExeCandidate(inv)) {
		if err := addPath(inv.rawTarget, false); err != nil {
			return nil, err
		}
	}
	for _, cand := range sandbox.ExtractExternalPathCandidates(req.Command, wsRoot, tempRoot) {
		if shouldSkipExeCandidate(inv) && pathsEqualOS(cand, inv.resolvedExe) {
			continue
		}
		if err := addPath(cand, false); err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func nearestExternalDir(resolved, raw string, treatAsDir bool) string {
	info, err := os.Lstat(resolved)
	if err == nil {
		if info.IsDir() {
			return resolved
		}
		return filepath.Dir(resolved)
	}
	trailingSep := strings.HasSuffix(raw, "/") || strings.HasSuffix(raw, `\`)
	if treatAsDir || trailingSep || filepath.Ext(filepath.Base(resolved)) == "" {
		return resolved
	}
	return filepath.Dir(resolved)
}

func checkBroadDirectories(dirs []string, req ExecStartRequest, wsRoot, mode string) error {
	if wd := strings.TrimSpace(req.Workdir); wd != "" && req.Escalated {
		if isBroadDirectoryScope(sandbox.ResolveRealishPath(wd)) && !sandbox.PathWithin(sandbox.ResolveRealishPath(wd), sandbox.ResolveRealishPath(wsRoot)) {
			return fail("broad_scope_denied")
		}
	}
	for _, dir := range dirs {
		if !isBroadDirectoryScope(dir) {
			continue
		}
		if mode == config.ExecutionModeTurbo && !isSystemCriticalOrRoot(dir) {
			continue
		}
		return fail("broad_scope_denied")
	}
	return nil
}

func isSystemCriticalOrRoot(dir string) bool {
	clean := filepath.Clean(sandbox.ResolveRealishPath(dir))
	if filepath.Dir(clean) == clean {
		return true
	}
	if runtime.GOOS != "windows" {
		for _, sys := range []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib64", "/proc", "/root", "/sbin", "/sys", "/usr"} {
			if strings.EqualFold(clean, sys) || sandbox.PathWithin(clean, sys) {
				return true
			}
		}
		return false
	}
	for _, envName := range []string{"SystemRoot", "WINDIR", "ProgramFiles", "ProgramFiles(x86)", "ProgramW6432", "ProgramData"} {
		if val := strings.TrimSpace(os.Getenv(envName)); val != "" {
			if sandbox.PathWithin(clean, sandbox.ResolveRealishPath(val)) {
				return true
			}
		}
	}
	return false
}

func isBroadDirectoryScope(dir string) bool {
	if isSystemCriticalOrRoot(dir) {
		return true
	}
	clean := filepath.Clean(sandbox.ResolveRealishPath(dir))
	for _, envName := range []string{"USERPROFILE", "APPDATA", "LOCALAPPDATA", "HOME"} {
		if val := strings.TrimSpace(os.Getenv(envName)); val != "" {
			if strings.EqualFold(clean, filepath.Clean(sandbox.ResolveRealishPath(val))) {
				return true
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if strings.EqualFold(clean, filepath.Clean(sandbox.ResolveRealishPath(home))) {
			return true
		}
	}
	return false
}

func requiresExplicitCommandAllow(inv commandInvocation, combinedAllows []config.TrustRule) bool {
	if inv.compound || inv.genericHost || inv.insideWS || inv.fromPathSearch || inv.resolvedExe == "" {
		return false
	}
	exeDir := filepath.Dir(inv.resolvedExe)
	if isSystemCriticalOrRoot(exeDir) {
		return false
	}
	return !matchesCommandAllow(combinedAllows, inv) && !allDirsCoveredBy([]string{exeDir}, combinedAllows)
}

func deriveScopeMetadata(dirs []string, inv commandInvocation, combinedAllows []config.TrustRule) (kind, target string, targets []string, canSession, canAlways bool) {
	if len(dirs) > 0 && !(allDirsCoveredBy(dirs, combinedAllows) && requiresExplicitCommandAllow(inv, combinedAllows)) {
		anyBroad := false
		for _, d := range dirs {
			if isBroadDirectoryScope(d) {
				anyBroad = true
				break
			}
		}
		copied := append([]string(nil), dirs...)
		return "folder", strings.Join(copied, ", "), copied, true, !anyBroad
	}
	if !inv.compound && !inv.genericHost && inv.toolName != "" && inv.resolvedExe != "" && !inv.insideWS {
		target = inv.resolvedExe
		if inv.fromPathSearch {
			target = inv.toolName
		}
		return "command", target, []string{inv.resolvedExe}, true, true
	}
	if inv.rawTarget != "" && !inv.insideWS && inv.resolvedExe == "" {
		return "workspace_escalated", "", nil, false, false
	}
	return "workspace_escalated", "", nil, true, false
}

func matchesRestrictiveRules(rules []config.TrustRule, action string, dirs []string, inv commandInvocation) bool {
	for _, r := range rules {
		if r.Action != action {
			continue
		}
		if r.Kind == "folder" {
			for _, dir := range dirs {
				if sandbox.PathWithin(dir, r.Pattern) || sandbox.PathWithin(r.Pattern, dir) {
					return true
				}
			}
		}
		if r.Kind == "command" && matchesRestrictiveCommandRule(r.Pattern, inv) {
			return true
		}
	}
	return false
}

func matchesCommandAllow(rules []config.TrustRule, inv commandInvocation) bool {
	if inv.compound || inv.genericHost || inv.toolName == "" || inv.resolvedExe == "" {
		return false
	}
	for _, r := range rules {
		if r.Action == "allow" && r.Kind == "command" && pathsEqualOS(r.Pattern, inv.resolvedExe) {
			return true
		}
	}
	return false
}

func matchesRestrictiveCommandRule(pattern string, inv commandInvocation) bool {
	if inv.resolvedExe != "" && pathsEqualOS(pattern, inv.resolvedExe) {
		return true
	}
	return inv.toolName != "" && strings.EqualFold(strings.TrimSpace(pattern), inv.toolName)
}

func allDirsCoveredBy(dirs []string, rules []config.TrustRule) bool {
	if len(dirs) == 0 {
		return false
	}
	for _, dir := range dirs {
		covered := false
		for _, r := range rules {
			if r.Action == "allow" && r.Kind == "folder" && sandbox.PathWithin(dir, r.Pattern) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func scopeCoveredBy(scopeKind string, dirs []string, inv commandInvocation, rules []config.TrustRule) bool {
	if len(dirs) > 0 {
		return allDirsCoveredBy(dirs, rules) && !requiresExplicitCommandAllow(inv, rules)
	}
	if scopeKind == "command" {
		return matchesCommandAllow(rules, inv)
	}
	return false
}

func scopeSatisfiedByAllow(dirs []string, inv commandInvocation, wsEscalated bool, persistent, session []config.TrustRule) bool {
	combined := append(append([]config.TrustRule(nil), persistent...), session...)
	if len(dirs) > 0 {
		return allDirsCoveredBy(dirs, combined) && !requiresExplicitCommandAllow(inv, combined)
	}
	if !inv.compound && !inv.genericHost && inv.toolName != "" && inv.resolvedExe != "" && !inv.insideWS {
		return matchesCommandAllow(combined, inv)
	}
	if inv.rawTarget != "" && !inv.insideWS && inv.resolvedExe == "" {
		return false
	}
	return wsEscalated
}

func sanitizeApprovalReason(rawReason, summary, command, scopeKind, scopeTarget string) string {
	_ = scopeKind
	clean := collapseControlWhitespace(rawReason)
	if clean == "" {
		clean = collapseControlWhitespace(summary)
	}
	if clean == "" {
		clean = collapseControlWhitespace(redactCommandSummary(command))
	}
	if clean == "" {
		clean = collapseControlWhitespace(scopeTarget)
	}
	return truncateUTF8Bytes(clean, MaxApprovalReasonBytes)
}

func collapseControlWhitespace(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(s), MaxApprovalReasonBytes+8))
	needSpace := false
	for _, r := range s {
		if !utf8.ValidRune(r) {
			continue
		}
		if r == '\r' || r == '\n' || r == '\t' || r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			if b.Len() > 0 {
				needSpace = true
			}
			continue
		}
		if needSpace {
			b.WriteByte(' ')
			needSpace = false
		}
		b.WriteRune(r)
		if b.Len() >= MaxApprovalReasonBytes+8 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func truncateUTF8Bytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := 0
	for i := range s {
		if i > maxBytes {
			break
		}
		cut = i
	}
	return strings.TrimSpace(s[:cut])
}

func ensureNonNilStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	return append([]string(nil), in...)
}
