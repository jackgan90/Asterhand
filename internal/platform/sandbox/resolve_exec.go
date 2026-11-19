package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExecResolution captures how arg0 resolves in the child process environment.
type ExecResolution struct {
	ResolvedExe    string
	RawTarget      string
	FromPathSearch bool
	InsideWS       bool
	ShellBuiltin   bool
}

var powershellBuiltinAliases = map[string]bool{
	"ac": true, "asnp": true, "cat": true, "cd": true, "chdir": true, "clc": true,
	"clear": true, "clhy": true, "cli": true, "clp": true, "cls": true, "clv": true,
	"cnsn": true, "compare": true, "copy": true, "cp": true, "cpi": true, "cpp": true,
	"curl": true, "cvpa": true, "dbp": true, "del": true, "diff": true, "dir": true,
	"dnsn": true, "ebp": true, "echo": true, "epal": true, "epcsv": true, "epsn": true,
	"erase": true, "etsn": true, "exsn": true, "fc": true, "fhx": true, "fl": true,
	"foreach": true, "ft": true, "fw": true, "gal": true, "gbp": true, "gc": true,
	"gcb": true, "gci": true, "gcm": true, "gcs": true, "gdr": true, "ghy": true,
	"gi": true, "gjb": true, "gl": true, "gm": true, "gmo": true, "gp": true,
	"gps": true, "gpv": true, "group": true, "gsn": true, "gsnp": true, "gsv": true,
	"gtz": true, "gu": true, "gv": true, "gwmi": true, "h": true, "help": true,
	"history": true, "icm": true, "iex": true, "ihy": true, "ii": true, "ipal": true,
	"ipcsv": true, "ipmo": true, "ipsn": true, "irm": true, "ise": true, "iwmi": true,
	"iwr": true, "kill": true, "lp": true, "ls": true, "man": true, "md": true,
	"measure": true, "mi": true, "mount": true, "move": true, "mp": true, "mv": true,
	"nal": true, "ndr": true, "ni": true, "nmo": true, "npssc": true, "nsn": true,
	"nv": true, "ogv": true, "oh": true, "popd": true, "ps": true, "pushd": true,
	"pwd": true, "r": true, "rbp": true, "rcjb": true, "rcsn": true, "rd": true,
	"rdr": true, "ren": true, "ri": true, "rjb": true, "rm": true, "rmdir": true,
	"rmo": true, "rni": true, "rnp": true, "rp": true, "rsn": true, "rsnp": true,
	"rujb": true, "rv": true, "rvpa": true, "rwmi": true, "sajb": true, "sal": true,
	"saps": true, "sasv": true, "sbp": true, "sc": true, "scb": true, "select": true,
	"set": true, "shcm": true, "si": true, "sl": true, "sleep": true, "sls": true,
	"sort": true, "sp": true, "spjb": true, "spps": true, "spsv": true, "start": true,
	"stz": true, "sujb": true, "sv": true, "swmi": true, "tee": true, "trcm": true,
	"type": true, "wget": true, "where": true, "wjb": true, "write": true,
}

var powershellCmdletPrefixes = []string{
	"add-", "clear-", "close-", "compare-", "compress-", "connect-", "convertfrom-",
	"convertto-", "copy-", "disable-", "disconnect-", "enable-", "enter-", "exit-",
	"expand-", "export-", "find-", "foreach-", "format-", "get-", "group-", "import-",
	"invoke-", "join-", "limit-", "measure-", "merge-", "move-", "new-", "open-",
	"out-", "pop-", "push-", "read-", "register-", "remove-", "rename-", "reset-",
	"resolve-", "restart-", "resume-", "save-", "select-", "send-", "set-", "show-",
	"skip-", "sort-", "split-", "start-", "stop-", "suspend-", "switch-", "tee-",
	"test-", "trace-", "undo-", "unblock-", "unregister-", "update-", "use-", "wait-",
	"watch-", "where-", "write-",
}

