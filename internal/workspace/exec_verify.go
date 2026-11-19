package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/platform/sandbox"
)

type ticketSnapshot struct {
	workdir      string
	externalDirs []string
	exePath      string
	exeSize      int64
	exeModNano   int64
	exeSHA256    string
	scriptHashes map[string]string
	absentPaths  map[string]bool
}

type codeCandidate struct {
	path       string
	exists     bool
	isScript   bool
	isExe      bool
	isExecuted bool
}

type openedCodeFile struct {
	file    *os.File
	canon   string
	size    int64
	modNano int64
	sha256  string
}

func isVerifiableScriptExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".ps1", ".psm1", ".psd1", ".bat", ".cmd", ".py", ".pyw",
		".mjs", ".cjs", ".js", ".ts", ".mts", ".cts",
		".sh", ".bash", ".zsh", ".rb", ".pl", ".php", ".lua",
		".vbs", ".vbe", ".jse", ".wsf", ".wsh", ".hta":
		return true
	default:
		return false
	}
}

func isVerifiableExeExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".exe", ".com", ".msi", ".msp", ".scr", ".cpl", ".dll":
		return true
	default:
		return false
	}
}

func resolveEffectiveWorkdirPath(wsRoot, rawWorkdir string) string {
	wsReal := sandbox.ResolveRealishPath(wsRoot)
	wd := strings.TrimSpace(rawWorkdir)
	if wd == "" || wd == "." {
		return wsReal
	}
	if !filepath.IsAbs(wd) {
		wd = filepath.Join(wsReal, wd)
	}
	if canon, err := sandbox.CanonicalExistingPath(wd); err == nil {
		return canon
	}
	return sandbox.ResolveRealishPath(wd)
}

