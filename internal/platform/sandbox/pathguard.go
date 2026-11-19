package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// DefaultProtectedDirs collects symbolic labels and normalized host paths for
// sensitive user credential directories and Asterhand's private home directory.
func DefaultProtectedDirs(homeRoot string) []ProtectedDir {
	var out []ProtectedDir
	add := func(label, p string) {
		if strings.TrimSpace(p) == "" {
			return
		}
		abs, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return
		}
		out = append(out, ProtectedDir{Label: label, Path: resolveRealish(abs)})
	}
	if homeRoot != "" {
		add("asterhand-home", homeRoot)
	}
	userHome, _ := os.UserHomeDir()
	appData := os.Getenv("APPDATA")
	localAppData := os.Getenv("LOCALAPPDATA")
	if userHome != "" {
		for _, sub := range []struct{ label, rel string }{
			{"~/.ssh", ".ssh"},
			{"~/.aws", ".aws"},
			{"~/.gnupg", ".gnupg"},
			{"~/.kube", ".kube"},
			{"~/.docker", ".docker"},
			{"~/.npmrc", ".npmrc"},
			{"~/.pypirc", ".pypirc"},
			{"~/.netrc", ".netrc"},
			{"~/.git-credentials", ".git-credentials"},
			{"~/.vault-token", ".vault-token"},
			{"~/.config/gh", filepath.Join(".config", "gh")},
			{"~/.config/gcloud", filepath.Join(".config", "gcloud")},
			{"~/Library/Keychains", filepath.Join("Library", "Keychains")},
			{"~/.local/share/keyrings", filepath.Join(".local", "share", "keyrings")},
		} {
			add(sub.label, filepath.Join(userHome, sub.rel))
		}
	}
	if appData != "" {
		add("%APPDATA%/GitHub CLI", filepath.Join(appData, "GitHub CLI"))
		add("%APPDATA%/gcloud", filepath.Join(appData, "gcloud"))
	}
	if localAppData != "" {
		add("%LOCALAPPDATA%/gcloud", filepath.Join(localAppData, "gcloud"))
	}
	return out
}

// ResolveWorkdir validates and resolves a requested script working directory, ensuring
// it stays strictly inside workspaceRoot without traversing symlinks/junctions outside.
func ResolveWorkdir(workspaceRoot, rawWorkdir string) (string, error) {
	wsReal, err := canonicalExistingDir(workspaceRoot)
	if err != nil {
		return "", fail("invalid_path")
	}
	trimmed := strings.TrimSpace(rawWorkdir)
	if trimmed == "" || trimmed == "." {
		return wsReal, nil
	}
	if strings.ContainsAny(trimmed, "\x00\r\n\t") || strings.HasPrefix(trimmed, `\\?\`) || strings.HasPrefix(trimmed, `\\.\`) || strings.HasPrefix(trimmed, `\\`) {
		return "", fail("sensitive_path_denied")
	}
	if strings.HasPrefix(trimmed, "~") {
		return "", fail("sensitive_path_denied")
	}
	candidate := trimmed
	if !filepath.IsAbs(candidate) {
		if strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, `\`) {
			return "", fail("sensitive_path_denied")
		}
		candidate = filepath.Join(wsReal, candidate)
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fail("not_found")
		}
		return "", fail("invalid_path")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fail("sensitive_path_denied")
	}
	realTarget, err := canonicalExistingDir(candidate)
	if err != nil {
		return "", fail("sensitive_path_denied")
	}
	if !pathWithin(realTarget, wsReal) {
		return "", fail("sensitive_path_denied")
	}
	return realTarget, nil
}

var (
	reWinEnvVar = regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`)
	rePSEnvVar  = regexp.MustCompile(`(?i)\$env:([A-Za-z_][A-Za-z0-9_]*)`)
	reUnixVar   = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
)

type normProtected struct {
	label string
	norm  string
	lower string
}

