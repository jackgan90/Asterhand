package localcommands

import (
	"os"

	"github.com/jackgan90/Asterhand/internal/platform/textcodec"
)

// Each frame checkpoints the unconsumed bytes of both streams for the four
// supported display encodings. Random pages can seed a fresh decoder without
// scanning the log or assuming capture chunks end at character boundaries.
var logEncodings = textcodec.SupportedEncodings

const logContextSize = 2 * 4 * 4
const logHeaderSize = 13 + logContextSize
const logFrameOverhead = logHeaderSize + 4
const logPageFrames = 64
const logPageBytes = 32 * 1024

type logContext [2][4][4]byte // stream, encoding, length + up to 3 pending bytes

type logFile struct {
	*os.File
	nodes map[byte]*logContext
}

func (c *logContext) advance(stream byte, data []byte) {
	for encoding := range logEncodings {
		textcodec.AdvancePending(encoding, &c[stream][encoding], data)
	}
}

func (c logContext) marshal(dst []byte) {
	at := 0
	for _, stream := range c {
		for _, state := range stream {
			copy(dst[at:], state[:])
			at += 4
		}
	}
}

func decodeLogContext(src []byte) (map[string][]LogChunk, error) {
	result := map[string][]LogChunk{}
	for stream, name := range []string{"stdout", "stderr"} {
		for encoding, label := range logEncodings {
			at := (stream*4 + encoding) * 4
			n := int(src[at] & 3)
			if src[at]&0x7c != 0 {
				return nil, problem("invalid", "err.localcmd.invalid_log_cursor")
			}
			if n > 0 || src[at]&0x80 != 0 {
				result[label] = append(result[label], LogChunk{Stream: name, Data: append([]byte{}, src[at+1:at+1+n]...), IgnoreBOM: src[at]&0x80 != 0})
			}
		}
	}
	return result, nil
}
