//go:build windows

package workspace

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestRejectedAdmissionDoesNotCreateParents(t *testing.T) {
	s, scope := testService(t)
	for _, scenario := range []struct{ id, expected string }{{"conflict", "stale"}, {"persist", MissingRevision}} {
		t.Run(scenario.id, func(t *testing.T) {
			if scenario.id == "persist" {
				if err := os.Mkdir(filepath.Join(s.journal, "persist.json"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.Mutate(scenario.id, scope, []Mutation{{Action: "write", Path: scenario.id + "/nested/file", Expected: scenario.expected, Content: "new"}})
			if err == nil {
				t.Fatal("rejection expected")
			}
			if _, err := os.Stat(filepath.Join(s.root, scenario.id)); !os.IsNotExist(err) {
				t.Fatalf("rejected operation created a directory: %v", err)
			}
			if len(s.records) != 0 {
				t.Fatal("rejected admission published an operation")
			}
		})
	}
}

func TestFullWorkspaceAllowsRecoveryAndCountsNewParents(t *testing.T) {
	s, scope := testService(t)
	for i := 0; i < MaxWorkspaceFiles; i++ {
		if err := os.WriteFile(filepath.Join(s.root, fmt.Sprintf("f%05d", i)), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Mutate("reject-parent", scope, []Mutation{{Action: "write", Path: "new/file", Expected: MissingRevision, Content: "x"}}); Code(err) != "workspace_limit" {
		t.Fatalf("missing parent quota: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "new")); !os.IsNotExist(err) {
		t.Fatal("capacity rejection created a directory", err)
	}
	if _, err := s.Mutate("delete-full", scope, []Mutation{{Action: "remove", Path: "f00000", Expected: revision([]byte("x"))}}); err != nil {
		t.Fatal("full workspace could not delete", err)
	}
	// One remaining slot is insufficient for both the parent and the new file.
	if _, err := s.Mutate("reject-two", scope, []Mutation{{Action: "write", Path: "new/file", Expected: MissingRevision, Content: "x"}}); Code(err) != "workspace_limit" {
		t.Fatalf("new directory was not counted: %v", err)
	}
	if _, err := s.Mutate("delete-again", scope, []Mutation{{Action: "remove", Path: "f00001", Expected: revision([]byte("x"))}}); err != nil {
		t.Fatal(err)
	}
	op, err := s.Mutate("create-two", scope, []Mutation{{Action: "write", Path: "new/file", Expected: MissingRevision, Content: "x"}})
	if err != nil || len(op.ParentDirectories) != 1 || op.ParentDirectories[0] != "new" {
		t.Fatalf("reserved parent: %+v %v", op, err)
	}
}

func TestExternalOverCapacityAllowsShrinkButRejectsGrowth(t *testing.T) {
	s, scope := testService(t)
	large, err := os.Create(filepath.Join(s.root, "external.bin"))
	if err != nil {
		t.Fatal(err)
	}
	err = large.Truncate(MaxWorkspaceBytes + 1)
	large.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(s.root, "small.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Begin(Begin{ID: "native-edit", Scope: scope, Tool: "edit_file", Targets: []Target{{Path: "small.txt"}}, NewBytes: 10, Fingerprint: "edit"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Write("native-edit", "small.txt", []byte("larger"), false); Code(err) != "workspace_limit" {
		t.Fatalf("growth admitted: %v", err)
	}
	if _, err = s.Write("native-edit", "small.txt", []byte("x"), false); err != nil {
		t.Fatal("shrinking edit rejected", err)
	}
	if _, err = s.End("native-edit", "completed", ""); err != nil {
		t.Fatal(err)
	}
	op, err := s.Mutate("overwrite", scope, []Mutation{{Action: "write", Path: "small.txt", Expected: revision([]byte("x")), Content: ""}})
	if err != nil || op.State != "completed" {
		t.Fatal("shrinking overwrite rejected", err)
	}
}

func TestNativePatchMoveAccountsForSourceContent(t *testing.T) {
	s, scope := testService(t)
	content := strings.Repeat("x", 3000) + "old\n"
	if err := os.WriteFile(filepath.Join(s.root, "source.txt"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Begin(Begin{ID: "move", Scope: scope, Tool: "apply_file_patch", Fingerprint: "move", NewBytes: 92,
		Targets: []Target{{Path: "source.txt", Remove: true}, {Path: "destination.txt"}}})
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(content, "old", "new", 1)
	if _, err := s.Write("move", "source.txt", []byte(updated), false); Code(err) != "undeclared_write" {
		t.Fatal("remove-only source accepted a write", err)
	}
	if _, err := s.Write("move", "destination.txt", []byte(updated), false); err != nil {
		t.Fatal("source content omitted from patch budget", err)
	}
	if err := s.Remove("move", "source.txt"); err != nil {
		t.Fatal(err)
	}
	if op, err := s.End("move", "completed", ""); err != nil || op.State != "completed" {
		t.Fatalf("move: %+v %v", op, err)
	}
	if data, err := os.ReadFile(filepath.Join(s.root, "destination.txt")); err != nil || string(data) != updated {
		t.Fatal("destination mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "source.txt")); !os.IsNotExist(err) {
		t.Fatal("source remains", err)
	}
}

func TestStoppedRuntimeReleasesTurnWhenTerminalPersistenceFails(t *testing.T) {
	s, scope := testService(t)
	scope.Turn = "turn"
	if err := s.AcquireTurnScope(scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "unfinished", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "file"}}, NewBytes: 1, Fingerprint: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("unfinished", "file", []byte("x"), false); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(s.journal, "unfinished.json")
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(journal, 0700); err != nil {
		t.Fatal(err)
	}
	if err = s.RuntimeStopped(); err == nil {
		t.Fatal("terminal persistence unexpectedly succeeded")
	}
	if state := s.Status(); state.Busy || state.Reason != "reconciliation_required" {
		t.Fatalf("stopped runtime retained its turn or lost uncertainty: %+v", state)
	}
	if err = os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(journal, before, 0600); err != nil {
		t.Fatal(err)
	}
	if op, err := s.ResolveRequest("resolve", "unfinished", "Stopped runtime; verified current file."); err != nil || op.State != "reconciled" {
		t.Fatalf("could not reconcile after I/O recovery: %+v %v", op, err)
	}
}

func TestParentChangedToJunctionCannotRedirectIO(t *testing.T) {
	s, scope := testService(t)
	outside := t.TempDir()
	parent := filepath.Join(s.root, "guarded")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(Begin{ID: "race", Scope: scope, Tool: "write_file", Targets: []Target{{Path: "guarded/escaped.txt"}}, NewBytes: 3, Fingerprint: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := makeJunctionInPlace(parent, outside); err != nil {
		t.Fatal("could not exercise actual reparse race", err)
	}
	defer func() { _ = s.RuntimeStopped(); _ = os.Remove(parent) }()
	if _, err := s.Write("race", "guarded/escaped.txt", []byte("new"), false); err == nil {
		t.Fatal("publication followed changed parent")
	}
	if _, err := s.NativeRead("race", "guarded/secret.txt"); err == nil {
		t.Fatal("read followed changed parent")
	}
	if _, err := s.End("race", "failed", "reparse_race"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatal("outside file was created", err)
	}
}

func makeJunctionInPlace(link, destination string) error {
	name, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	substitute, err := windows.UTF16FromString(`\??\` + destination)
	if err != nil {
		return err
	}
	printed, err := windows.UTF16FromString(destination)
	if err != nil {
		return err
	}
	buffer := make([]byte, 16+2*(len(substitute)+len(printed)))
	binary.LittleEndian.PutUint32(buffer[0:4], windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buffer[4:6], uint16(len(buffer)-8))
	binary.LittleEndian.PutUint16(buffer[10:12], uint16((len(substitute)-1)*2))
	binary.LittleEndian.PutUint16(buffer[12:14], uint16(len(substitute)*2))
	binary.LittleEndian.PutUint16(buffer[14:16], uint16((len(printed)-1)*2))
	for i, value := range append(substitute, printed...) {
		binary.LittleEndian.PutUint16(buffer[16+i*2:18+i*2], value)
	}
	var returned uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil)
}
