package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

var allowedInheritedEnvKeys = map[string]bool{
	// Core OS, paths, locale, and Windows runtime fundamentals
	"ALLUSERSPROFILE": true, "APPDATA": true, "COLORTERM": true,
	"COMMONPROGRAMFILES": true, "COMMONPROGRAMFILES(X86)": true, "COMMONPROGRAMW6432": true,
	"COMPUTERNAME": true, "COMSPEC": true, "DRIVERDATA": true,
	"HOME": true, "HOMEDRIVE": true, "HOMEPATH": true, "HOSTNAME": true,
	"LANG": true, "LANGUAGE": true, "LC_ALL": true, "LC_COLLATE": true,
	"LC_CTYPE": true, "LC_MESSAGES": true, "LC_MONETARY": true, "LC_NUMERIC": true,
	"LC_TIME": true, "LOCALAPPDATA": true, "LOGNAME": true,
	"NUMBER_OF_PROCESSORS": true, "OS": true, "PATH": true, "PATHEXT": true,
	"POWERSHELL_DISTRIBUTION_CHANNEL": true, "PROCESSOR_ARCHITECTURE": true,
	"PROCESSOR_ARCHITEW6432": true, "PROCESSOR_IDENTIFIER": true,
	"PROCESSOR_LEVEL": true, "PROCESSOR_REVISION": true,
	"PROGRAMDATA": true, "PROGRAMFILES": true, "PROGRAMFILES(X86)": true, "PROGRAMW6432": true,
	"PSMODULEPATH": true, "PUBLIC": true, "SHELL": true, "SYSTEMDRIVE": true,
	"SYSTEMROOT": true, "TEMP": true, "TMP": true, "TMPDIR": true, "TZ": true,
	"USER": true, "USERDOMAIN": true, "USERDOMAIN_ROAMINGPROFILE": true,
	"USERNAME": true, "USERPROFILE": true, "WINDIR": true,
	"XDG_CONFIG_HOME": true, "XDG_DATA_DIRS": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true,

	// Non-secret SDK / compiler / package manager locations & flags
	"ANDROID_HOME": true, "ANDROID_SDK_ROOT": true, "BUN_INSTALL": true, "CARGO_HOME": true,
	"CC": true, "CGO_ENABLED": true, "CHOCOLATEYINSTALL": true, "CMAKE_GENERATOR": true,
	"CONDA_DEFAULT_ENV": true, "CONDA_EXE": true, "CONDA_PREFIX": true, "CONDA_PYTHON_EXE": true,
	"COREPACK_HOME": true, "CXX": true, "DOTNET_CLI_TELEMETRY_OPTOUT": true, "DOTNET_ROOT": true,
	"FNM_DIR": true, "FNM_MULTISHELL_PATH": true, "GOENV": true, "GOMODCACHE": true,
	"GONOPROXY": true, "GONOSUMDB": true, "GOPATH": true, "GOPRIVATE": true, "GOPROXY": true,
	"GOROOT": true, "GOSUMDB": true, "GOTOOLCHAIN": true, "GRADLE_USER_HOME": true,
	"JAVA_HOME": true, "JDK_HOME": true, "JRE_HOME": true, "M2_HOME": true, "MAVEN_HOME": true,
	"NUGET_PACKAGES": true, "NVM_DIR": true, "NVM_HOME": true, "NVM_SYMLINK": true,
	"PIPX_BIN_DIR": true, "PIPX_HOME": true, "PNPM_HOME": true, "POETRY_HOME": true,
	"PYENV": true, "PYENV_HOME": true, "PYENV_ROOT": true,
	"RUSTUP_DIST_SERVER": true, "RUSTUP_HOME": true, "RUSTUP_UPDATE_ROOT": true,
	"UV_PYTHON_INSTALL_DIR": true, "UV_TOOL_DIR": true, "VCINSTALLDIR": true,
	"VCPKG_ROOT": true, "VIRTUAL_ENV": true, "VOLTA_HOME": true, "VSINSTALLDIR": true,
	"WINDOWSSDKDIR": true, "WINDOWSSDKVERSION": true, "YARN_CACHE_FOLDER": true,

	// TLS CA bundles & proxy endpoints (credentials inside URLs are scrubbed separately)
	"ALL_PROXY": true, "CURL_CA_BUNDLE": true, "HTTPS_PROXY": true, "HTTP_PROXY": true,
	"NODE_EXTRA_CA_CERTS": true, "NO_PROXY": true, "PIP_CERT": true,
	"REQUESTS_CA_BUNDLE": true, "SSL_CERT_DIR": true, "SSL_CERT_FILE": true,
}