func collectCommandTokenStrings(command string) []string {
	tokens := scanLooseCommandTokens(command)
	out := make([]string, 0, len(tokens))
	seen := map[string]bool{}
	add := func(s string) {
		t := strings.TrimSpace(s)
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, tok := range tokens {
		add(tok)
		if idx := strings.IndexByte(tok, '='); idx > 0 && idx+1 < len(tok) {
			add(tok[idx+1:])
		}
		if strings.ContainsAny(tok, " \t'\"|&;") {
			for _, st := range scanLooseCommandTokens(tok) {
				add(st)
				if idx := strings.IndexByte(st, '='); idx > 0 && idx+1 < len(st) {
					add(st[idx+1:])
				}
			}
		}
	}
	return out
}

func hasInvalidDriveColon(t string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	idx := strings.IndexByte(t, ':')
	if idx < 0 {
		return false
	}
	if idx == 1 && ((t[0] >= 'a' && t[0] <= 'z') || (t[0] >= 'A' && t[0] <= 'Z')) {
		if len(t) >= 3 && (t[2] == '\\' || t[2] == '/') && !strings.ContainsRune(t[2:], ':') {
			return false
		}
	}
	return true
}

func normalizeCandidatePathToken(tok string) (string, bool) {
	t := strings.TrimSpace(strings.Trim(tok, `"'`))
	t = strings.TrimPrefix(t, "file:///")
	t = strings.TrimPrefix(t, "file://")
	if t == "" || strings.HasPrefix(t, "-") || strings.Contains(t, "://") {
		return "", false
	}
	if strings.ContainsAny(t, "\x00\r\n*?<>|") || hasInvalidDriveColon(t) {
		return "", false
	}
	return t, true
}

func splitStatementSegments(command string) []string {
	var segs []string
	var quote byte
	start := 0
	for i := 0; i < len(command); i++ {
		b := command[i]
		if quote != 0 {
			if quote == '\'' && b == '\'' && i+1 < len(command) && command[i+1] == '\'' {
				i++
				continue
			}
			if b == quote {
				quote = 0
			}
			continue
		}
		switch b {
		case '"', '\'':
			quote = b
		case '\r', '\n', ';', '|':
			if s := strings.TrimSpace(command[start:i]); s != "" {
				segs = append(segs, s)
			}
			start = i + 1
		}
	}
	if s := strings.TrimSpace(command[start:]); s != "" {
		segs = append(segs, s)
	}
	return segs
}

func executedTokensInStatement(tokens []string, depth int) []string {
	for len(tokens) > 0 && (tokens[0] == "&" || tokens[0] == ".") {
		tokens = tokens[1:]
	}
	if len(tokens) == 0 {
		return nil
	}
	head, rest := tokens[0], tokens[1:]
	var out []string
	ext := filepath.Ext(head)
	if strings.ContainsAny(head, `/\`) || isVerifiableScriptExt(ext) || isVerifiableExeExt(ext) {
		out = append(out, head)
	}
	switch commandToolStem(head) {
	case "powershell", "pwsh":
		return append(out, psExecutedArgs(rest, depth)...)
	case "cmd":
		return append(out, cmdExecutedArgs(rest, depth)...)
	case "python", "python3", "py":
		return append(out, pythonExecutedArgs(rest)...)
	case "node", "deno", "bun":
		return append(out, nodeExecutedArgs(rest)...)
	case "bash", "sh", "zsh", "ash", "dash":
		return append(out, shellExecutedArgs(rest, depth)...)
	case "ruby", "perl", "php", "lua", "cscript", "wscript", "mshta":
		return append(out, genericScriptExecutedArgs(rest)...)
	default:
		return out
	}
}

func psExecutedArgs(rest []string, depth int) []string {
	for i := 0; i < len(rest); i++ {
		low := strings.ToLower(rest[i])
		switch low {
		case "-f", "-file":
			if i+1 < len(rest) {
				return []string{rest[i+1]}
			}
			return nil
		case "-c", "-command":
			if i+1 < len(rest) {
				return collectExecutedRawTokens(rest[i+1], depth+1)
			}
			return nil
		case "-enc", "-encodedcommand":
			return nil
		case "-ep", "-executionpolicy", "-if", "-inputformat", "-of", "-outputformat",
			"-v", "-version", "-wd", "-workingdirectory", "-w", "-windowstyle", "-configurationname":
			i++
		default:
			if strings.HasPrefix(low, "-") {
				continue
			}
			return []string{rest[i]}
		}
	}
	return nil
}

func cmdExecutedArgs(rest []string, depth int) []string {
	for i := 0; i < len(rest); i++ {
		low := strings.ToLower(rest[i])
		if (low == "/c" || low == "/k") && i+1 < len(rest) {
			out := []string{rest[i+1]}
			return append(out, collectExecutedRawTokens(rest[i+1], depth+1)...)
		}
		if strings.HasPrefix(low, "/") || strings.HasPrefix(low, "-") {
			continue
		}
		return []string{rest[i]}
	}
	return nil
}

func shellExecutedArgs(rest []string, depth int) []string {
	for i := 0; i < len(rest); i++ {
		low := strings.ToLower(rest[i])
		if low == "-c" {
			if i+1 < len(rest) {
				return collectExecutedRawTokens(rest[i+1], depth+1)
			}
			return nil
		}
		if strings.HasPrefix(low, "-") {
			continue
		}
		return []string{rest[i]}
	}
	return nil
}

func pythonExecutedArgs(rest []string) []string {
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		if tok == "-c" || tok == "-m" {
			return nil
		}
		if tok == "-X" || tok == "-W" || tok == "--check-hash-based-pycs" {
			i++
			continue
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		return []string{tok}
	}
	return nil
}

func nodeExecutedArgs(rest []string) []string {
	var out []string
	for i := 0; i < len(rest); i++ {
		tok := rest[i]
		low := strings.ToLower(tok)
		if low == "-e" || low == "--eval" || low == "-p" || low == "--print" || low == "-c" {
			return out
		}
		if low == "-r" || low == "--require" || low == "--import" {
			if i+1 < len(rest) {
				out = append(out, rest[i+1])
				i++
			}
			continue
		}
		if low == "--loader" || low == "--conditions" || low == "-c" {
			i++
			continue
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		return append(out, tok)
	}
	return out
}

func genericScriptExecutedArgs(rest []string) []string {
	for _, tok := range rest {
		low := strings.ToLower(tok)
		if low == "-e" || low == "-c" || low == "-r" {
			return nil
		}
		if strings.HasPrefix(tok, "-") {
			continue
		}
		return []string{tok}
	}
	return nil
}

func collectExecutedRawTokens(command string, depth int) []string {
	if depth > 3 || strings.TrimSpace(command) == "" {
		return nil
	}
	var out []string
	for _, seg := range splitStatementSegments(command) {
		tokens := scanLooseCommandTokens(seg)
		out = append(out, executedTokensInStatement(tokens, depth)...)
	}
	return out
}

func extractExecutedTargetKeys(wdReal, command string) map[string]bool {
	keys := map[string]bool{}
	for _, raw := range collectExecutedRawTokens(command, 0) {
		cand, ok := normalizeCandidatePathToken(raw)
		if !ok {
			continue
		}
		full := cand
		if !filepath.IsAbs(full) {
			full = filepath.Join(wdReal, full)
		}
		clean := filepath.Clean(full)
		realPath := sandbox.ResolveRealishPath(clean)
		keys[strings.ToLower(filepath.ToSlash(clean))] = true
		keys[strings.ToLower(filepath.ToSlash(realPath))] = true
	}
	return keys
}

func buildExecSpawnSpec(wsRoot, tempRoot, homeRoot, nodeDir, sessionID string, req ExecStartRequest, externalDirs []string, escalated bool, timeout time.Duration, verifiedInv commandInvocation) sandbox.SpawnSpec {
	jobName := ""
	if escalated {
		jobName = `Local\Asterhand.Exec.` + sessionID
	}
	spec := sandbox.SpawnSpec{
		WorkspaceRoot:  wsRoot,
		TempRoot:       tempRoot,
		HomeRoot:       homeRoot,
		Command:        req.Command,
		Workdir:        req.Workdir,
		Env:            req.Env,
		BundledNodeDir: nodeDir,
		Timeout:        timeout,
		ProtectedDirs:  sandbox.DefaultProtectedDirs(homeRoot),
		Escalated:      escalated,
		ExternalDirs:   externalDirs,
		JobName:        jobName,
	}
	if verifiedInv.canDirectExec() {
		spec.DirectExe = verifiedInv.resolvedExe
		spec.DirectArgs = append([]string(nil), verifiedInv.argv[1:]...)
		spec.DirectRawArgs = verifiedInv.rawArgsTail
	}
	return spec
}

func extractVerifiableCodeCandidates(wsRoot, workdir, command string) ([]codeCandidate, error) {
	wsReal := sandbox.ResolveRealishPath(wsRoot)
	wdReal := resolveEffectiveWorkdirPath(wsRoot, workdir)
	execKeys := extractExecutedTargetKeys(wdReal, command)
	byKey := map[string]int{}
	var out []codeCandidate

	for _, raw := range collectCommandTokenStrings(command) {
		cand, ok := normalizeCandidatePathToken(raw)
		if !ok {
			continue
		}
		full := cand
		if !filepath.IsAbs(full) {
			full = filepath.Join(wdReal, full)
		}
		clean := filepath.Clean(full)
		cleanKey := strings.ToLower(filepath.ToSlash(clean))
		cleanExt := filepath.Ext(clean)
		if !execKeys[cleanKey] && !isVerifiableScriptExt(cleanExt) && !isVerifiableExeExt(cleanExt) {
			continue
		}
		lexInside := sandbox.PathWithin(clean, wsReal)
		realPath := sandbox.ResolveRealishPath(clean)
		realInside := sandbox.PathWithin(realPath, wsReal)
		if lexInside && !realInside {
			return nil, fail("unsafe_links_detected")
		}
		if linfo, lerr := os.Lstat(clean); lerr == nil && linfo.Mode()&os.ModeSymlink != 0 {
			return nil, fail("unsafe_links_detected")
		}

		realKey := strings.ToLower(filepath.ToSlash(realPath))
		isExecuted := execKeys[cleanKey] || execKeys[realKey]
		ext := filepath.Ext(realPath)
		isExe := isVerifiableExeExt(ext)
		isScript := isVerifiableScriptExt(ext) || (isExecuted && !isExe)
		if !isScript && !isExe {
			continue
		}

		st, statErr := os.Lstat(realPath)
		exists := false
		if statErr != nil {
			if !os.IsNotExist(statErr) {
				continue
			}
			if !realInside && !isExecuted {
				continue
			}
			if strings.ContainsAny(cand, " \t'\"=;|&<>()*?{}`") {
				continue
			}
		} else {
			if st.Mode()&os.ModeSymlink != 0 {
				return nil, fail("unsafe_links_detected")
			}
			if !st.Mode().IsRegular() {
				continue
			}
			exists = true
		}

		if idx, seen := byKey[realKey]; seen {
			out[idx].isExecuted = out[idx].isExecuted || isExecuted
			out[idx].isScript = out[idx].isScript || isScript
			continue
		}
		byKey[realKey] = len(out)
		out = append(out, codeCandidate{
			path:       realPath,
			exists:     exists,
			isScript:   isScript,
			isExe:      isExe,
			isExecuted: isExecuted,
		})
	}
	return out, nil
}

