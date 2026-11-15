package textcodec

import (
	"bytes"
	"unicode/utf8"
)

// StreamDecoder decodes streaming process output across UTF-8, UTF-16LE BOM,
// and legacy Windows OEM/ANSI codepages (GB18030/GBK or Big5).
type StreamDecoder struct {
	started  bool
	bomBuf   []byte
	encoding string // "utf-16le", "utf-8", "gbk", "big5" when pinned by BOM or legacy preference
	fallback string // legacy codepage fallback ("gbk" or "big5")
	pending  []byte
	stripBOM bool
}

// NewStreamDecoder creates a streaming decoder initialized with the OS fallback codepage.
func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{fallback: DefaultLegacyEncoding(), stripBOM: true}
}

// Write feeds an arbitrary byte chunk and returns valid UTF-8 text.
func (d *StreamDecoder) Write(chunk []byte) string {
	if len(chunk) == 0 {
		return ""
	}
	if !d.started {
		d.bomBuf = append(d.bomBuf, chunk...)
		if len(d.bomBuf) < 3 && (d.bomBuf[0] == 0xef || d.bomBuf[0] == 0xff) {
			return ""
		}
		d.started = true
		chunk = d.bomBuf
		d.bomBuf = nil
		if len(chunk) >= 2 && chunk[0] == 0xff && chunk[1] == 0xfe {
			d.encoding = "utf-16le"
		} else if len(chunk) >= 3 && chunk[0] == 0xef && chunk[1] == 0xbb && chunk[2] == 0xbf {
			d.encoding = "utf-8"
		}
	}
	return d.decodeStep(chunk, false)
}

// Flush flushes any remaining buffered or incomplete trailing bytes as valid UTF-8.
func (d *StreamDecoder) Flush() string {
	var rem []byte
	if !d.started && len(d.bomBuf) > 0 {
		d.started = true
		rem = d.bomBuf
		d.bomBuf = nil
		if len(rem) >= 2 && rem[0] == 0xff && rem[1] == 0xfe {
			d.encoding = "utf-16le"
		}
	}
	return d.decodeStep(rem, true)
}

func (d *StreamDecoder) decodeStep(chunk []byte, final bool) string {
	if d.encoding == "utf-16le" {
		state := State{Pending: d.pending, IgnoreBOM: !d.stripBOM}
		text, next, err := Decode("utf-16le", state, chunk, final, 0)
		if err == nil {
			d.pending = next.Pending
			if text != "" {
				d.stripBOM = false
			}
			return text
		}
	}
	src := append(d.pending, chunk...)
	d.pending = nil
	if len(src) == 0 {
		return ""
	}
	if d.stripBOM && len(src) >= 3 && src[0] == 0xef && src[1] == 0xbb && src[2] == 0xbf {
		src = src[3:]
	}
	d.stripBOM = false

	// Fast path: entire chunk (except optional incomplete trailing UTF-8 sequence)
	// is valid UTF-8 and we have not switched permanently away from UTF-8.
	completeEnd := len(src)
	if !final {
		completeEnd = trailingIncompleteUTF8Start(src)
	}
	if utf8.Valid(src[:completeEnd]) {
		d.pending = append([]byte(nil), src[completeEnd:]...)
		if !final || len(d.pending) == 0 {
			return string(src[:completeEnd])
		}
		// At EOF, any incomplete trailing UTF-8 sequence is invalid as UTF-8, so
		// fall through to decode that remainder via legacy codepage if applicable.
		prefix := string(src[:completeEnd])
		legacyText, _, _ := Decode(d.fallbackName(), State{IgnoreBOM: true}, d.pending, true, 0)
		d.pending = nil
		return prefix + legacyText
	}

	// Mixed or legacy stream: emit valid UTF-8 prefix up to first non-UTF-8 byte,
	// then decode the invalid/legacy segment with the Windows legacy codepage
	// (split cleanly at newline boundaries so subsequent UTF-8 lines still stay UTF-8).
	return d.decodeMixed(src, final)
}

func (d *StreamDecoder) decodeMixed(src []byte, final bool) string {
	var out []byte
	for len(src) > 0 {
		idx := firstInvalidUTF8Index(src, final)
		if idx < 0 {
			out = append(out, src...)
			return string(out)
		}
		if idx == len(src) {
			// Should not happen when invalid index is searched, defensive guard.
			out = append(out, src...)
			return string(out)
		}
		if idx > 0 {
			// Check whether invalid bytes start only because of an incomplete tail.
			tail := src[idx:]
			if !final && len(tail) <= 3 && !utf8.FullRune(tail) && isPotentialUTF8Start(tail) {
				out = append(out, src[:idx]...)
				d.pending = append([]byte(nil), tail...)
				return string(out)
			}
			out = append(out, src[:idx]...)
			src = src[idx:]
		}
		// Consume up to next newline (or end) as a legacy-encoded slice.
		segEnd := len(src)
		if nl := bytes.IndexByte(src, '\n'); nl >= 0 {
			segEnd = nl + 1
		}
		seg := src[:segEnd]
		segFinal := final || segEnd < len(src)
		text, next, err := Decode(d.fallbackName(), State{IgnoreBOM: true}, seg, segFinal, 0)
		if err != nil {
			out = append(out, seg...)
		} else {
			out = append(out, text...)
			if !segFinal && len(next.Pending) > 0 {
				d.pending = next.Pending
				return string(out)
			}
		}
		src = src[segEnd:]
	}
	return string(out)
}

func (d *StreamDecoder) fallbackName() string {
	if d.fallback != "" {
		return d.fallback
	}
	return "gbk"
}

func firstInvalidUTF8Index(b []byte, final bool) int {
	for i := 0; i < len(b); {
		if b[i] < utf8.RuneSelf {
			i++
			continue
		}
		if !utf8.FullRune(b[i:]) {
			if !final && len(b[i:]) <= 3 && isPotentialUTF8Start(b[i:]) {
				return i
			}
			return i
		}
		r, size := utf8.DecodeRune(b[i:])
		if r == utf8.RuneError && size == 1 {
			return i
		}
		i += size
	}
	return -1
}

func isPotentialUTF8Start(tail []byte) bool {
	if len(tail) == 0 {
		return false
	}
	b := tail[0]
	if b < 0xc2 || b > 0xf4 {
		return false
	}
	for _, c := range tail[1:] {
		if c&0xc0 != 0x80 {
			return false
		}
	}
	return true
}

func trailingIncompleteUTF8Start(b []byte) int {
	n := len(b)
	for back := 1; back <= 3 && back <= n; back++ {
		c := b[n-back]
		if c&0xc0 == 0x80 {
			continue
		}
		if c >= 0xc0 && !utf8.FullRune(b[n-back:]) {
			return n - back
		}
		break
	}
	return n
}
