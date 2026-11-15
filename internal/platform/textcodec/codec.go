// Package textcodec provides shared WHATWG streaming text decoders (UTF-8,
// UTF-16LE, GBK/GB18030, and Big5) for local command logs and sandboxed scripts.
package textcodec

import (
	"encoding/binary"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands/encodingdata"
	"golang.org/x/text/encoding"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// SupportedEncodings defines the canonical encoding order used by log frames.
var SupportedEncodings = [4]string{"utf-8", "gbk", "big5", "utf-16le"}

var (
	ErrInvalidEncoding = i18n.Err("err.textcodec.invalid_encoding")
	ErrInvalidCursor   = i18n.Err("err.textcodec.invalid_cursor")
)

// State captures carry-over incomplete bytes and BOM state across chunks.
type State struct {
	Pending   []byte `json:"pending,omitempty"`
	IgnoreBOM bool   `json:"ignore_bom,omitempty"`
}

// NormalizeEncoding canonicalizes an encoding label to a supported codec name.
func NormalizeEncoding(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "utf-8", "utf8":
		return "utf-8", nil
	case "gbk", "gb18030", "cp936":
		return "gbk", nil
	case "big5", "cp950":
		return "big5", nil
	case "utf-16le", "utf16le":
		return "utf-16le", nil
	}
	return "", ErrInvalidEncoding
}

// Decode decodes a chunk using the normalized encoding and updates streaming state.
// If maxChunk > 0 and len(data) > maxChunk, ErrInvalidCursor is returned.
func Decode(name string, state State, data []byte, final bool, maxChunk int) (string, State, error) {
	canonical, err := NormalizeEncoding(name)
	if err != nil {
		return "", state, err
	}
	if len(state.Pending) > 3 || (maxChunk > 0 && len(data) > maxChunk) {
		return "", state, ErrInvalidCursor
	}
	src := append(append([]byte{}, state.Pending...), data...)
	var text string
	var nIn int
	var enc encoding.Encoding
	switch canonical {
	case "utf-8":
		enc = textunicode.UTF8
	case "utf-16le":
		enc = textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM)
	case "gbk", "big5":
		text, nIn, err = decodeLegacy(canonical, src, final)
	}
	if enc != nil {
		dst := make([]byte, 3*len(src)+16)
		var nOut int
		nOut, nIn, err = enc.NewDecoder().Transform(dst, src, final)
		text = string(dst[:nOut])
	}
	if err != nil && !errors.Is(err, transform.ErrShortSrc) {
		return "", state, err
	}
	pending := append([]byte{}, src[nIn:]...)
	if len(pending) > 3 {
		return "", state, ErrInvalidCursor
	}
	if text != "" {
		if !state.IgnoreBOM && (canonical == "utf-8" || canonical == "utf-16le") {
			text = strings.TrimPrefix(text, "\ufeff")
		}
		state.IgnoreBOM = true
	}
	state.Pending = pending
	return text, state, nil
}

// AdvancePending advances a packed 4-byte checkpoint state for one of SupportedEncodings.
func AdvancePending(encodingIndex int, state *[4]byte, data []byte) {
	var buffer [4]byte
	pending := buffer[:int(state[0]&3)]
	previous := len(pending)
	ignoreBOM := state[0] & 0x80
	copy(pending, state[1:])
	for _, b := range data {
		pending = append(pending, b)
		pending = stepPending(encodingIndex, pending)
	}
	*state = [4]byte{}
	if previous+len(data) > len(pending) {
		ignoreBOM = 0x80
	}
	state[0] = byte(len(pending)) | ignoreBOM
	copy(state[1:], pending)
}