var dangerousEnvKeys = map[string]bool{
	"PATH": true, "PATHEXT": true, "COMSPEC": true, "SYSTEMROOT": true,
	"WINDIR": true, "OS": true, "USERPROFILE": true, "HOME": true,
	"APPDATA": true, "LOCALAPPDATA": true, "TEMP": true, "TMP": true, "TMPDIR": true,
	"NODE_OPTIONS": true, "PYTHONSTARTUP": true, "PYTHONPATH": true, "PYTHONHOME": true,
	"PYTHONEXECUTABLE": true, "DOTNET_STARTUP_HOOKS": true, "GIT_SSH": true,
	"GIT_SSH_COMMAND": true, "GIT_EXEC_PATH": true, "GIT_ASKPASS": true,
	"SSH_ASKPASS": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true,
	"DYLD_INSERT_LIBRARIES": true, "DYLD_LIBRARY_PATH": true,
}

var redirectedTempSubdirs = map[string]string{
	"TEMP":                "",
	"TMP":                 "",
	"TMPDIR":              "",
	"PIP_CACHE_DIR":       "pip-cache",
	"UV_CACHE_DIR":        "uv-cache",
	"NPM_CONFIG_CACHE":    "npm-cache",
	"XDG_CACHE_HOME":      "xdg-cache",
	"GOCACHE":             "go-cache",
	"PYTHONPYCACHEPREFIX": "pycache",
}

// EffectiveSearchPath returns the PATH and PATHEXT values exposed to sandboxed
// and escalated child processes so pre-launch resolution matches runtime lookup.
func EffectiveSearchPath(bundledNodeDir string) (pathVal, pathExt string) {
	envMap := map[string]string{}
	origKey := map[string]string{}
	for _, entry := range os.Environ() {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || k == "" || !shouldInheritEnvEntry(k, v) {
			continue
		}
		upper := strings.ToUpper(k)
		if upper == "PATH" || upper == "PATHEXT" {
			envMap[upper] = v
			origKey[upper] = k
		}
	}
	applyPathPrepends(envMap, origKey, bundledNodeDir)
	return envMap["PATH"], envMap["PATHEXT"]
}

// BuildEnvironment creates a scrubbed environment for sandboxed scripts, rejecting
// dangerous overrides or sensitive path references upfront.
func BuildEnvironment(spec SpawnSpec) ([]string, error) {
	return buildFilteredEnvironment(spec, true)
}

// BuildEscalatedEnvironment builds an environment for Tier 2 non-elevated runs:
// host secrets and dangerous overrides remain stripped, while the user's native
// temp/cache directories and system TLS defaults stay intact.
func BuildEscalatedEnvironment(spec SpawnSpec) ([]string, error) {
	return buildFilteredEnvironment(spec, false)
}

func buildFilteredEnvironment(spec SpawnSpec, isolateTempAndSSL bool) ([]string, error) {
	if len(spec.Env) > 64 {
		return nil, fail("invalid_request")
	}
	for k, v := range spec.Env {
		if !isValidEnvKey(k) || isBlockedOverrideKey(k) {
			return nil, fail("dangerous_env_denied")
		}
		if len(v) > 8192 {
			return nil, fail("invalid_request")
		}
		if _, matched := CheckSensitiveReferences(v, spec.WorkspaceRoot, spec.TempRoot, spec.ProtectedDirs); matched {
			return nil, fail("sensitive_path_denied")
		}
	}

	envMap := map[string]string{}
	origKey := map[string]string{}
	for _, entry := range os.Environ() {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || k == "" || !shouldInheritEnvEntry(k, v) {
			continue
		}
		upper := strings.ToUpper(k)
		envMap[upper] = v
		origKey[upper] = k
	}

	applyPathPrepends(envMap, origKey, spec.BundledNodeDir)
	if isolateTempAndSSL {
		if err := applyRedirectedTempVars(envMap, origKey, spec.TempRoot); err != nil {
			return nil, err
		}
	}

	defaults := map[string]string{
		"PYTHONIOENCODING":    "utf-8",
		"PYTHONUTF8":          "1",
		"GIT_TERMINAL_PROMPT": "0",
		"GCM_INTERACTIVE":     "never",
		"CI":                  "1",
		"NO_COLOR":            "1",
		"TERM":                "dumb",
	}
	if isolateTempAndSSL {
		defaults["GIT_SSL_BACKEND"] = "openssl"
	}
	for k, v := range defaults {
		upper := strings.ToUpper(k)
		if _, exists := envMap[upper]; !exists {
			envMap[upper] = v
			origKey[upper] = k
		}
	}
	for k, v := range spec.Env {
		upper := strings.ToUpper(k)
		envMap[upper] = v
		origKey[upper] = k
	}

	out := make([]string, 0, len(envMap))
	for upper, v := range envMap {
		name := origKey[upper]
		if name == "" {
			name = upper
		}
		out = append(out, name+"="+v)
	}
	return out, nil
}

