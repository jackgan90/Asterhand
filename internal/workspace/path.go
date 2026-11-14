package workspace

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Paths on the public boundary are portable workspace-relative names. Reject
// aliases rather than normalizing away Windows device or traversal semantics.
func relative(name string, rootAllowed bool) (string, error) {
	if name == "" || name == "." {
		if rootAllowed {
			return "", nil
		}
		return "", fail("invalid_path")
	}
	if !utf8.ValidString(name) || len(name) > 1024 || strings.ContainsAny(name, ":\x00\r\n\t") || strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fail("invalid_path")
	}
	name = strings.ReplaceAll(name, `\`, "/")
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.ContainsAny(part, `<>"|?*`) {
			return "", fail("invalid_path")
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		device := []rune(base)
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" ||
			(len(device) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.ContainsRune("123456789¹²³", device[3])) {
			return "", fail("invalid_path")
		}
	}
	return strings.Join(parts, "/"), nil
}

func pathKey(name string) string { return strings.ToLower(filepath.ToSlash(name)) }