func prenormalizeProtected(protected []ProtectedDir) []normProtected {
	out := make([]normProtected, 0, len(protected))
	for _, p := range protected {
		n := normalizeForCompare(p.Path)
		if n == "" {
			continue
		}
		out = append(out, normProtected{label: p.Label, norm: n, lower: strings.ToLower(n)})
	}
	return out
}

func sampleVariants(text string) []string {
	expanded := expandKnownPathTokens(text)
	if expanded == text {
		return []string{text}
	}
	return []string{text, expanded}
}

// CheckSensitiveReferences inspects a command string or env value for literal,
// tilde/env-expanded, file://, or short-name path references targeting protected
// directories outside workspaceRoot and tempRoot.
func CheckSensitiveReferences(text, workspaceRoot, tempRoot string, protected []ProtectedDir) (string, bool) {
	if strings.TrimSpace(text) == "" || len(protected) == 0 {
		return "", false
	}
	norms := prenormalizeProtected(protected)
	if len(norms) == 0 {
		return "", false
	}
	wsNorm := normalizeForCompare(resolveRealish(workspaceRoot))
	tmpNorm := normalizeForCompare(resolveRealish(tempRoot))
	wsLower := strings.ToLower(wsNorm)
	tmpLower := strings.ToLower(tmpNorm)

	for _, sample := range sampleVariants(text) {
		// Check full normalized string for literal/env-expanded prefixes of protected paths,
		// skipping spans that actually point inside allowed workspaceRoot or tempRoot.
		sampleSlash := strings.ToLower(strings.ReplaceAll(sample, `\`, "/"))
		for _, p := range norms {
			if containsProtectedOutsideAllowed(sampleSlash, p.lower, wsLower, tmpLower) {
				return p.label, true
			}
		}
		// Inspect tokenized path candidates for ~/, short 8.3 names, or symlinks.
		for _, tok := range extractPathTokens(sample, wsNorm, tmpNorm) {
			if label, bad := checkTokenCandidate(tok, wsNorm, tmpNorm, norms); bad {
				return label, true
			}
		}
	}
	return "", false
}

func checkTokenCandidate(tok, wsNorm, tmpNorm string, protected []normProtected) (string, bool) {
	abs, norm, ok := normalizeTokenPath(tok)
	if !ok {
		return "", false
	}
	if pathWithin(norm, wsNorm) || (tmpNorm != "" && pathWithin(norm, tmpNorm)) {
		return "", false
	}
	absNorm := normalizeForCompare(abs)
	for _, p := range protected {
		if pathWithin(norm, p.norm) || pathWithin(absNorm, p.norm) {
			return p.label, true
		}
	}
	return "", false
}

func normalizeTokenPath(tok string) (string, string, bool) {
	if len(tok) > 4096 {
		return "", "", false
	}
	tok = trimTokenEnclosure(tok)
	if tok == "" {
		return "", "", false
	}
	tok = strings.TrimPrefix(tok, "file:///")
	tok = strings.TrimPrefix(tok, "file://")
	if strings.HasPrefix(tok, "~/") || strings.HasPrefix(tok, `~\`) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			tok = filepath.Join(home, tok[2:])
		}
	}
	if !looksLikeHostPath(tok) {
		return "", "", false
	}
	abs, err := filepath.Abs(filepath.Clean(tok))
	if err != nil {
		return "", "", false
	}
	return abs, normalizeForCompare(resolveRealish(abs)), true
}

func tokenResolvesWithinAllowed(tok, wsNorm, tmpNorm string) bool {
	_, norm, ok := normalizeTokenPath(tok)
	if !ok {
		return false
	}
	return pathWithin(norm, wsNorm) || (tmpNorm != "" && pathWithin(norm, tmpNorm))
}

func trimTokenEnclosure(tok string) string {
	pairs := [][2]byte{{'(', ')'}, {'[', ']'}, {'{', '}'}}
	for {
		prev := tok
		tok = strings.TrimSpace(strings.Trim(tok, `"'`+"`"+`,;`))
		for _, pair := range pairs {
			tok = stripMatchingOrUnmatched(tok, pair[0], pair[1])
		}
		if tok == prev {
			return tok
		}
	}
}

func stripMatchingOrUnmatched(tok string, open, close byte) string {
	n := len(tok)
	if n == 0 {
		return tok
	}
	if n >= 2 && tok[0] == open && tok[n-1] == close && isOuterEnclosed(tok, open, close) {
		return tok[1 : n-1]
	}
	opens := strings.Count(tok, string(open))
	closes := strings.Count(tok, string(close))
	if tok[0] == open && opens > closes {
		return tok[1:]
	}
	if tok[n-1] == close && closes > opens {
		return tok[:n-1]
	}
	return tok
}

func isOuterEnclosed(tok string, open, close byte) bool {
	depth := 0
	for i := 0; i < len(tok); i++ {
		switch tok[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 && i < len(tok)-1 {
				return false
			}
		}
	}
	return depth == 0
}

func looksLikeHostPath(tok string) bool {
	if len(tok) >= 3 && tok[1] == ':' && (tok[2] == '\\' || tok[2] == '/') {
		return true
	}
	if strings.HasPrefix(tok, `\\`) {
		return true
	}
	if !strings.HasPrefix(tok, "/") {
		return false
	}
	if runtime.GOOS != "windows" || tok == "/" || strings.ContainsAny(tok[1:], `/\`) {
		return true
	}
	if strings.ContainsAny(tok[1:], ":?*") {
		return false
	}
	abs, err := filepath.Abs(filepath.Clean(tok))
	if err != nil {
		return false
	}
	_, statErr := os.Lstat(abs)
	return statErr == nil
}

func expandKnownPathTokens(s string) string {
	if !strings.ContainsAny(s, "%$") {
		return s
	}
	home, _ := os.UserHomeDir()
	lookup := func(name string) (string, bool) {
		switch strings.ToUpper(name) {
		case "USERPROFILE", "HOME":
			if home != "" {
				return home, true
			}
		case "APPDATA", "LOCALAPPDATA":
			if v := os.Getenv(strings.ToUpper(name)); v != "" {
				return v, true
			}
		}
		return "", false
	}
	s = reWinEnvVar.ReplaceAllStringFunc(s, func(m string) string {
		if v, ok := lookup(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
	s = rePSEnvVar.ReplaceAllStringFunc(s, func(m string) string {
		if idx := strings.IndexByte(m, ':'); idx >= 0 {
			if v, ok := lookup(m[idx+1:]); ok {
				return v
			}
		}
		return m
	})
	s = reUnixVar.ReplaceAllStringFunc(s, func(m string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(m, "$"), "{"), "}")
		if v, ok := lookup(name); ok {
			return v
		}
		return m
	})
	return s
}

type quotedSpan struct {
	start int
	text  string
}

func collectQuotedSpans(s string) []quotedSpan {
	var spans []quotedSpan
	for _, q := range []byte{'"', '\''} {
		for i := 0; i < len(s); i++ {
			if s[i] != q || (q == '"' && i > 0 && (s[i-1] == '\\' || s[i-1] == '`')) {
				continue
			}
			for j := i + 1; j < len(s); j++ {
				if isStatementBreak(s[j]) {
					i = j
					break
				}
				if s[j] == q {
					spans = append(spans, quotedSpan{start: i + 1, text: s[i+1 : j]})
					i = j
					break
				}
			}
		}
	}
	for i := 1; i < len(s); i++ {
		if s[i] != '"' || (s[i-1] != '\\' && s[i-1] != '`') {
			continue
		}
		esc := s[i-1]
		for j := i + 1; j < len(s); j++ {
			if isStatementBreak(s[j]) {
				i = j
				break
			}
			if s[j] == '"' && s[j-1] == esc {
				spans = append(spans, quotedSpan{start: i + 1, text: s[i+1 : j-1]})
				i = j
				break
			}
		}
	}
	return spans
}

