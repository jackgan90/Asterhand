package config

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestKeyLifecycleAndIdentity(t *testing.T) {
	home := t.TempDir()
	m, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	id := m.Get().NodeID
	key := "synthetic-test-key-not-a-provider-credential"
	if err = m.SetKey(key); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		b, _ := os.ReadFile(filepath.Join(home, "deepseek.secret"))
		if bytes.Contains(b, []byte(key)) {
			t.Fatal("plaintext persisted")
		}
	}
	m, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := m.Key()
	if err != nil || actual != key || m.Get().NodeID != id {
		t.Fatal("restart lost identity or credential")
	}
	if err = m.SetKey("replacement"); err != nil {
		t.Fatal(err)
	}
	if actual, _ = m.Key(); actual != "replacement" {
		t.Fatal("replacement failed")
	}
	if err = m.SetKey(""); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Key(); err == nil {
		t.Fatal("deleted credential remains readable")
	}
}
func TestExclusiveNodeLock(t *testing.T) {
	home := t.TempDir()
	release, err := Lock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if unlock, err := Lock(home); err == nil {
		unlock()
		t.Fatal("two processes can own the same node")
	}
}