func openVerifyAndHashCodeFile(path string, isScript bool, insideWS bool) (*openedCodeFile, []byte, error) {
	f, canon, size, modNano, err := sandbox.OpenSharedReadLocked(path)
	if err != nil {
		return nil, nil, err
	}
	maxBytes := int64(MaxVerifiableExeBytes)
	overErr := "approval_tampered"
	if isScript {
		maxBytes = MaxVerifiableScriptBytes
		overErr = "script_unverifiable"
	} else if !insideWS && size > maxBytes {
		_ = f.Close()
		return &openedCodeFile{canon: canon, size: size, modNano: modNano}, nil, nil
	}
	if size > maxBytes {
		_ = f.Close()
		return nil, nil, fail(overErr)
	}
	if isScript {
		data, rErr := io.ReadAll(io.LimitReader(f, maxBytes+1))
		if rErr != nil {
			_ = f.Close()
			return nil, nil, fail("io_error")
		}
		if int64(len(data)) > maxBytes {
			_ = f.Close()
			return nil, nil, fail(overErr)
		}
		sum := sha256.Sum256(data)
		return &openedCodeFile{
			file: f, canon: canon, size: int64(len(data)), modNano: modNano, sha256: hex.EncodeToString(sum[:]),
		}, data, nil
	}
	h := sha256.New()
	n, cErr := io.Copy(h, io.LimitReader(f, maxBytes+1))
	if cErr != nil {
		_ = f.Close()
		return nil, nil, fail("io_error")
	}
	if n > maxBytes {
		_ = f.Close()
		return nil, nil, fail(overErr)
	}
	return &openedCodeFile{
		file: f, canon: canon, size: n, modNano: modNano, sha256: hex.EncodeToString(h.Sum(nil)),
	}, nil, nil
}