func isStatementBreak(b byte) bool {
	return b == '\r' || b == '\n' || b == ';' || b == '&' || b == '|'
}

func isTokenDelim(b byte) bool {
	return b == ' ' || b == '\t' || b == '"' || b == '\'' || b == '`' || b == '=' || isStatementBreak(b)
}

func unquotedAllowedSpanCandidates(s string, start int, wsNorm, tmpNorm string) ([]string, bool) {
	maxPref := max(len(wsNorm), len(tmpNorm))
	if maxPref == 0 {
		return nil, false
	}
	trimStart := start
	for trimStart < len(s) && (s[trimStart] == '(' || s[trimStart] == '[' || s[trimStart] == '{') {
		trimStart++
	}
	rest := s[trimStart:]
	offset := 0
	if len(rest) >= 7 {
		if strings.HasPrefix(strings.ToLower(rest[:min(len(rest), 8)]), "file:///") {
			offset = len("file:///")
		} else if strings.HasPrefix(strings.ToLower(rest[:7]), "file://") {
			offset = len("file://")
		}
	}
	tail := rest[offset:]
	headEnd := min(len(tail), maxPref+2)
	headNorm := strings.ReplaceAll(tail[:headEnd], `\`, "/")
	if runtime.GOOS == "windows" {
		headNorm = strings.ToLower(headNorm)
	}
	prefixLen := allowedPrefixMatchLen(headNorm, wsNorm, tmpNorm)
	if prefixLen == 0 {
		return nil, false
	}
	segEnd := trimStart + offset + prefixLen
	for segEnd < len(s) && !isStatementBreak(s[segEnd]) && s[segEnd] != '"' && s[segEnd] != '\'' && s[segEnd] != '`' && s[segEnd] != '=' {
		segEnd++
	}
	var cands []string
	minEnd := trimStart + offset + prefixLen
	for pos := minEnd; pos < segEnd; pos++ {
		switch s[pos] {
		case ' ', '\t', ',', ')', ']', '}':
			cands = append(cands, s[trimStart:pos])
		}
	}
	cands = append(cands, s[trimStart:segEnd])
	return cands, true
}

