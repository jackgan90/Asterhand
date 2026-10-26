//go:build windows

package quickinput

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/testutil/webmodules"
)

func TestInputModuleClosureIsEmbeddedAndAllowed(t *testing.T) {
	files, err := fs.Sub(inputAssets, "assets")
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range webmodules.Closure(t, files, "index.html") {
		body, mime, status := inputResource("GET", surfaceOrigin+"/quick-input/"+module)
		if status != 200 || mime != "application/javascript; charset=utf-8" || len(body) == 0 {
			t.Fatalf("module %s status=%d type=%s", module, status, mime)
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
