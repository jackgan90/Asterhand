package app

import (
	"bytes"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

func TestRejectedHumanSettingsHasRecoverableTerminalReceipt(t *testing.T) {
	n := newNode(t, &fakeExecutor{})
	a := n.app
	root := filepath.Join(t.TempDir(), "must-not-be-created")
	id := operations.NewID(time.Now())
	input := map[string]any{"operation_id": id, "name": "valid-name", "root": root, "endpoint": "http://bad.example", "expected_revision": nodeSettingsRevision(a.Config.Get())}
	w := httptest.NewRecorder()
	a.settings(w, httptest.NewRequest("POST", "/api/settings", bytes.NewReader(compactResult(input))))
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("validation created a workspace", err)
	}
	receipt, err := a.Store.Lookup("human:web", a.Config.Get().NodeID, id)
	if err != nil || receipt.State != "rejected" || receipt.Accepted {
		t.Fatal(receipt, err)
	}
	recovered, err := a.reconcileAutomationOperation(receipt)
	if err != nil || recovered.State != "rejected" {
		t.Fatal(recovered, err)
	}
	// A genuinely uncertain completion must not be converted into rejection.
	unknownID := operations.NewID(time.Now())
	_, finish, err := a.beginHumanSettings(unknownID, map[string]string{"name": "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	finish(nil)
	unknown, err := a.Store.Lookup("human:web", a.Config.Get().NodeID, unknownID)
	if err != nil || unknown.State != "unknown" {
		t.Fatal(unknown, err)
	}
}
