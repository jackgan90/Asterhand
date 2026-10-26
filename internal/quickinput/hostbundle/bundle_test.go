package hostbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHostIdentityAndDigestAreRequired(t *testing.T) {
	dir := t.TempDir()
	contents := []byte("fixture binary")
	digest := sha256.Sum256(contents)
	manifest, _ := json.Marshal(Identity{1, "test-version", "test-source", hex.EncodeToString(digest[:])})
	if err := os.WriteFile(filepath.Join(dir, Executable), contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, Manifest), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := Verify(dir, "test-version", "test-source")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if f, err = Verify(dir, "test-version", "other-source"); err == nil {
		f.Close()
		t.Fatal("mixed builds accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, Executable), []byte("different binary"), 0600); err != nil {
		t.Fatal(err)
	}
	if f, err = Verify(dir, "test-version", "test-source"); err == nil {
		f.Close()
		t.Fatal("modified binary accepted")
	}
}