func isPowerShellBuiltinInvocation(arg0 string) bool {
	if runtime.GOOS != "windows" || arg0 == "" || strings.ContainsAny(arg0, `/\`) {
		return false
	}
	if filepath.Ext(arg0) != "" {
		return false
	}
	low := strings.ToLower(strings.TrimSpace(arg0))
	if powershellBuiltinAliases[low] {
		return true
	}
	for _, prefix := range powershellCmdletPrefixes {
		if strings.HasPrefix(low, strings.ToLower(prefix)) {
			return true
		}
	}
	return false
}

// ResolveEffectiveExecutable resolves arg0 using the child process's effective
// PATH/PATHEXT and working directory rules, preventing host-LookPath drift and
// built-in PowerShell alias shadowing.
func ResolveEffectiveExecutable(arg0, workdir, workspaceRoot, bundledNodeDir string) ExecResolution {
	trimmed := strings.TrimSpace(arg0)
	if trimmed == "" {
		return ExecResolution{}
	}
	wsReal := ResolveRealishPath(workspaceRoot)
	pathVal, pathExt := EffectiveSearchPath(bundledNodeDir)
	exts := effectiveExecExtensions(pathExt)

	if strings.ContainsAny(trimmed, `/\`) {
		cand := trimmed
		if !filepath.IsAbs(cand) {
			base := wsReal
			if wd := strings.TrimSpace(workdir); wd != "" && wd != "." {
				if filepath.IsAbs(wd) {
					base = ResolveRealishPath(wd)
				} else {
					base = ResolveRealishPath(filepath.Join(wsReal, wd))
				}
			}
			cand = filepath.Join(base, cand)
		}
		rawReal := ResolveRealishPath(cand)
		match, ok := findExecutableCandidate(cand, exts)
		if !ok {
			return ExecResolution{RawTarget: rawReal}
		}
		canon, err := CanonicalExistingPath(match)
		if err != nil {
			canon = ResolveRealishPath(match)
		}
		return ExecResolution{
			ResolvedExe:    canon,
			RawTarget:      rawReal,
			FromPathSearch: false,
			InsideWS:       PathWithin(canon, wsReal),
		}
	}

	if isPowerShellBuiltinInvocation(trimmed) {
		return ExecResolution{ShellBuiltin: true}
	}

	for _, dir := range filepath.SplitList(pathVal) {
		dir = strings.TrimSpace(dir)
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		cand := filepath.Join(dir, trimmed)
		if match, ok := findExecutableCandidate(cand, exts); ok {
			canon, err := CanonicalExistingPath(match)
			if err != nil {
				continue
			}
			return ExecResolution{
				ResolvedExe:    canon,
				RawTarget:      canon,
				FromPathSearch: true,
				InsideWS:       PathWithin(canon, wsReal),
			}
		}
	}
	return ExecResolution{FromPathSearch: true}
}

func effectiveExecExtensions(pathExt string) []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	raw := strings.Split(pathExt, ";")
	if strings.TrimSpace(pathExt) == "" {
		raw = []string{".COM", ".EXE", ".BAT", ".CMD"}
	}
	seen := map[string]bool{}
	var out []string
	add := func(ext string) {
		e := strings.TrimSpace(ext)
		if e == "" || !strings.HasPrefix(e, ".") {
			return
		}
		low := strings.ToLower(e)
		if !seen[low] {
			seen[low] = true
			out = append(out, e)
		}
	}
	for _, item := range raw {
		add(item)
	}
	add(".PS1")
	return out
}

func findExecutableCandidate(basePath string, exts []string) (string, bool) {
	if runtime.GOOS != "windows" {
		info, err := os.Stat(basePath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return "", false
		}
		return basePath, true
	}
	if filepath.Ext(basePath) != "" {
		info, err := os.Stat(basePath)
		if err == nil && info.Mode().IsRegular() {
			return basePath, true
		}
		return "", false
	}
	for _, ext := range exts {
		cand := basePath + ext
		info, err := os.Stat(cand)
		if err == nil && info.Mode().IsRegular() {
			return cand, true
		}
	}
	return "", false
}
