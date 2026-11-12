package automation

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackgan90/Asterhand/internal/config"
)

// Decoder checkpoints contain derived output bytes. Authenticate them so a
// caller cannot insert fabricated text into a page presented as a stored log.
func (r *Registry) LoadCursorKey(home string) error {
	path := filepath.Join(home, "automation-cursor.key")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return config.AtomicFile(path, r.cursorKey[:], 0600)
	}
	if err != nil {
		return err
	}
	if len(b) != len(r.cursorKey) {
		return Fail("configuration", "Invalid cursor signing key")
	}
	copy(r.cursorKey[:], b)
	return nil
}

func (r *Registry) SealCursor(binding any, after string) string {
	raw := EncodeCursor(binding, "", after)
	mac := hmac.New(sha256.New, r.cursorKey[:])
	mac.Write([]byte(raw))
	return raw + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (r *Registry) OpenCursor(raw string, binding any) (Cursor, error) {
	if raw == "" {
		return Cursor{}, nil
	}
	body, signature, ok := strings.Cut(raw, ".")
	if !ok || len(body) > 24*1024 {
		return Cursor{}, Fail("invalid_cursor", "Malformed signed cursor")
	}
	got, err := base64.RawURLEncoding.DecodeString(signature)
	mac := hmac.New(sha256.New, r.cursorKey[:])
	mac.Write([]byte(body))
	if err != nil || !hmac.Equal(got, mac.Sum(nil)) {
		return Cursor{}, Fail("invalid_cursor", "Cursor signature does not match this node")
	}
	return ParseCursor(body, binding)
}
