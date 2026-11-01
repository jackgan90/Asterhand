package quickinput

import (
	"embed"
	"encoding/json"
	"path"
	"strings"
)

// Resources and their policy are independent of the OS WebView implementation.
//
//go:embed assets/*
var inputAssets embed.FS

const surfaceOrigin = "https://quick-input.asterhand.invalid"
const surfaceURL = surfaceOrigin + "/quick-input/index.html"
const inputCSP = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"

var inputResourceTypes = resourceTypes()

func resourceTypes() map[string]string {
	allowed := map[string]string{"index.html": "text/html; charset=utf-8"}
	for _, name := range []string{"input.css", "chat.css"} {
		allowed[name] = "text/css; charset=utf-8"
	}
	for _, name := range []string{"input.js", "input-model.mjs", "webview2-bridge.mjs", "chat-client.mjs",
		"chat-model.mjs", "chat-renderer.mjs", "chat-ui.mjs", "chat-outbox.mjs", "chat-status.mjs", "chat-queue.mjs", "chat-connection.mjs",
		"chat-markup.mjs", "chat-math.mjs"} {
		allowed[name] = "application/javascript; charset=utf-8"
	}
	// The checked-in vendor manifest is the exact asset inventory, not a directory
	// prefix permission. LICENSE/metadata are shipped but not served to the page.
	data, _ := inputAssets.ReadFile("assets/vendor/katex/manifest.json")
	var vendor struct {
		Files map[string]string `json:"files"`
	}
	if json.Unmarshal(data, &vendor) != nil {
		return allowed
	}
	for name := range vendor.Files {
		if path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
			continue
		}
		mime := map[string]string{".mjs": "application/javascript; charset=utf-8", ".css": "text/css; charset=utf-8",
			".woff2": "font/woff2", ".woff": "font/woff", ".ttf": "font/ttf"}[path.Ext(name)]
		if mime != "" {
			allowed["vendor/katex/"+name] = mime
		}
	}
	return allowed
}

// inputResource is also used by the native resource callback. Query strings,
// traversal, foreign origins and writes are rejected by exact-name lookup.
func inputResource(method, uri string) ([]byte, string, int) {
	const prefix = surfaceOrigin + "/quick-input/"
	if method == "GET" && strings.HasPrefix(uri, prefix) {
		file := strings.TrimPrefix(uri, prefix)
		if mime, ok := inputResourceTypes[file]; ok {
			if data, err := inputAssets.ReadFile("assets/" + file); err == nil {
				return data, mime, 200
			}
		}
	}
	return []byte("Blocked"), "text/plain", 403
}
