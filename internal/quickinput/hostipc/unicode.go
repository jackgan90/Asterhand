package hostipc

import "strconv"

// encoding/json replaces isolated surrogate escapes with U+FFFD. Reject them
// before decoding so a malformed edit is never silently changed into a draft.
func PairedSurrogates(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		value, err := strconv.ParseUint(raw[i+1:i+5], 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value < 0xd800 || value > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || raw[i+1:i+3] != `\u` {
			return false
		}
		low, err := strconv.ParseUint(raw[i+3:i+7], 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
