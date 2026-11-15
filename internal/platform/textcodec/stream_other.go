//go:build !windows

package textcodec

// DefaultLegacyEncoding returns GB18030 as the legacy multi-byte fallback on
// non-UTF-8 byte streams.
func DefaultLegacyEncoding() string {
	return "gbk"
}
