//go:build windows

package localcommands

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func readLaunchFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("script output missing: %s", path)
	return nil
}

func TestWindowsPowerShellActualArgumentsAndDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "脚本 space % data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "参数.ps1")
	output := filepath.Join(dir, "result.json")
	body := `param([string]$Output, [Parameter(ValueFromRemainingArguments=$true)][string[]]$Values)
@{values=$Values;directory=(Get-Location).Path;private=$env:ASTERHAND_COMMAND_PRIVATE_TEST} | ConvertTo-Json -Compress | Set-Content -LiteralPath $Output -Encoding UTF8`
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASTERHAND_COMMAND_PRIVATE_TEST", "must-not-inherit")
	values := []string{"", "中文 space", `a" b`, "%PATH%", "a&b|c^!", `C:\tail\`, "'single'"}
	c := Command{Script: script, Arguments: append([]string{"-Output", output}, values...), Window: "background"}
	if err := StartWindows(c, 0); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Values    []string `json:"values"`
		Directory string   `json:"directory"`
		Private   string   `json:"private"`
	}
	data := readLaunchFile(t, output)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	if !reflect.DeepEqual(got.Values, values) || !strings.EqualFold(got.Directory, dir) || got.Private != "" {
		t.Fatalf("got %+v, want %q", got, values)
	}
}

func TestWindowsBatchActualArguments(t *testing.T) {
	for _, ext := range []string{".bat", ".cmd"} {
		t.Run(ext, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "脚本 space %PATH% & data")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(dir, "run"+ext)
			output := filepath.Join(dir, "result.json")
			// The helper records what a conventional batch %* forwarding entry sees.
			body := "@echo off\r\n\"" + os.Args[0] + "\" -test.run=^TestCommandArgvHelper$ -- %*\r\n"
			if err := os.WriteFile(script, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			values := []string{"", "中文 space", "%PATH%", "a&b|c^!", `C:\path`, "'single'"}
			c := Command{Script: script, Arguments: append([]string{output}, values...), Window: "background"}
			if err := StartWindows(c, 0); err != nil {
				t.Fatal(err)
			}
			var got []string
			data := readLaunchFile(t, output)
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("%s: %v", data, err)
			}
			if !reflect.DeepEqual(got, values) {
				t.Fatalf("got %q, want %q", got, values)
			}
		})
	}
}

func TestCommandArgvHelper(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && len(os.Args) > i+1 {
			data, _ := json.Marshal(os.Args[i+2:])
			if err := os.WriteFile(os.Args[i+1], data, 0600); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}

func TestWindowsBatchParameterVariables(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "vars.cmd")
	output := filepath.Join(dir, "vars.txt")
	body := "@echo off\r\nsetlocal DisableDelayedExpansion\r\nset \"observed=%~2\"\r\nset \"result=%~1\"\r\npowershell.exe -NoLogo -NoProfile -Command \"[IO.File]::WriteAllText($env:result,$env:observed)\"\r\n"
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	c := Command{Script: script, Arguments: []string{output, `hello space & percent %PATH% ! C:\tail\`}, Window: "background"}
	if err := StartWindows(c, 0); err != nil {
		t.Fatal(err)
	}
	if got := string(readLaunchFile(t, output)); got != c.Arguments[1] {
		t.Fatalf("parameter variable: %q", got)
	}
}

func TestWindowsTerminalAndBatchQuoteBoundary(t *testing.T) {
	if err := StartWindows(Command{Script: `C:\test.cmd`, Arguments: []string{`a"b`}}, 0); err == nil {
		t.Fatal("ambiguous batch quote reached process creation")
	}
	if os.Getenv("ASTERHAND_COMMAND_TERMINAL_TEST") != "1" {
		t.Skip("explicit terminal test required")
	}
	for _, ext := range []string{".cmd", ".ps1"} {
		dir := t.TempDir()
		script, output := filepath.Join(dir, "terminal"+ext), filepath.Join(dir, "result.txt")
		body := `echo ready > "` + output + "\"\r\nexit\r\n"
		if ext == ".ps1" {
			body = `[IO.File]::WriteAllText('` + strings.ReplaceAll(output, "'", "''") + `','ready'); exit`
		}
		if err := os.WriteFile(script, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		c := Command{Script: script, Directory: os.TempDir(), Window: "terminal"}
		if err := StartWindows(c, 0); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(string(readLaunchFile(t, output))); got != "ready" {
			t.Fatal(got)
		}
	}
}

func TestDetachedScriptOutlivesLauncherProcess(t *testing.T) {
	if os.Getenv("ASTERHAND_LAUNCH_PARENT") != "" {
		c := Command{Script: os.Getenv("ASTERHAND_LAUNCH_PARENT"), Window: "background"}
		if err := StartWindows(c, 0); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "delayed.ps1")
	output := filepath.Join(dir, "survived.txt")
	body := `Start-Sleep -Milliseconds 800; [IO.File]::WriteAllText('` + strings.ReplaceAll(output, "'", "''") + `','survived')`
	if err := os.WriteFile(script, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDetachedScriptOutlivesLauncherProcess$")
	cmd.Env = append(os.Environ(), "ASTERHAND_LAUNCH_PARENT="+script)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, data)
	}
	if _, err := os.Stat(output); err == nil {
		t.Fatal("launcher waited for script completion")
	}
	if got := string(readLaunchFile(t, output)); got != "survived" {
		t.Fatal(got)
	}
}
