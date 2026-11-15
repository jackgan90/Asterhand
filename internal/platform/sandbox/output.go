package sandbox

import (
	"fmt"
	"sync"
	"unicode/utf8"
)

// OutputBuffer stores up to maxBytes of valid UTF-8 script output (preserving a
// bounded head + tail window when output exceeds capacity) and supports offset reads.
type OutputBuffer struct {
	mu           sync.Mutex
	maxBytes     int
	headQuota    int
	head         []byte
	tail         []byte
	droppedBytes int64
	totalBytes   int64
}

// NewOutputBuffer creates a bounded head+tail UTF-8 output buffer.
func NewOutputBuffer(maxBytes int) *OutputBuffer {
	if maxBytes <= 0 {
		maxBytes = MaxOutputSessionBytes
	}
	headQuota := maxBytes / 4
	if headQuota < 1024 && maxBytes >= 4096 {
		headQuota = 1024
	}
	return &OutputBuffer{maxBytes: maxBytes, headQuota: headQuota}
}

// Append writes decoded valid UTF-8 text into the bounded buffer.
func (b *OutputBuffer) Append(text string) {
	if text == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	raw := []byte(text)
	b.totalBytes += int64(len(raw))
	if b.droppedBytes == 0 && len(b.head)+len(b.tail)+len(raw) <= b.maxBytes {
		b.head = append(b.head, raw...)
		return
	}
	// Promote excess bytes beyond headQuota into tail, keeping rune boundaries intact.
	if b.droppedBytes == 0 && len(b.head) > b.headQuota {
		cut := alignUTF8Right(b.head, b.headQuota)
		b.tail = append(b.tail, b.head[cut:]...)
		b.head = append([]byte(nil), b.head[:cut]...)
	} else if len(b.head) < b.headQuota {
		need := min(len(raw), b.headQuota-len(b.head))
		cut := alignUTF8Left(raw, need)
		b.head = append(b.head, raw[:cut]...)
		raw = raw[cut:]
	}
	b.tail = append(b.tail, raw...)
	tailCap := b.maxBytes - len(b.head) - 64
	if tailCap < 256 {
		tailCap = 256
	}
	if len(b.tail) > tailCap {
		excess := len(b.tail) - tailCap
		cut := alignUTF8LeftForward(b.tail, excess)
		b.droppedBytes += int64(cut)
		b.tail = append([]byte(nil), b.tail[cut:]...)
	}
}

// Materialize renders the buffered head, optional truncation marker, and tail.
func (b *OutputBuffer) Materialize() (text string, totalBytes int64, truncated bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.droppedBytes == 0 {
		return string(b.head), b.totalBytes, false
	}
	marker := fmt.Sprintf("\n...[truncated %d bytes]...\n", b.droppedBytes)
	out := make([]byte, 0, len(b.head)+len(marker)+len(b.tail))
	out = append(out, b.head...)
	out = append(out, marker...)
	out = append(out, b.tail...)
	return string(out), b.totalBytes, true
}

// ReadSlice returns up to limit valid UTF-8 bytes starting from logical offset
// within the materialized buffer.
func (b *OutputBuffer) ReadSlice(offset int64, limit int) OutputSlice {
	if limit <= 0 || limit > MaxOutputPollBytes {
		limit = MaxOutputPollBytes
	}
	full, total, truncated := b.Materialize()
	raw := []byte(full)
	if offset < 0 {
		offset = 0
	}
	if offset >= int64(len(raw)) {
		return OutputSlice{NextOffset: int64(len(raw)), TotalBytes: total, Truncated: truncated}
	}
	start := alignUTF8LeftForward(raw, int(offset))
	end := min(len(raw), start+limit)
	if end < len(raw) {
		end = alignUTF8Left(raw, end)
	}
	if end <= start && start < len(raw) {
		_, size := utf8.DecodeRune(raw[start:])
		if size <= 0 {
			size = 1
		}
		end = min(len(raw), start+size)
	}
	return OutputSlice{
		Output:     string(raw[start:end]),
		NextOffset: int64(end),
		TotalBytes: total,
		Truncated:  truncated,
	}
}

func alignUTF8Left(b []byte, limit int) int {
	if limit >= len(b) {
		return len(b)
	}
	if limit <= 0 {
		return 0
	}
	pos := limit
	for pos > 0 && !utf8.RuneStart(b[pos]) {
		pos--
	}
	return pos
}

func alignUTF8Right(b []byte, target int) int {
	return alignUTF8Left(b, min(target, len(b)))
}

func alignUTF8LeftForward(b []byte, offset int) int {
	if offset <= 0 {
		return 0
	}
	if offset >= len(b) {
		return len(b)
	}
	pos := offset
	for pos < len(b) && !utf8.RuneStart(b[pos]) {
		pos++
	}
	return pos
}
