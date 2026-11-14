//go:build windows

package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLongNamesAndRenameBufferAlignment(t *testing.T) {
	s, scope := testService(t)
	for i := 1; i <= 8; i++ {
		name := strings.Repeat("nested-directory/", 18) + strings.Repeat("x", i) + ".txt"
		op, err := s.Mutate(fmt.Sprintf("long%d", i), scope, []Mutation{{Action: "write", Path: name, Expected: MissingRevision, Content: "old"}})
		if err != nil {
			t.Fatal(name, err)
		}
		if _, err = s.Mutate(fmt.Sprintf("update%d", i), scope, []Mutation{{Action: "write", Path: name, Expected: op.Changes[0].After, Content: "new"}}); err != nil {
			t.Fatal(err)
		}
		read, err := s.Read(name, "", 0)
		if err != nil || string(read.Data) != "new" {
			t.Fatal(read, err)
		}
	}
}

func TestSearchCursorVisitsNestedSiblingsExactlyOnce(t *testing.T) {
	s, _ := testService(t)
	want := map[string]bool{}
	for _, dir := range []string{"a", "a.txt", "a0"} {
		if err := os.MkdirAll(filepath.Join(s.root, dir), 0700); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 80; i++ {
			name := fmt.Sprintf("%s/%03d.txt", dir, i)
			if err := os.WriteFile(filepath.Join(s.root, name), []byte("needle"), 0600); err != nil {
				t.Fatal(err)
			}
			want[name] = true
		}
	}
	cursor := ""
	for pageCount := 0; ; pageCount++ {
		if pageCount > 4 {
			t.Fatal("cursor did not terminate")
		}
		page, err := s.Search("", "needle", cursor, true, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if !want[item.Path] {
				t.Fatalf("duplicate/unexpected result %s", item.Path)
			}
			delete(want, item.Path)
		}
		if page.Next == "" {
			break
		}
		if page.Next == cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = page.Next
	}
	if len(want) != 0 {
		t.Fatalf("missing %d files", len(want))
	}
}

func TestActivityCursorDeliversTerminalAndReconciledUpdates(t *testing.T) {
	s, scope := testService(t)
	start, err := s.Begin(Begin{ID: "activity", Scope: scope, Tool: "read", Fingerprint: "one"})
	if err != nil {
		t.Fatal(err)
	}
	page, cursor := s.Operations("", 0)
	if len(page) != 1 || page[0].State != "started" {
		t.Fatal(page)
	}
	ended, err := s.End(start.ID, "unknown", "runtime_stopped")
	if err != nil {
		t.Fatal(err)
	}
	page, cursor = s.Operations("", cursor)
	if len(page) != 1 || page[0].State != "unknown" || ended.Sequence <= start.Sequence {
		t.Fatal(page)
	}
	if _, err = s.Resolve(start.ID, "Inspected the stopped runtime and current files."); err != nil {
		t.Fatal(err)
	}
	page, _ = s.Operations("", cursor)
	if len(page) != 1 || page[0].State != "reconciled" {
		t.Fatal(page)
	}
}

func TestVersionRetentionDoesNotEraseRecentBatchNeighbors(t *testing.T) {
	s, scope := testService(t)
	first, err := s.Mutate("first", scope, []Mutation{{Action: "write", Path: "repeated", Expected: MissingRevision, Content: "0"}, {Action: "write", Path: "neighbor", Expected: MissingRevision, Content: "keep"}})
	if err != nil {
		t.Fatal(err)
	}
	rev := revision([]byte("0"))
	for i := 1; i <= 22; i++ {
		text := fmt.Sprint(i)
		if _, err = s.Mutate(fmt.Sprintf("update%d", i), scope, []Mutation{{Action: "write", Path: "repeated", Expected: rev, Content: text}}); err != nil {
			t.Fatal(err)
		}
		rev = revision([]byte(text))
	}
	versions, _, err := s.Versions("repeated", 0)
	if err != nil || len(versions) != 20 {
		t.Fatal(len(versions), err)
	}
	if _, err = s.Restore("expired", scope, first.ID, "repeated", rev); Code(err) != "version_expired" {
		t.Fatal(err)
	}
	if _, err = s.Restore("neighbor-restore", scope, first.ID, "neighbor", revision([]byte("keep"))); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Read("neighbor", "", 0); Code(err) != "not_found" {
		t.Fatal(err)
	}
}