func extractPathTokens(s, wsNorm, tmpNorm string) []string {
	var tokens []string
	seen := make(map[string]bool)
	addTok := func(tok string) {
		if tok != "" && !seen[tok] {
			seen[tok] = true
			tokens = append(tokens, tok)
		}
	}
	safeQuoteStarts := make(map[int]bool)
	for _, qs := range collectQuotedSpans(s) {
		addTok(qs.text)
		if tokenResolvesWithinAllowed(qs.text, wsNorm, tmpNorm) {
			safeQuoteStarts[qs.start] = true
		}
	}
	for i := 0; i < len(s); {
		for i < len(s) && isTokenDelim(s[i]) {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		for i < len(s) && !isTokenDelim(s[i]) {
			i++
		}
		if safeQuoteStarts[start] {
			continue
		}
		if cands, ok := unquotedAllowedSpanCandidates(s, start, wsNorm, tmpNorm); ok {
			for _, c := range cands {
				addTok(c)
			}
			continue
		}
		addTok(s[start:i])
	}
	return tokens
}

func containsProtectedOutsideAllowed(haystackSlash, needleNorm, wsNorm, tmpNorm string) bool {
	idx := 0
	for {
		pos := strings.Index(haystackSlash[idx:], needleNorm)
		if pos < 0 {
			return false
		}
		absPos := idx + pos
		endPos := absPos + len(needleNorm)
		beforeOK := absPos == 0 || !isIdentChar(haystackSlash[absPos-1])
		afterOK := endPos == len(haystackSlash) || haystackSlash[endPos] == '/' || !isIdentChar(haystackSlash[endPos])
		if beforeOK && afterOK {
			span := extractPathSpanAt(haystackSlash, absPos, len(needleNorm), wsNorm, tmpNorm)
			if (wsNorm == "" || !pathWithin(span, wsNorm)) && (tmpNorm == "" || !pathWithin(span, tmpNorm)) {
				return true
			}
		}
		idx = absPos + 1
	}
}

func detectEnclosingQuote(haystackSlash string, absPos int) byte {
	qPos := absPos
	if strings.HasSuffix(haystackSlash[:qPos], "file:///") {
		qPos -= len("file:///")
	} else if strings.HasSuffix(haystackSlash[:qPos], "file://") {
		qPos -= len("file://")
	}
	if qPos > 0 {
		ch := haystackSlash[qPos-1]
		if ch == '"' || ch == '\'' || ch == '`' {
			return ch
		}
	}
	return 0
}

func allowedPrefixMatchLen(rest, wsNorm, tmpNorm string) int {
	best := 0
	for _, prefix := range []string{wsNorm, tmpNorm} {
		if prefix == "" || len(rest) < len(prefix) || !strings.HasPrefix(rest, prefix) {
			continue
		}
		if len(rest) == len(prefix) {
			if len(prefix) > best {
				best = len(prefix)
			}
			continue
		}
		next := rest[len(prefix)]
		if next == '/' || next == ' ' || next == '\t' || isUnquotedSpanDelimiter(next) {
			if len(prefix) > best {
				best = len(prefix)
			}
		}
	}
	return best
}

func isUnquotedSpanDelimiter(b byte) bool {
	switch b {
	case '"', '\'', '`', '\r', '\n', ';', '&', '|', '=', ',', '(', ')', '[', ']', '{', '}', '\t', ' ':
		return true
	default:
		return false
	}
}

func extractPathSpanAt(haystackSlash string, absPos, needleLen int, wsNorm, tmpNorm string) string {
	rest := haystackSlash[absPos:]
	if q := detectEnclosingQuote(haystackSlash, absPos); q != 0 {
		for i := needleLen; i < len(rest); i++ {
			b := rest[i]
			if b == q || isStatementBreak(b) || (q == '"' && b == '`') {
				return strings.TrimRight(rest[:i], "/")
			}
		}
		return strings.TrimRight(rest, "/")
	}
	start := needleLen
	if n := allowedPrefixMatchLen(rest, wsNorm, tmpNorm); n > start {
		start = n
	}
	for i := start; i < len(rest); i++ {
		if isUnquotedSpanDelimiter(rest[i]) {
			return strings.TrimRight(rest[:i], "/")
		}
	}
	return strings.TrimRight(rest, "/")
}

func isIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-' || b == '.'
}