func inspectReferencedScripts(wsRoot, tempRoot, workdir, command string, protected []sandbox.ProtectedDir) error {
	cands, err := extractVerifiableCodeCandidates(wsRoot, workdir, command)
	if err != nil {
		return err
	}
	for _, c := range cands {
		if !c.exists || !c.isScript {
			continue
		}
		opened, data, err := openVerifyAndHashCodeFile(c.path, true, sandbox.PathWithin(c.path, wsRoot))
		if opened != nil && opened.file != nil {
			_ = opened.file.Close()
		}
		if err != nil {
			return err
		}
		text := string(data)
		if sandbox.CheckElevationVerbs(text) {
			return fail("elevation_verb_denied")
		}
		if _, bad := sandbox.CheckSensitiveReferences(text, wsRoot, tempRoot, protected); bad {
			return fail("sensitive_path_denied")
		}
	}
	return nil
}

func captureTicketSnapshot(wsRoot string, req ExecStartRequest, extDirs []string, inv commandInvocation) (ticketSnapshot, error) {
	snap := ticketSnapshot{
		workdir:      resolveEffectiveWorkdirPath(wsRoot, req.Workdir),
		externalDirs: canonicalSortedDirs(extDirs),
		scriptHashes: map[string]string{},
		absentPaths:  map[string]bool{},
	}
	if inv.resolvedExe != "" {
		opened, _, err := openVerifyAndHashCodeFile(inv.resolvedExe, false, inv.insideWS)
		if opened != nil && opened.file != nil {
			_ = opened.file.Close()
		}
		if err != nil {
			return ticketSnapshot{}, fail("approval_tampered")
		}
		snap.exePath = opened.canon
		snap.exeSize = opened.size
		snap.exeModNano = opened.modNano
		snap.exeSHA256 = opened.sha256
	}
	cands, err := extractVerifiableCodeCandidates(wsRoot, snap.workdir, req.Command)
	if err != nil {
		return ticketSnapshot{}, err
	}
	for _, c := range cands {
		if !c.exists {
			snap.absentPaths[filepath.Clean(c.path)] = true
			continue
		}
		opened, _, err := openVerifyAndHashCodeFile(c.path, c.isScript, sandbox.PathWithin(c.path, wsRoot))
		if opened != nil && opened.file != nil {
			_ = opened.file.Close()
		}
		if err != nil {
			return ticketSnapshot{}, err
		}
		if opened != nil && opened.sha256 != "" {
			snap.scriptHashes[opened.canon] = opened.sha256
		}
	}
	return snap, nil
}

