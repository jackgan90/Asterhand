package quickinput

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestFormulaResourceInventoryAndIntegrity(t *testing.T) {
	data, err := inputAssets.ReadFile("assets/vendor/katex/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var vendor struct {
		Version string            `json:"version"`
		Files   map[string]string `json:"files"`
	}
	if err = json.Unmarshal(data, &vendor); err != nil {
		t.Fatal(err)
	}
	if vendor.Version == "" || len(vendor.Files) < 4 {
		t.Fatal("missing formula dependency inventory")
	}
	for name, digest := range vendor.Files {
		body, err := inputAssets.ReadFile("assets/vendor/katex/" + name)
		if err != nil {
			t.Fatal(err)
		}
		actual := sha256.Sum256(body)
		if hex.EncodeToString(actual[:]) != digest {
			t.Fatalf("vendor resource modified: %s", name)
		}
		if name != "LICENSE" {
			served, mime, status := inputResource("GET", surfaceOrigin+"/quick-input/vendor/katex/"+name)
			if status != 200 || mime == "" || len(served) != len(body) {
				t.Fatalf("resource unavailable: %s %d %s", name, status, mime)
			}
		}
	}
	for _, name := range []string{"../katex.mjs", "fonts/unknown.woff2", "fonts/../katex.mjs", "katex.mjs?x", "LICENSE", "manifest.json"} {
		if _, _, status := inputResource("GET", surfaceOrigin+"/quick-input/vendor/katex/"+name); status != 403 {
			t.Fatalf("unexpected resource permitted: %s", name)
		}
	}
	if !strings.Contains(inputCSP, "font-src 'self'") || strings.Contains(inputCSP, "unsafe-") {
		t.Fatal("formula CSP widened beyond local fonts")
	}
}
