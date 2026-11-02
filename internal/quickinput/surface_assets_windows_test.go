//go:build windows

package quickinput

import (
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackgan90/Asterhand/internal/testutil/webmodules"
	"github.com/jackgan90/Asterhand/web"
)

func TestInputModuleClosureIsEmbeddedAndAllowed(t *testing.T) {
	files, err := fs.Sub(inputAssets, "assets")
	if err != nil {
		t.Fatal(err)
	}
	closureFiles := fstest.MapFS{}
	if err = fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		body, err := fs.ReadFile(files, name)
		closureFiles[name] = &fstest.MapFile{Data: body}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	shared, err := web.Files.ReadFile("command-run.mjs")
	if err != nil {
		t.Fatal(err)
	}
	closureFiles["shared/command-run.mjs"] = &fstest.MapFile{Data: shared}
	aliases := map[string]string{"../../../web/command-run.mjs": "shared/command-run.mjs"}
	for _, entry := range []string{"index.html", "command-log.html"} {
		for _, module := range webmodules.ClosureWithAliases(t, closureFiles, entry, aliases) {
			uri := surfaceOrigin + "/quick-input/" + module
			if module == "shared/command-run.mjs" {
				uri = surfaceOrigin + "/web/command-run.mjs"
			}
			body, mime, status := inputResource("GET", uri)
			if status != 200 || mime != "application/javascript; charset=utf-8" || len(body) == 0 {
				t.Fatalf("module %s status=%d type=%s", module, status, mime)
			}
		}
	}
	for _, uri := range []string{
		surfaceOrigin + "/quick-input/unknown.mjs", surfaceOrigin + "/quick-input/../app.js",
		surfaceOrigin + "/quick-input/webview2-bridge.mjs?query", "https://example.invalid/input.js",
		surfaceOrigin + ".evil/quick-input/input.js",
	} {
		if _, _, status := inputResource("GET", uri); status != 403 {
			t.Fatalf("allowed unexpected resource %s", uri)
		}
	}
	if _, _, status := inputResource("POST", surfaceURL); status != 403 {
		t.Fatal("allowed a write")
	}
	if !strings.Contains(inputCSP, "connect-src 'none'") {
		t.Fatal("input networking enabled")
	}
}