func canonicalSortedDirs(dirs []string) []string {
	out := make([]string, len(dirs))
	for i, d := range dirs {
		if canon, err := sandbox.CanonicalExistingPath(d); err == nil {
			out[i] = canon
		} else {
			out[i] = sandbox.ResolveRealishPath(d)
		}
	}
	sort.Strings(out)
	return out
}

func closeLockedHandles(closers []io.Closer) {
	for _, c := range closers {
		if c != nil {
			_ = c.Close()
		}
	}
}

func (s *Service) verifyAndLockPreSpawn(req ExecStartRequest, extDirs []string, escalated bool, expectedExe string, tk *approvalTicket, skipStaticCmdScan bool, homeRoot, nodeDir string) (commandInvocation, []io.Closer, error) {
	protected := sandbox.DefaultProtectedDirs(homeRoot)
	var locked []io.Closer
	failCode := func(err error) error {
		closeLockedHandles(locked)
		if tk != nil {
			return fail("approval_tampered")
		}
		return err
	}
	if !skipStaticCmdScan {
		if sandbox.CheckElevationVerbs(req.Command) {
			return commandInvocation{}, nil, failCode(fail("elevation_verb_denied"))
		}
		if _, bad := sandbox.CheckSensitiveReferences(req.Command, s.root, s.exec.tempRoot, protected); bad {
			return commandInvocation{}, nil, failCode(fail("sensitive_path_denied"))
		}
		if err := checkEnvProtectedPaths(req.Env, s.root, s.exec.tempRoot, protected); err != nil {
			return commandInvocation{}, nil, failCode(err)
		}
	}

	nowInv := parseCommandInvocation(req.Command, req.Workdir, s.root, nodeDir)
	wdReal := resolveEffectiveWorkdirPath(s.root, req.Workdir)
	curHashes, exeMeta, candLocks, err := s.verifyAndLockCandidates(req.Command, wdReal, nowInv.resolvedExe, tk != nil, skipStaticCmdScan, protected)
	locked = append(locked, candLocks...)
	if err != nil {
		return commandInvocation{}, nil, failCode(err)
	}
	exeMeta, exeLock, err := s.lockResolvedExeIfNeeded(nowInv, exeMeta)
	if exeLock != nil {
		locked = append(locked, exeLock)
	}
	if err != nil {
		return commandInvocation{}, nil, failCode(err)
	}

	if tk != nil {
		if err := s.verifyTicketSnapshotAgainstCurrent(tk, nowInv, wdReal, exeMeta, curHashes, protected); err != nil {
			closeLockedHandles(locked)
			return commandInvocation{}, nil, err
		}
	}
	if escalated {
		if err := s.verifyEscalatedSpawnTarget(req, extDirs, expectedExe, nowInv, protected); err != nil {
			closeLockedHandles(locked)
			return commandInvocation{}, nil, err
		}
	}
	return nowInv, locked, nil
}