func stepPending(encodingIndex int, pending []byte) []byte {
	switch encodingIndex {
	case 0: // utf-8
		for len(pending) > 0 && utf8.FullRune(pending) {
			_, size := utf8.DecodeRune(pending)
			copy(pending, pending[size:])
			pending = pending[:len(pending)-size]
		}
	case 1: // gbk / gb18030
		pending = GB18030Pending(pending)
	case 2: // big5
		if len(pending) == 2 {
			trail := pending[1]
			valid := trail >= 0x40 && trail <= 0x7e || trail >= 0xa1 && trail <= 0xfe
			pending = pending[:0]
			if !valid && trail < 0x80 {
				pending = append(pending, trail)
			}
		}
		if len(pending) == 1 && (pending[0] < 0x81 || pending[0] > 0xfe) {
			pending = pending[:0]
		}
	case 3: // utf-16le
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
	return pending
}

// GB18030Pending trims a candidate lead buffer according to the WHATWG GB18030 decoder.
func GB18030Pending(pending []byte) []byte {
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

func encodingPoint(table []byte, index int) rune {
	if index < 0 || index >= len(table)/4 {
		return 0
	}
	return rune(binary.LittleEndian.Uint32(table[index*4:]))
}

func gbRangePoint(pointer int) rune {
	if pointer > 39419 && pointer < 189000 || pointer > 1237575 {
		return 0
	}
	if pointer == 7457 {
		return '\ue7c7'
	}
	i := sort.Search(len(encodingdata.GBRanges)/8, func(i int) bool {
		return int(binary.LittleEndian.Uint32(encodingdata.GBRanges[i*8:])) > pointer
	}) - 1
	if i < 0 {
		return 0
	}
	return rune(int(binary.LittleEndian.Uint32(encodingdata.GBRanges[i*8+4:])) + pointer - int(binary.LittleEndian.Uint32(encodingdata.GBRanges[i*8:])))
}

func decodeLegacy(name string, src []byte, final bool) (string, int, error) {
	var out strings.Builder
	for i := 0; i < len(src); {
		first := src[i]
		if first < 0x80 {
			out.WriteByte(first)
			i++
			continue
		}
		if name == "gbk" && first == 0x80 {
			out.WriteRune('€')
			i++
			continue
		}
		if first < 0x81 || first > 0xfe {
			out.WriteRune('\ufffd')
			i++
			continue
		}
		if i+1 == len(src) {
			if !final {
				return out.String(), i, transform.ErrShortSrc
			}
			out.WriteRune('\ufffd')
			return out.String(), len(src), nil
		}
		second := src[i+1]
		if name == "gbk" && second >= 0x30 && second <= 0x39 {
			text, nextIdx, needMore := decodeGB18030Four(&out, src, i, first, second, final)
			if needMore {
				return text, nextIdx, transform.ErrShortSrc
			}
			i = nextIdx
			continue
		}
		point, step := decodeLegacyPair(name, &out, first, second)
		if point != 0 {
			out.WriteRune(point)
			i += 2
			continue
		}
		if step > 0 {
			i += step
			continue
		}
		out.WriteRune('\ufffd')
		i++
		if second >= 0x80 {
			i++
		}
	}
	return out.String(), len(src), nil
}

func decodeGB18030Four(out *strings.Builder, src []byte, i int, first, second byte, final bool) (string, int, bool) {
	if i+2 == len(src) {
		if !final {
			return out.String(), i, true
		}
		out.WriteRune('\ufffd')
		return out.String(), len(src), false
	}
	third := src[i+2]
	if third < 0x81 || third > 0xfe {
		out.WriteRune('\ufffd')
		return "", i + 1, false
	}
	if i+3 == len(src) {
		if !final {
			return out.String(), i, true
		}
		out.WriteRune('\ufffd')
		return out.String(), len(src), false
	}
	fourth := src[i+3]
	if fourth < 0x30 || fourth > 0x39 {
		out.WriteRune('\ufffd')
		return "", i + 1, false
	}
	point := gbRangePoint(((int(first)-0x81)*10+int(second)-0x30)*1260 + (int(third)-0x81)*10 + int(fourth) - 0x30)
	if point == 0 {
		point = '\ufffd'
	}
	out.WriteRune(point)
	return "", i + 4, false
}

func decodeLegacyPair(name string, out *strings.Builder, first, second byte) (rune, int) {
	if name == "gbk" {
		if second >= 0x40 && second <= 0x7e || second >= 0x80 && second <= 0xfe {
			offset := 0x40
			if second >= 0x7f {
				offset = 0x41
			}
			return encodingPoint(encodingdata.GBIndex, (int(first)-0x81)*190+int(second)-offset), 0
		}
		return 0, 0
	}
	if second >= 0x40 && second <= 0x7e || second >= 0xa1 && second <= 0xfe {
		offset := 0x40
		if second >= 0x7f {
			offset = 0x62
		}
		pointer := (int(first)-0x81)*157 + int(second) - offset
		special := map[int]string{1133: "Ê̄", 1135: "Ê̌", 1164: "ê̄", 1166: "ê̌"}[pointer]
		if special != "" {
			out.WriteString(special)
			return 0, 2
		}
		return encodingPoint(encodingdata.Big5Index, pointer), 0
	}
	return 0, 0
}
