package localcommands

import (
	_ "embed"
	"encoding/binary"
	"sort"
	"strings"

	"golang.org/x/text/transform"
)

// WHATWG Encoding snapshot and BSD-3-Clause attribution: encodingdata/README.md.
//
//go:embed encodingdata/index-gb18030.bin
var gbIndex []byte

//go:embed encodingdata/index-gb18030-ranges.bin
var gbRanges []byte

//go:embed encodingdata/index-big5.bin
var big5Index []byte

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
	i := sort.Search(len(gbRanges)/8, func(i int) bool { return int(binary.LittleEndian.Uint32(gbRanges[i*8:])) > pointer }) - 1
	if i < 0 {
		return 0
	}
	return rune(int(binary.LittleEndian.Uint32(gbRanges[i*8+4:])) + pointer - int(binary.LittleEndian.Uint32(gbRanges[i*8:])))
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
			if i+2 == len(src) {
				if !final {
					return out.String(), i, transform.ErrShortSrc
				}
				out.WriteRune('\ufffd')
				return out.String(), len(src), nil
			}
			third := src[i+2]
			if third < 0x81 || third > 0xfe {
				out.WriteRune('\ufffd')
				i++
				continue
			}
			if i+3 == len(src) {
				if !final {
					return out.String(), i, transform.ErrShortSrc
				}
				out.WriteRune('\ufffd')
				return out.String(), len(src), nil
			}
			fourth := src[i+3]
			if fourth < 0x30 || fourth > 0x39 {
				out.WriteRune('\ufffd')
				i++
				continue
			}
			point := gbRangePoint(((int(first)-0x81)*10+int(second)-0x30)*1260 + (int(third)-0x81)*10 + int(fourth) - 0x30)
			if point == 0 {
				point = '\ufffd'
			}
			out.WriteRune(point)
			i += 4
			continue
		}
		point := rune(0)
		if name == "gbk" {
			if second >= 0x40 && second <= 0x7e || second >= 0x80 && second <= 0xfe {
				offset := 0x40
				if second >= 0x7f {
					offset = 0x41
				}
				point = encodingPoint(gbIndex, (int(first)-0x81)*190+int(second)-offset)
			}
		} else {
			if second >= 0x40 && second <= 0x7e || second >= 0xa1 && second <= 0xfe {
				offset := 0x40
				if second >= 0x7f {
					offset = 0x62
				}
				pointer := (int(first)-0x81)*157 + int(second) - offset
				special := map[int]string{1133: "Ê̄", 1135: "Ê̌", 1164: "ê̄", 1166: "ê̌"}[pointer]
				if special != "" {
					out.WriteString(special)
					i += 2
					continue
				}
				point = encodingPoint(big5Index, pointer)
			}
		}
		if point != 0 {
			out.WriteRune(point)
			i += 2
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