func canonicalExistingDir(p string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	realPath, err := evalRealPath(abs)
	if err != nil {
		return "", err
	}
	return stripExtendedPrefix(filepath.Clean(realPath)), nil
}

func resolveRealish(p string) string {
	return resolveRealishDepth(p, 0)
}

func resolveRealishDepth(p string, depth int) string {
	if p == "" {
		return ""
	}
	clean := stripExtendedPrefix(filepath.Clean(p))
	if depth > 16 {
		return clean
	}
	if realPath, err := evalRealPath(clean); err == nil {
		return stripExtendedPrefix(filepath.Clean(realPath))
	}
	// Walk upward to resolve existing ancestor (e.g. 8.3 short name, junction, or symlink + missing leaf).
	var suffix []string
	cur := clean
	for {
		if target, err := os.Readlink(cur); err == nil && target != "" {
			target = stripExtendedPrefix(target)
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(cur), target)
			}
			base := resolveRealishDepth(target, depth+1)
			parts := append([]string{base}, suffix...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		if realParent, err := evalRealPath(parent); err == nil {
			parts := append([]string{stripExtendedPrefix(filepath.Clean(realParent))}, suffix...)
			return filepath.Join(parts...)
		}
		cur = parent
	}
	return clean
}

func normalizeForCompare(p string) string {
	slash := filepath.ToSlash(stripExtendedPrefix(filepath.Clean(p)))
	slash = strings.TrimRight(slash, "/")
	if runtime.GOOS == "windows" {
		return strings.ToLower(slash)
	}
	return slash
}