func (s *Service) verifyAndLockCandidates(command, wdReal, resolvedExe string, fromTicket, skipStaticCmdScan bool, protected []sandbox.ProtectedDir) (map[string]string, *openedCodeFile, []io.Closer, error) {
	cands, err := extractVerifiableCodeCandidates(s.root, wdReal, command)
	if err != nil {
		return nil, nil, nil, err
	}
	curHashes := map[string]string{}
	var exeMeta *openedCodeFile
	var locked []io.Closer
	failAndClose := func(err error) (map[string]string, *openedCodeFile, []io.Closer, error) {
		closeLockedHandles(locked)
		return nil, nil, nil, err
	}
	for _, c := range cands {
		if !c.exists {
			continue
		}
		opened, data, err := openVerifyAndHashCodeFile(c.path, c.isScript, sandbox.PathWithin(c.path, s.root))
		if err != nil {
			if opened != nil && opened.file != nil {
				_ = opened.file.Close()
			}
			return failAndClose(err)
		}
		if opened != nil && opened.sha256 != "" {
			curHashes[opened.canon] = opened.sha256
		}
		if c.isScript && len(data) > 0 && (!skipStaticCmdScan || c.isExecuted || fromTicket) {
			text := string(data)
			if sandbox.CheckElevationVerbs(text) {
				if opened.file != nil {
					_ = opened.file.Close()
				}
				return failAndClose(fail("elevation_verb_denied"))
			}
			if _, bad := sandbox.CheckSensitiveReferences(text, s.root, s.exec.tempRoot, protected); bad {
				if opened.file != nil {
					_ = opened.file.Close()
				}
				return failAndClose(fail("sensitive_path_denied"))
			}
		}
		isExeMatch := resolvedExe != "" && opened != nil && pathsEqualOS(opened.canon, resolvedExe)
		shouldHoldLock := opened != nil && opened.file != nil && !isSystemCriticalOrRoot(filepath.Dir(opened.canon)) && (c.isExecuted || isExeMatch)
		if isExeMatch {
			exeMeta = opened
		}
		if shouldHoldLock {
			locked = append(locked, opened.file)
		} else if opened != nil && opened.file != nil {
			_ = opened.file.Close()
		}
	}
	return curHashes, exeMeta, locked, nil
}

func (s *Service) lockResolvedExeIfNeeded(nowInv commandInvocation, exeMeta *openedCodeFile) (*openedCodeFile, io.Closer, error) {
	if nowInv.resolvedExe == "" || exeMeta != nil {
		return exeMeta, nil, nil
	}
	openedExe, _, err := openVerifyAndHashCodeFile(nowInv.resolvedExe, false, nowInv.insideWS)
	if err != nil {
		if openedExe != nil && openedExe.file != nil {
			_ = openedExe.file.Close()
		}
		return nil, nil, err
	}
	if openedExe != nil && openedExe.file != nil && (nowInv.insideWS || !isSystemCriticalOrRoot(filepath.Dir(openedExe.canon))) {
		return openedExe, openedExe.file, nil
	}
	if openedExe != nil && openedExe.file != nil {
		_ = openedExe.file.Close()
	}
	return openedExe, nil, nil
}

func (s *Service) verifyEscalatedSpawnTarget(req ExecStartRequest, extDirs []string, expectedExe string, nowInv commandInvocation, protected []sandbox.ProtectedDir) error {
	if expectedExe != "" && !pathsEqualOS(expectedExe, nowInv.resolvedExe) {
		return fail("approval_tampered")
	}
	nowDirs, err := deriveExternalDirectories(s.root, s.exec.tempRoot, req, nowInv, protected)
	if err != nil {
		return err
	}
	if extDirs != nil && !allDirsWithin(nowDirs, extDirs) {
		return fail("approval_tampered")
	}
	_, err = sandbox.ResolveEscalatedWorkdir(s.root, req.Workdir, extDirs, extDirs == nil, protected)
	return err
}

func (s *Service) verifyTicketSnapshotAgainstCurrent(tk *approvalTicket, nowInv commandInvocation, curWorkdir string, exeMeta *openedCodeFile, curHashes map[string]string, protected []sandbox.ProtectedDir) error {
	nowDirs, err := deriveExternalDirectories(s.root, s.exec.tempRoot, tk.req, nowInv, protected)
	if err != nil {
		return fail("approval_tampered")
	}
	curDirs := canonicalSortedDirs(nowDirs)
	expected := tk.snapshot
	if !pathsEqualOS(expected.workdir, curWorkdir) || len(expected.externalDirs) != len(curDirs) {
		return fail("approval_tampered")
	}
	for i := range expected.externalDirs {
		if !pathsEqualOS(expected.externalDirs[i], curDirs[i]) {
			return fail("approval_tampered")
		}
	}
	if expected.exePath != "" {
		if exeMeta == nil || !pathsEqualOS(expected.exePath, exeMeta.canon) ||
			expected.exeSize != exeMeta.size || expected.exeModNano != exeMeta.modNano || expected.exeSHA256 != exeMeta.sha256 {
			return fail("approval_tampered")
		}
	} else if nowInv.resolvedExe != "" {
		return fail("approval_tampered")
	}
	if len(expected.scriptHashes) != len(curHashes) {
		return fail("approval_tampered")
	}
	for path, wantHash := range expected.scriptHashes {
		if curHashes[path] != wantHash {
			return fail("approval_tampered")
		}
	}
	for path := range expected.absentPaths {
		if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
			return fail("approval_tampered")
		}
	}
	return nil
}

