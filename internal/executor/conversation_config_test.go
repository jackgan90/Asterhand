package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConversationTempPathsSupportDeepWindowsRoots(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path namespace")
	}
	root := filepath.Join(t.TempDir(), strings.Repeat("deep-", 30))
	values := map[string]string{}
	for _, item := range conversationEnvironment(root, "node.exe", "", "", "fixture-token") {
		parts := strings.SplitN(item, "=", 2)
		values[parts[0]] = parts[1]
	}
	if !strings.HasPrefix(values["TEMP"], `\\?\`) || values["TEMP"] != values["TMP"] {
		t.Fatal("temporary root lacks long-path support")
	}
	if values["USERPROFILE"] != filepath.Join(root, "userprofile") {
		t.Fatal("unrelated environment changed")
	}
	if err := os.MkdirAll(values["TEMP"], 0700); err != nil {
		t.Fatal(err)
	}
	if conversationTempPath(`\\server\share\temp`) != `\\?\UNC\server\share\temp` {
		t.Fatal("UNC temp path lost its server/share")
	}
	if conversationTempPath(values["TEMP"]) != values["TEMP"] {
		t.Fatal("namespace prefix duplicated")
	}
}
