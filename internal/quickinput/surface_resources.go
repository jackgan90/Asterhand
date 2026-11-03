package quickinput

import (
	"embed"
	"encoding/json"
	"github.com/jackgan90/Asterhand/web"
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
	allowed := map[string]string{"index.html": "text/html; charset=utf-8", "command-log.html": "text/html; charset=utf-8"}
	for _, name := range []string{"input.css", "chat.css", "commands.css", "command-log.css"} {
		allowed[name] = "text/css; charset=utf-8"
	}
	// Monochrome CSS mask icons; the page colors them with the theme.
	for _, name := range []string{"check", "cross", "alert", "logs", "stop", "gear", "more", "down"} {
		allowed["icons/"+name+".svg"] = "image/svg+xml"
	}
	for _, name := range []string{"input.js", "i18n.mjs", "i18n-catalog.mjs", "input-model.mjs", "composer-layout.mjs", "webview2-bridge.mjs", "chat-client.mjs",
		"chat-model.mjs", "chat-renderer.mjs", "chat-ui.mjs", "chat-outbox.mjs", "chat-status.mjs", "chat-queue.mjs", "chat-connection.mjs",
		"chat-markup.mjs", "chat-math.mjs", "command-client.mjs", "command-model.mjs", "command-results.mjs", "command-ui.mjs", "command-log.mjs", "command-run.mjs", "log-model.mjs", "log-graph.mjs", "log-node.mjs", "log-reader.mjs", "log-pane.mjs"} {
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
	if method == "GET" && uri == surfaceOrigin+"/web/command-run.mjs" {
		data, err := web.Files.ReadFile("command-run.mjs")
		if err == nil {
			return data, "application/javascript; charset=utf-8", 200
		}
	}
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