func allDirsWithin(dirs, allowed []string) bool {
	for _, d := range dirs {
		covered := false
		for _, a := range allowed {
			if sandbox.PathWithin(d, a) {
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

func pathsEqualOS(a, b string) bool {
	if a == "" && b == "" {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	ca := filepath.Clean(a)
	cb := filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

func parseDirectPowerShellInvocation(command string) ([]string, string, bool) {
	if containsSpecialPSUnicode(command) {
		return nil, "", false
	}
	var tokens []string
	var cur strings.Builder
	var quote byte
	var inTok, sawQuote, leadQuoted, justClosed, hasCallOp, headQuoted bool
	arg0End := -1
	flush := func(endIdx int) bool {
		if !inTok {
			return true
		}
		tok := cur.String()
		if (sawQuote && tok == "") || strings.ContainsRune(tok, '"') || (strings.HasSuffix(tok, `\`) && strings.ContainsAny(tok, " \t")) {
			return false
		}
		if len(tokens) == 0 {
			arg0End = endIdx
			headQuoted = leadQuoted
		}
		tokens = append(tokens, tok)
		cur.Reset()
		inTok, sawQuote, leadQuoted, justClosed = false, false, false, false
		return true
	}
	for i := 0; i < len(command); i++ {
		b := command[i]
		if quote != 0 {
			var closed, ok bool
			if i, quote, closed, ok = stepPSQuotedChar(command, i, quote, &cur); !ok {
				return nil, "", false
			}
			justClosed = closed
			continue
		}
		if justClosed && b != ' ' && b != '\t' {
			return nil, "", false
		}
		justClosed = false
		switch b {
		case '&':
			if len(tokens) == 0 && !inTok && !hasCallOp {
				hasCallOp = true
				continue
			}
			return nil, "", false
		case '"', '\'':
			if !inTok {
				if len(tokens) == 0 && !hasCallOp {
					return nil, "", false
				}
				leadQuoted = true
			} else if len(tokens) == 0 || (!isPSParamColonPrefix(cur.String()) && !strings.Contains(cur.String(), "=")) {
				return nil, "", false
			}
			quote, inTok, sawQuote = b, true, true
		case '$':
			if len(tokens) > 0 && (!inTok || (!leadQuoted && isPSParamColonPrefix(cur.String()))) {
				if sw, nextIdx, ok := consumePSSwitchBool(command, i); ok {
					cur.WriteString(sw)
					inTok = true
					i = nextIdx
					continue
				}
			}
			return nil, "", false
		case ';', '|', '`', '>', '<', '(', ')', '{', '}', '[', ']', '@', '#', ',':
			return nil, "", false
		case ' ', '\t':
			if !flush(i) {
				return nil, "", false
			}
		default:
			if b < 0x20 || b == 0x7f {
				return nil, "", false
			}
			cur.WriteByte(b)
			inTok = true
		}
	}
	if quote != 0 || !flush(len(command)) || !validDirectPowerShellTokens(tokens, hasCallOp, headQuoted) {
		return nil, "", false
	}
	if len(tokens) > 1 && arg0End >= 0 && arg0End < len(command) {
		return tokens, command[arg0End:], true
	}
	return tokens, "", true
}

func stepPSQuotedChar(command string, i int, quote byte, cur *strings.Builder) (int, byte, bool, bool) {
	b := command[i]
	if quote == '\'' {
		if b == '\'' {
			if i+1 < len(command) && command[i+1] == '\'' {
				cur.WriteByte('\'')
				return i + 1, '\'', false, true
			}
			return i, 0, true, true
		}
		cur.WriteByte(b)
		return i, '\'', false, true
	}
	// PowerShell double-quoted strings do not treat '\' as an escape character.
	if b == '`' || b == '$' {
		return i, quote, false, false
	}
	if b == '"' {
		if i+1 < len(command) && command[i+1] == '"' {
			return i, quote, false, false
		}
		return i, 0, true, true
	}
	cur.WriteByte(b)
	return i, '"', false, true
}
