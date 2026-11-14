//go:build windows

package workspace

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testService(t *testing.T) (*Service, Scope) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "workspace"), filepath.Join(dir, "versions"), "workspace-test")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Configure(true, "policy-one"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.RuntimeStopped() })
	return s, Scope{Policy: "policy-one"}
}

func TestMutateRestoreAndStableReceipts(t *testing.T) {
	s, scope := testService(t)
	m := []Mutation{{Action: "write", Path: "notes/中文.txt", Expected: MissingRevision, Content: "old\n"}}
	op, err := s.Mutate("create", scope, m)
	if err != nil || op.State != "completed" {
		t.Fatalf("create: %+v %v", op, err)
	}
	read, err := s.Read("notes/中文.txt", "", 0)
	if err != nil || string(read.Data) != "old\n" {
		t.Fatalf("read: %+v %v", read, err)
	}
	op, err = s.Mutate("edit", scope, []Mutation{{Action: "write", Path: read.Path, Expected: read.Revision, Content: "new\n"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Restore("restore", scope, op.ID, read.Path, revision([]byte("new\n"))); err != nil {
		t.Fatal(err)
	}
	read, err = s.Read(read.Path, read.Revision, 0)
	if err != nil || string(read.Data) != "old\n" {
		t.Fatalf("restored: %+v %v", read, err)
	}
	if _, err = s.Mutate("create", scope, m); err != nil {
		t.Fatal(err)
	}
	m[0].Content = "different"
	if _, err = s.Mutate("create", scope, m); Code(err) != "identity_conflict" {
		t.Fatalf("identity: %v", err)
	}
	if _, err = s.Restore("stale", scope, op.ID, read.Path, "stale"); Code(err) != "revision_conflict" {
		t.Fatalf("stale: %v", err)
	}
}

func TestExternalWorkspaceWriter(t *testing.T) {
	mode := os.Getenv("ASTERHAND_WORKSPACE_TEST_CHILD")
	if mode == "" {
		return
	}
	name := os.Getenv("ASTERHAND_WORKSPACE_TEST_PATH")
	var err error
	switch mode {
	case "write":
		err = os.WriteFile(name, []byte("external"), 0600)
	case "replace":
		tmp := name + ".external"
		err = os.WriteFile(tmp, []byte("external"), 0600)
		if err == nil {
			err = os.Rename(tmp, name)
		}
	case "link":
		err = os.Link(name, name+".external-link")
	case "parent":
		err = os.Rename(filepath.Dir(name), filepath.Dir(name)+".external")
	}
	if err != nil {
		os.Exit(9)
	}
	os.Exit(0)
}

func externalAttempt(t *testing.T, mode, name string, denied bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestExternalWorkspaceWriter$")
	cmd.Env = append(os.Environ(), "ASTERHAND_WORKSPACE_TEST_CHILD="+mode, "ASTERHAND_WORKSPACE_TEST_PATH="+name)
	err := cmd.Run()
	if denied && err == nil {
		t.Fatalf("external %s unexpectedly succeeded", mode)
	}
	if !denied && err != nil {
		t.Fatalf("external %s: %v", mode, err)
	}
}

func TestNativeCallHoldsSnapshotAndParentThroughMutation(t *testing.T) {
	s, scope := testService(t)
	name := filepath.Join(s.root, "item.txt")
	if err := os.WriteFile(name, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Begin(Begin{ID: "native-call", Scope: scope, Tool: "write", Targets: []Target{{Path: "item.txt"}}, NewBytes: 3, Fingerprint: "one"})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"write", "replace", "parent"} {
		externalAttempt(t, mode, name, true)
	}
	// Windows permits linking even an exclusively opened file. Publication must
	// leave that externally-created alias attached to the unchanged old bytes.
	externalAttempt(t, "link", name, false)
	if _, err = s.Write("native-call", "item.txt", []byte("new"), false); err != nil {
		t.Fatal(err)
	}
	op, err := s.End("native-call", "completed", "")
	if err != nil || op.Changes[0].Before != revision([]byte("old")) || op.Changes[0].After != revision([]byte("new")) {
		t.Fatalf("end: %+v %v", op, err)
	}
	alias, err := os.ReadFile(name + ".external-link")
	if err != nil || string(alias) != "old" {
		t.Fatalf("outside alias modified: %q %v", alias, err)
	}
	externalAttempt(t, "write", name, false)
}

func TestConcurrentCreateDoesNotOverwrite(t *testing.T) {
	s, scope := testService(t)
	_, err := s.Begin(Begin{ID: "native-call", Scope: scope, Tool: "write", Targets: []Target{{Path: "item.txt"}}, NewBytes: 3, Fingerprint: "one"})
	if err != nil {
		t.Fatal(err)
	}
	externalAttempt(t, "write", filepath.Join(s.root, "item.txt"), false)
	if _, err = s.Write("native-call", "item.txt", []byte("new"), false); Code(err) != "revision_conflict" {
		t.Fatalf("write: %v", err)
	}
	if _, err = s.End("native-call", "failed", "revision_conflict"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.root, "item.txt"))
	if err != nil || string(data) != "external" {
		t.Fatalf("external lost: %q %v", data, err)
	}
}

func TestUnsafePathsAndExistingHardlink(t *testing.T) {
	s, scope := testService(t)
	for _, name := range []string{"../escape", "C:/escape", `\\server\share`, "x:secret", "a./b", "a /b", "NUL.txt", "COM¹", "LPT².txt", "a/../b"} {
		if _, err := s.Mutate("invalid", scope, []Mutation{{Action: "write", Path: name, Expected: MissingRevision, Content: "new"}}); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	outside := filepath.Join(filepath.Dir(s.root), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(s.root, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read("alias.txt", "", 0); Code(err) != "unsafe_link" {
		t.Fatalf("read alias: %v", err)
	}
	if _, err := s.Mutate("alias", scope, []Mutation{{Action: "write", Path: "alias.txt", Expected: revision([]byte("secret")), Content: "new"}}); Code(err) != "unsafe_link" {
		t.Fatalf("write alias: %v", err)
	}
}

func TestCrashRequiresExplicitReconciliation(t *testing.T) {
	s, scope := testService(t)
	if err := s.AcquireTurn("turn", scope.Policy); err != nil {
		t.Fatal(err)
	}
	scope.Turn = "turn"
	if _, err := s.Begin(Begin{ID: "unfinished", Scope: scope, Tool: "write", Targets: []Target{{Path: "item"}}, NewBytes: 3, Fingerprint: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("unfinished", "item", []byte("new"), false); err != nil {
		t.Fatal(err)
	}
	// Abrupt process death leaves the durable started journal. Close handles only.
	closeCall(s.active)
	s.active = nil
	s.turn = ""
	reopened, err := Open(s.root, s.journal, s.id)
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Configure(true, scope.Policy); err != nil {
		t.Fatal(err)
	}
	if err = reopened.AcquireTurn("next", scope.Policy); Code(err) != "reconciliation_required" {
		t.Fatalf("replayed: %v", err)
	}
	op, err := reopened.Resolve("unfinished", "Owned runtime has stopped; inspected current files.")
	if err != nil || op.State != "reconciled" || op.Changes[0].After != revision([]byte("new")) {
		t.Fatalf("resolve: %+v %v", op, err)
	}
	if err = reopened.AcquireTurn("next", scope.Policy); err != nil {
		t.Fatal(err)
	}
}

func TestJunctionAndRevokedPolicyRejectFileOperations(t *testing.T) {
	s, scope := testService(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(s.root, "junction")
	if output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Fatalf("create real junction: %v %s", err, output)
	}
	defer os.Remove(link)
	if _, err := s.Read("junction/secret.txt", "", 0); Code(err) != "unsafe_link" {
		t.Fatalf("junction read: %v", err)
	}
	if _, err := s.Mutate("junction-write", scope, []Mutation{{Action: "write", Path: "junction/secret.txt", Expected: revision([]byte("outside")), Content: "bad"}}); Code(err) != "unsafe_link" {
		t.Fatalf("junction write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
	if err != nil || string(data) != "outside" {
		t.Fatal("outside was changed", err)
	}
	if err = s.AcquireTurn("active", scope.Policy); err != nil {
		t.Fatal(err)
	}
	if err = s.Configure(false, "revoked"); Code(err) != "busy" {
		t.Fatal("revoked before active turn ended", err)
	}
	if err = s.ReleaseTurn("active", true); err != nil {
		t.Fatal(err)
	}
	if err = s.Configure(false, "revoked"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mutate("stale-policy", scope, []Mutation{{Action: "write", Path: "new.txt", Expected: MissingRevision, Content: "bad"}}); Code(err) != "not_authorized" {
		t.Fatal("revoked policy admitted mutation", err)
	}
}

func TestSearchPagingAndBoundedRead(t *testing.T) {
	s, scope := testService(t)
	content := strings.Repeat("hello", 20000)
	if _, err := s.Mutate("create", scope, []Mutation{{Action: "write", Path: "folder/file.txt", Expected: MissingRevision, Content: content}}); err != nil {
		t.Fatal(err)
	}
	page, err := s.Search("", "hello", "", true, true)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("search: %+v %v", page, err)
	}
	var result []byte
	var offset int64
	for {
		fragment, err := s.Read("folder/file.txt", revision([]byte(content)), offset)
		if err != nil {
			t.Fatal(err)
		}
		if len(fragment.Data) > PageBytes {
			t.Fatal("unbounded")
		}
		result = append(result, fragment.Data...)
		offset = fragment.Next
		if !fragment.More {
			break
		}
	}
	if !bytes.Equal(result, []byte(content)) {
		t.Fatal("fragment mismatch")
	}
	data, _ := json.Marshal(publicOperation(s.records["create"]))
	if bytes.Contains(data, []byte("hello")) {
		t.Fatal("body leaked into metadata")
	}
}