func isValidEnvKey(k string) bool {
	if k == "" || len(k) > 128 {
		return false
	}
	return !strings.ContainsAny(k, "=\x00\r\n")
}

func isBlockedOverrideKey(k string) bool {
	upper := strings.ToUpper(strings.TrimSpace(k))
	if dangerousEnvKeys[upper] || strings.HasPrefix(upper, "ASTERHAND_") || strings.HasPrefix(upper, "OPENCLAW_") || strings.HasPrefix(upper, "DEEPSEEK_") {
		return true
	}
	return redirectedTempSubdirs[upper] != "" || upper == "TEMP" || upper == "TMP" || upper == "TMPDIR"
}

func shouldInheritEnvEntry(k, v string) bool {
	upper := strings.ToUpper(strings.TrimSpace(k))
	if !allowedInheritedEnvKeys[upper] {
		return false
	}
	if shouldScrubInheritedKey(upper) || hasURLUserInfo(v) {
		return false
	}
	return true
}

func shouldScrubInheritedKey(k string) bool {
	upper := strings.ToUpper(k)
	if strings.HasPrefix(upper, "ASTERHAND_") || strings.HasPrefix(upper, "OPENCLAW_") || strings.HasPrefix(upper, "DEEPSEEK_") {
		return true
	}
	for _, suffix := range []string{"_KEY", "_PAT", "_PWD", "_PASS", "_JWT"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	for _, secretToken := range []string{
		"TOKEN", "SECRET", "PASSWORD", "PASSWD",
		"API_KEY", "APIKEY", "PRIVATE_KEY", "ACCESS_KEY",
		"MASTER_KEY", "MANAGEMENT_KEY", "SIGNING_KEY", "SESSION_KEY",
		"CREDENTIAL", "AUTH_HEADER", "COOKIE", "BEARER",
	} {
		if strings.Contains(upper, secretToken) {
			return true
		}
	}
	return false
}

func hasURLUserInfo(v string) bool {
	rest := v
	for {
		idx := strings.Index(rest, "://")
		if idx < 0 {
			return false
		}
		after := rest[idx+3:]
		end := strings.IndexAny(after, "/?#,; \t\r\n")
		authority := after
		if end >= 0 {
			authority = after[:end]
		}
		if strings.Contains(authority, "@") {
			return true
		}
		if end < 0 {
			return false
		}
		rest = after[end:]
	}
}

func applyPathPrepends(envMap, origKey map[string]string, bundledNodeDir string) {
	if bundledNodeDir == "" {
		return
	}
	cleanNode := stripExtendedPrefix(filepath.Clean(bundledNodeDir))
	cur := envMap["PATH"]
	parts := strings.Split(cur, string(os.PathListSeparator))
	for _, part := range parts {
		if strings.EqualFold(filepath.Clean(strings.TrimSpace(part)), cleanNode) {
			return
		}
	}
	if cur == "" {
		envMap["PATH"] = cleanNode
	} else {
		envMap["PATH"] = cleanNode + string(os.PathListSeparator) + cur
	}
	if origKey["PATH"] == "" {
		origKey["PATH"] = "PATH"
	}
}

func applyRedirectedTempVars(envMap, origKey map[string]string, tempRoot string) error {
	cleanTemp := stripExtendedPrefix(filepath.Clean(tempRoot))
	if err := os.MkdirAll(cleanTemp, 0700); err != nil {
		return err
	}
	for k, sub := range redirectedTempSubdirs {
		target := cleanTemp
		if sub != "" {
			target = filepath.Join(cleanTemp, sub)
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
		}
		upper := strings.ToUpper(k)
		envMap[upper] = target
		origKey[upper] = k
	}
	return nil
}

func stripExtendedPrefix(p string) string {
	if strings.HasPrefix(p, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(p, `\\?\UNC\`)
	}
	return strings.TrimPrefix(p, `\\?\`)
}
