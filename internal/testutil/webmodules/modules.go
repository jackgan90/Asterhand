// Package webmodules checks the static native-module closure of embedded pages.
package webmodules

import (
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var scripts = regexp.MustCompile(`<script\s+type="module"\s+src="([^"]+)"`)
var imports = regexp.MustCompile(`(?:\bfrom\s*|\bimport\s*)["']([^"']+)["']`)

// Closure starts at the actual HTML entry and follows every static import.
// The project uses native static imports only, with no bundler or bare imports.
func Closure(t *testing.T, files fs.FS, entry string) []string {
	t.Helper()
	html, err := fs.ReadFile(files, entry)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		data, err := fs.ReadFile(files, name)
		if err != nil {
			t.Fatalf("module %s: %v", name, err)
		}
		for _, match := range imports.FindAllSubmatch(data, -1) {
			specifier := string(match[1])
			if !strings.HasPrefix(specifier, "./") {
				t.Fatalf("module %s has nonlocal dependency %q", name, specifier)
			}
			visit(path.Join(path.Dir(name), specifier))
		}
	}
	for _, match := range scripts.FindAllSubmatch(html, -1) {
		visit(strings.TrimPrefix(string(match[1]), "/"))
	}
	if len(seen) == 0 {
		t.Fatal("no native module entry found")
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
