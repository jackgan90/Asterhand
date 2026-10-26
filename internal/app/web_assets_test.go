package app

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackgan90/Asterhand/internal/testutil/webmodules"
	"github.com/jackgan90/Asterhand/web"
)

func TestWebModulesUseExecutableMIMEWithNoSniff(t *testing.T) {
	// Windows MIME associations can register .mjs as plain text. The served
	// application must not depend on the host's file associations.
	previous := mime.TypeByExtension(".mjs")
	if previous == "" {
		previous = "text/javascript"
	}
	if err := mime.AddExtensionType(".mjs", "text/plain"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mime.AddExtensionType(".mjs", previous) })
	a := &App{webAddress: "127.0.0.1:17432"}
	for _, module := range webmodules.Closure(t, web.Files, "index.html") {
		path := "/" + module
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+path, func(t *testing.T) {
				r := httptest.NewRequest(method, "http://"+a.webAddress+path, nil)
				w := httptest.NewRecorder()
				a.local(w, r)
				kind, _, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
				if w.Code != http.StatusOK || err != nil || kind != "application/javascript" {
					t.Fatalf("module response: status=%d type=%q error=%v", w.Code, kind, err)
				}
				if w.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal("module response removed nosniff")
				}
				data, err := web.Files.ReadFile(module)
				if err != nil || (method == http.MethodGet && w.Body.String() != string(data)) {
					t.Fatal("module content differs from embedded resource")
				}
				if method == http.MethodHead && w.Body.Len() != 0 {
					t.Fatal("HEAD returned a body")
				}
			})
		}
	}
	for _, path := range []string{"/missing.mjs", "/webview2-bridge.mjs"} {
		w := httptest.NewRecorder()
		a.local(w, httptest.NewRequest(http.MethodGet, "http://"+a.webAddress+path, nil))
		if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "<script") {
			t.Fatalf("unknown asset served as page: %s status=%d", path, w.Code)
		}
	}
}