func pathWithin(child, parent string) bool {
	c := normalizeForCompare(child)
	p := normalizeForCompare(parent)
	if c == "" || p == "" {
		return false
	}
	return c == p || strings.HasPrefix(c, p+"/")
}

var (
	reVerbRunAs      = regexp.MustCompile(`(?i)(?:^|[\s;|&({])-verb\s+['"]?runas\b`)
	rePropVerbRunAs  = regexp.MustCompile(`(?i)\.verb\s*=\s*['"]runas['"]`)
	reShellExecRunAs = regexp.MustCompile(`(?i)shellexecute\w*\s*\([^)]*['"]runas['"]`)
)

// CheckElevationVerbs rejects commands that attempt to request OS UAC elevation
// via ShellExecute RunAs or runas/sudo launchers, which would either block on the
// secure desktop or break away from the non-elevated Job Object.
func CheckElevationVerbs(command string) bool {
	if strings.TrimSpace(command) == "" {
		return false
	}
	if reVerbRunAs.MatchString(command) || rePropVerbRunAs.MatchString(command) || reShellExecRunAs.MatchString(command) {
		return true
	}
	return hasUnquotedElevationCommand(command)
}

func hasUnquotedElevationCommand(command string) bool {
	var quote byte
	atCmdStart := true
	for i := 0; i < len(command); {
		b := command[i]
		if quote != 0 {
			if b == quote && (quote != '"' || i == 0 || (command[i-1] != '\\' && command[i-1] != '`')) {
				quote = 0
			}
			i++
			continue
		}
		if b == '"' || b == '\'' {
			quote = b
			atCmdStart = false
			i++
			continue
		}
		if b == ' ' || b == '\t' || b == '@' || b == '&' && i+1 < len(command) && command[i+1] == ' ' {
			i++
			continue
		}
		if isStatementBreak(b) || b == '(' || b == '{' || b == '`' {
			atCmdStart = true
			i++
			continue
		}
		if b == ')' || b == '}' || b == '=' {
			atCmdStart = false
			i++
			continue
		}
		start := i
		for i < len(command) && !isTokenDelim(command[i]) && command[i] != '(' && command[i] != '{' && command[i] != ')' && command[i] != '}' {
			i++
		}
		if i == start {
			i++
			continue
		}
		if atCmdStart {
			base := strings.ToLower(filepath.Base(strings.Trim(command[start:i], `"'`)))
			switch base {
			case "runas", "runas.exe", "sudo", "sudo.exe", "gsudo", "gsudo.exe":
				return true
			}
		}
		atCmdStart = false
	}
	return false
}

// CanonicalExistingPath resolves an existing path through symlinks, junctions, and short names.
func CanonicalExistingPath(p string) (string, error) {
	return canonicalExistingDir(p)
}

// ResolveRealishPath canonicalizes an existing or future path via its deepest existing ancestor.
func ResolveRealishPath(p string) string {
	return resolveRealish(p)
}

// PathWithin reports whether child equals or lies strictly underneath parent.
func PathWithin(child, parent string) bool {
	return pathWithin(child, parent)
}

