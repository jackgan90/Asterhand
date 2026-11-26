package workspace

import (
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/platform/sandbox"
)

type commandInvocation struct {
	raw            string
	arg0           string
	argv           []string
	rawArgsTail    string
	toolName       string
	resolvedExe    string
	rawTarget      string
	fromPathSearch bool
	insideWS       bool
	shellBuiltin   bool
	compound       bool
	genericHost    bool
}

func (inv commandInvocation) canDirectExec() bool {
	return !inv.compound && !inv.genericHost && !inv.shellBuiltin && inv.resolvedExe != "" && len(inv.argv) > 0
}

var genericShellOrHostSet = map[string]bool{
	"cmd": true, "powershell": true, "pwsh": true,
	"bash": true, "sh": true, "zsh": true, "ash": true, "dash": true, "fish": true, "wsl": true,
	"python": true, "python3": true, "py": true,
	"node": true, "deno": true, "bun": true,
	"ruby": true, "perl": true, "php": true, "lua": true,
	"mshta": true, "cscript": true, "wscript": true,
	"rundll32": true, "regsvr32": true, "certutil": true, "bitsadmin": true, "msiexec": true, "schtasks": true,
}

var dynamicExecKeywords = map[string]bool{
	"iex": true, "invoke-expression": true, "saps": true, "start-process": true,
}

func parseCommandInvocation(command, workdir, wsRoot, bundledNodeDir string) commandInvocation {
	inv := commandInvocation{raw: command}
	argv, rawTail, directOK := parseDirectPowerShellInvocation(command)
	inv.compound = !directOK
	var arg0 string
	if directOK {
		inv.argv = argv
		inv.rawArgsTail = rawTail
		arg0 = argv[0]
	} else {
		loose := scanLooseCommandTokens(command)
		if len(loose) == 0 {
			return inv
		}
		arg0 = loose[0]
	}
	low0 := strings.ToLower(arg0)
	if arg0 == "" || arg0 == "&" || arg0 == "." || strings.Contains(arg0, "=") || dynamicExecKeywords[low0] {
		inv.compound = true
	}
	inv.arg0 = arg0
	stem := commandToolStem(arg0)
	inv.toolName = stem
	inv.genericHost = genericShellOrHostSet[stem]
	res := sandbox.ResolveEffectiveExecutable(arg0, workdir, wsRoot, bundledNodeDir)
	inv.resolvedExe = res.ResolvedExe
	inv.rawTarget = res.RawTarget
	inv.fromPathSearch = res.FromPathSearch
	inv.insideWS = res.InsideWS
	inv.shellBuiltin = res.ShellBuiltin
	return inv
}

func containsSpecialPSUnicode(command string) bool {
	if !utf8.ValidString(command) {
		return true
	}
	for _, r := range command {
		if r < utf8.RuneSelf {
			if r == 0 || (r < 0x20 && r != '\t') || r == 0x7f {
				return true
			}
			continue
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
		switch r {
		case '\u2013', '\u2014', '\u2015', // Unicode dashes treated as '-' by PowerShell
			'\u2018', '\u2019', '\u201A', '\u201B', // smart single quotes
			'\u201C', '\u201D', '\u201E', '\u201F', // smart double quotes
			'\uFF02', '\uFF07': // fullwidth quotes
			return true
		}
	}
	return false
}

func isPSParamColonPrefix(s string) bool {
	if len(s) < 3 || s[0] != '-' || s[len(s)-1] != ':' {
		return false
	}
	body := s[1 : len(s)-1]
	for i, r := range body {
		if i == 0 {
			if !unicode.IsLetter(r) && r != '_' {
				return false
			}
			continue
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return false
		}
	}
	return len(body) > 0
}

func consumePSSwitchBool(command string, dollarIdx int) (string, int, bool) {
	rest := command[dollarIdx:]
	for _, lit := range []string{"$true", "$false"} {
		if len(rest) >= len(lit) && strings.EqualFold(rest[:len(lit)], lit) {
			end := dollarIdx + len(lit)
			if end == len(command) || command[end] == ' ' || command[end] == '\t' {
				return rest[:len(lit)], end - 1, true
			}
		}
	}
	return "", 0, false
}

func isUnquotedCommandHead(arg0 string) bool {
	if arg0 == "" || arg0 == "." || arg0 == ".." {
		return false
	}
	b0 := arg0[0]
	if (b0 >= '0' && b0 <= '9') || b0 == '+' || b0 == '-' || b0 == '!' {
		return false
	}
	if b0 == '.' && arg0[1] >= '0' && arg0[1] <= '9' {
		return false
	}
	return true
}

func validDirectPowerShellTokens(tokens []string, hasCallOp, headQuoted bool) bool {
	if len(tokens) == 0 {
		return false
	}
	arg0 := tokens[0]
	low0 := strings.ToLower(arg0)
	if arg0 == "" || arg0 == "." || arg0 == ".." || strings.HasPrefix(arg0, "[") || strings.Contains(arg0, "=") || dynamicExecKeywords[low0] {
		return false
	}
	if !hasCallOp && headQuoted {
		return false
	}
	if !headQuoted && !isUnquotedCommandHead(arg0) {
		return false
	}
	for _, tok := range tokens {
		low := strings.ToLower(tok)
		if dynamicExecKeywords[low] || low == "-enc" || low == "-encodedcommand" || tok == "--%" {
			return false
		}
	}
	return true
}

func scanLooseCommandTokens(command string) []string {
	var tokens []string
	var cur strings.Builder
	var quote byte
	inTok := false
	flush := func() {
		if inTok {
			tokens = append(tokens, cur.String())
			cur.Reset()
			inTok = false
		}
	}
	for i := 0; i < len(command); i++ {
		b := command[i]
		if quote != 0 {
			if quote == '\'' && b == '\'' && i+1 < len(command) && command[i+1] == '\'' {
				cur.WriteByte('\'')
				i++
				continue
			}
			if quote == '"' && (b == '\\' || b == '`' || b == '"') && i+1 < len(command) && command[i+1] == '"' {
				cur.WriteByte('"')
				i++
				continue
			}
			if b == quote {
				quote = 0
				continue
			}
			cur.WriteByte(b)
			continue
		}
		switch b {
		case '"', '\'':
			quote = b
			inTok = true
		case ' ', '\t', '\r', '\n', ';', '|', '&', '>', '<', '(', ')', '{', '}':
			flush()
		default:
			cur.WriteByte(b)
			inTok = true
		}
	}
	flush()
	return tokens
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
