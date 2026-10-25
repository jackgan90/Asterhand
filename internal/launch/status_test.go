package launch

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const testToken = "test-login-token-never-in-diagnostics"

func statusFixture(t *testing.T, home string, handler http.HandlerFunc) (*httptest.Server, runtimeRecord) {
	t.Helper()
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	writeJSON(t, filepath.Join(home, "config.json"), map[string]string{"node_id": "test-node"})
	if err := os.WriteFile(filepath.Join(home, "web-login.token"), []byte(testToken), 0600); err != nil {
		t.Fatal(err)
	}
	record := runtimeRecord{Address: strings.TrimPrefix(server.URL, "http://"), PID: os.Getpid()}
	return server, record
}

func healthy(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/health" {
		w.WriteHeader(404)
		return
	}
	fmt.Fprintf(w, `{"node_id":"test-node","pid":%d}`, os.Getpid())
}

func TestExistingURLUsesLiveLocalIdentity(t *testing.T) {
	home := t.TempDir()
	server, record := statusFixture(t, home, healthy)
	writeJSON(t, filepath.Join(home, "runtime.json"), record)
	address, err := ExistingURL(home)
	if err != nil || address != server.URL+"/#login="+testToken {
		t.Fatalf("health check failed: %v", err)
	}
}

func TestExistingURLIsReadOnlyForStoppedNode(t *testing.T) {
	home := filepath.Join(t.TempDir(), "absent")
	if _, err := ExistingURL(home); err == nil {
		t.Fatal("absent node accepted")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("status initialized a node home")
	}
}

func TestExistingURLRejectsWrongIdentityAndStalePID(t *testing.T) {
	for _, name := range []string{"node-id", "health-pid", "stale-pid", "http-error"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			_, record := statusFixture(t, home, func(w http.ResponseWriter, _ *http.Request) {
				id, pid := "test-node", os.Getpid()
				if name == "node-id" {
					id = "other-node"
				}
				if name == "health-pid" {
					pid++
				}
				if name == "http-error" {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
				fmt.Fprintf(w, `{"node_id":%q,"pid":%d}`, id, pid)
			})
			if name == "stale-pid" {
				record.PID = 0
			}
			writeJSON(t, filepath.Join(home, "runtime.json"), record)
			if _, err := ExistingURL(home); err == nil || strings.Contains(err.Error(), testToken) {
				t.Fatalf("invalid identity accepted or secret exposed: %v", err)
			}
		})
	}
}

func TestHealthDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		healthy(w, r)
	}))
	defer target.Close()
	home := t.TempDir()
	_, record := statusFixture(t, home, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/health", http.StatusFound)
	})
	writeJSON(t, filepath.Join(home, "runtime.json"), record)
	if _, err := ExistingURL(home); err == nil || followed.Load() != 0 {
		t.Fatal("health redirect was accepted or followed")
	}
}

func TestLocalAddressRejectsNonLoopbackAndInvalidPorts(t *testing.T) {
	for _, address := range []string{"example.com:17432", "100.64.0.2:17432", "0.0.0.0:17432", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:17432/health", "http://127.0.0.1:17432", "localhost:17432"} {
		if _, err := localAddress(address); err == nil {
			t.Fatalf("unsafe address accepted: %s", address)
		}
	}
	for _, address := range []string{"127.0.0.1:17432", "[::1]:17432"} {
		if _, err := localAddress(address); err != nil {
			t.Fatal(err)
		}
	}
}
