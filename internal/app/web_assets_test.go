package app

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	for _, path := range []string{"/app.js", "/view-model.mjs", "/desktop-settings.mjs"} {
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
				expected := "taskViews"
				if path == "/desktop-settings.mjs" {
					expected = "desktopSettings"
				}
				if method == http.MethodGet && !strings.Contains(w.Body.String(), expected) {
					t.Fatal("module content missing")
				}
				if method == http.MethodHead && w.Body.Len() != 0 {
					t.Fatal("HEAD returned a body")
				}
			})
		}
	}
}
