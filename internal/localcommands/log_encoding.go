package localcommands

import (
	"encoding/binary"
	"os"
	"unicode/utf8"
)

// Each frame checkpoints the unconsumed bytes of both streams for the four
// supported display encodings. Random pages can seed a fresh decoder without
// scanning the log or assuming capture chunks end at character boundaries.
var logEncodings = [...]string{"utf-8", "gbk", "big5", "utf-16le"}

const logContextSize = 2 * 4 * 4
const logHeaderSize = 13 + logContextSize
const logFrameOverhead = logHeaderSize + 4
const logPageFrames = 64
const logPageBytes = 32 * 1024

type logContext [2][4][4]byte // stream, encoding, length + up to 3 pending bytes

type logFile struct {
	*os.File
	context logContext
	nodes   map[byte]*logContext
}

func (c *logContext) advance(stream byte, data []byte) {
	for encoding := range logEncodings {
		state := &c[stream][encoding]
		var buffer [4]byte
		pending := buffer[:int(state[0]&3)]
		previous := len(pending)
		ignoreBOM := state[0] & 0x80
		copy(pending, state[1:])
		for _, b := range data {
			pending = append(pending, b)
			switch encoding {
			case 0:
				for len(pending) > 0 && utf8.FullRune(pending) {
					_, size := utf8.DecodeRune(pending)
					copy(pending, pending[size:])
					pending = pending[:len(pending)-size]
				}
			case 1:
				pending = gb18030Pending(pending)
			case 2:
				if len(pending) == 2 {
					trail := pending[1]
					valid := trail >= 0x40 && trail <= 0x7e || trail >= 0xa1 && trail <= 0xfe
					pending = pending[:0]
					// WHATWG legacy decoders reconsume an invalid ASCII trail.
					if !valid && trail < 0x80 {
						pending = append(pending, trail)
					}
				}
				if len(pending) == 1 && (pending[0] < 0x81 || pending[0] > 0xfe) {
					pending = pending[:0]
				}
			case 3:
				if len(pending) >= 2 {
					unit := binary.LittleEndian.Uint16(pending)
					high := unit >= 0xd800 && unit <= 0xdbff
					if !high {
						copy(pending, pending[2:])
						pending = pending[:len(pending)-2]
					} else if len(pending) == 4 {
						next := binary.LittleEndian.Uint16(pending[2:])
						if next >= 0xd800 && next <= 0xdbff {
							copy(pending, pending[2:])
							pending = pending[:len(pending)-2]
						} else {
							pending = pending[:0]
						}
					}
				}
			}
		}
		*state = [4]byte{}
		if previous+len(data) > len(pending) {
			ignoreBOM = 0x80
		}
		state[0] = byte(len(pending)) | ignoreBOM
		copy(state[1:], pending)
	}
}

// WHATWG GBK uses the GB18030 decoder, including four-byte sequences.
// https://encoding.spec.whatwg.org/#gb18030-decoder
// An invalid third byte consumes the lead and reconsumes the digit and byte;
// an invalid fourth reconsumes digit+lead+byte. Neither leaves a pending tail.
func gb18030Pending(pending []byte) []byte {
	keep := false
	switch len(pending) {
	case 1:
		keep = pending[0] >= 0x81 && pending[0] <= 0xfe
	case 2:
		keep = pending[1] >= 0x30 && pending[1] <= 0x39
	case 3:
		keep = pending[2] >= 0x81 && pending[2] <= 0xfe
	}
	if keep {
		return pending
	}
	return pending[:0]
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