// PathTargetsProtected reports whether candidate lies inside a protected directory outside workspace/temp.
func PathTargetsProtected(candidate, workspaceRoot, tempRoot string, protected []ProtectedDir) (string, bool) {
	norm := normalizeForCompare(resolveRealish(candidate))
	wsNorm := normalizeForCompare(resolveRealish(workspaceRoot))
	tmpNorm := normalizeForCompare(resolveRealish(tempRoot))
	if norm == "" || pathWithin(norm, wsNorm) || (tmpNorm != "" && pathWithin(norm, tmpNorm)) {
		return "", false
	}
	for _, p := range protected {
		pNorm := normalizeForCompare(p.Path)
		if pNorm != "" && pathWithin(norm, pNorm) {
			return p.Label, true
		}
	}
	return "", false
}

// ExtractExternalPathCandidates extracts resolved host paths from text that fall outside workspaceRoot and tempRoot.
func ExtractExternalPathCandidates(text, workspaceRoot, tempRoot string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	wsNorm := normalizeForCompare(resolveRealish(workspaceRoot))
	tmpNorm := normalizeForCompare(resolveRealish(tempRoot))
	seen := map[string]bool{}
	var out []string
	for _, sample := range sampleVariants(text) {
		for _, tok := range extractPathTokens(sample, wsNorm, tmpNorm) {
			_, norm, ok := normalizeTokenPath(tok)
			if !ok || pathWithin(norm, wsNorm) || (tmpNorm != "" && pathWithin(norm, tmpNorm)) {
				continue
			}
			resolved := resolveRealish(trimTokenPathTail(tok))
			key := normalizeForCompare(resolved)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, resolved)
		}
	}
	return out
}

func trimTokenPathTail(tok string) string {
	t := trimTokenEnclosure(tok)
	t = strings.TrimPrefix(t, "file:///")
	t = strings.TrimPrefix(t, "file://")
	if strings.HasPrefix(t, "~/") || strings.HasPrefix(t, `~\`) {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			t = filepath.Join(home, t[2:])
		}
	}
	return stripExtendedPrefix(filepath.Clean(t))
}

// ResolveEscalatedWorkdir resolves either a workspace-relative workdir or an existing
// non-protected external directory permitted by allowedExternalDirs (or any non-protected
// directory when allowAnyNonProtected is true).
func ResolveEscalatedWorkdir(workspaceRoot, rawWorkdir string, allowedExternalDirs []string, allowAnyNonProtected bool, protected []ProtectedDir) (string, error) {
	wsReal, err := canonicalExistingDir(workspaceRoot)
	if err != nil {
		return "", fail("invalid_path")
	}
	trimmed := strings.TrimSpace(rawWorkdir)
	if trimmed == "" || trimmed == "." {
		return wsReal, nil
	}
	if strings.ContainsAny(trimmed, "\x00\r\n\t") || strings.HasPrefix(trimmed, `\\?\`) || strings.HasPrefix(trimmed, `\\.\`) || strings.HasPrefix(trimmed, `\\`) || strings.HasPrefix(trimmed, "~") {
		return "", fail("sensitive_path_denied")
	}
	candidate := trimmed
	isRelative := !filepath.IsAbs(candidate)
	if isRelative {
		if strings.HasPrefix(candidate, "/") || strings.HasPrefix(candidate, `\`) {
			return "", fail("sensitive_path_denied")
		}
		candidate = filepath.Join(wsReal, candidate)
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fail("not_found")
		}
		return "", fail("invalid_path")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fail("sensitive_path_denied")
	}
	realTarget, err := canonicalExistingDir(candidate)
	if err != nil {
		return "", fail("sensitive_path_denied")
	}
	if pathWithin(realTarget, wsReal) {
		return realTarget, nil
	}
	if isRelative {
		return "", fail("sensitive_path_denied")
	}
	if _, bad := PathTargetsProtected(realTarget, workspaceRoot, "", protected); bad {
		return "", fail("sensitive_path_denied")
	}
	if allowAnyNonProtected {
		return realTarget, nil
	}
	for _, allowed := range allowedExternalDirs {
		if pathWithin(realTarget, allowed) {
			return realTarget, nil
		}
	}
	return "", fail("sensitive_path_denied")
}
