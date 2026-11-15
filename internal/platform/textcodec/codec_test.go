package textcodec

import (
	"testing"
)

func TestDecodeUTF8AndLegacyEncodings(t *testing.T) {
	// Split 3-byte UTF-8 rune across chunks with BOM.
	bom := []byte{0xef, 0xbb, 0xbf, 0xe4, 0xb8, 0xad}
	var st State
	p1, st, err := Decode("utf-8", st, bom[:4], false, 0)
	if err != nil {
		t.Fatal(err)
	}
	p2, st, err := Decode("utf-8", st, bom[4:], true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p1+p2 != "中" {
		t.Fatalf("utf-8 split decode = %q, want 中", p1+p2)
	}

	// GB18030 4-byte sequence split byte-by-byte.
	gb4 := []byte{0x81, 0x30, 0x89, 0x38} // U+0101 / non-BMP or GB18030 range
	var gSt State
	var gOut string
	for _, b := range gb4 {
		var part string
		part, gSt, err = Decode("gb18030", gSt, []byte{b}, false, 0)
		if err != nil {
			t.Fatal(err)
		}
		gOut += part
	}
	tail, _, err := Decode("gb18030", gSt, nil, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if gOut+tail == "" {
		t.Fatal("expected decoded GB18030 rune")
	}

	// UTF-16LE with BOM.
	u16 := []byte{0xff, 0xfe, 0x2d, 0x4e, 0x87, 0x65} // "中文"
	got16, _, err := Decode("utf-16le", State{}, u16, true, 0)
	if err != nil || got16 != "中文" {
		t.Fatalf("utf-16le decode = %q, err=%v", got16, err)
	}
}

func TestStreamDecoderAutoMixedUTF8AndGBK(t *testing.T) {
	// UTF-8 BOM + UTF-8 Chinese split across boundary.
	d1 := NewStreamDecoder()
	raw := append([]byte{0xef, 0xbb, 0xbf}, []byte("中文UTF8输出\n")...)
	var out string
	for i := 0; i < len(raw); i += 2 {
		end := min(i+2, len(raw))
		out += d1.Write(raw[i:end])
	}
	out += d1.Flush()
	if out != "中文UTF8输出\n" {
		t.Fatalf("utf-8 stream = %q", out)
	}

	// Legacy GBK output ("中文" = D6 D0 CE C4) split mid-character.
	d2 := &StreamDecoder{fallback: "gbk", stripBOM: true}
	gbk := []byte{0xd6, 0xd0, 0xce, 0xc4, '\n'}
	got2 := d2.Write(gbk[:1]) + d2.Write(gbk[1:3]) + d2.Write(gbk[3:]) + d2.Flush()
	if got2 != "中文\n" {
		t.Fatalf("gbk split stream = %q, want 中文\\n", got2)
	}

	// Mixed line: UTF-8 line followed by GBK line followed by UTF-8 line.
	d3 := &StreamDecoder{fallback: "gbk", stripBOM: true}
	mixed := append([]byte("你好\n"), 0xb2, 0xbb, 0xca, 0xc7, '\n') // "你好\n不是\n"
	mixed = append(mixed, []byte("世界\n")...)
	got3 := d3.Write(mixed) + d3.Flush()
	if got3 != "你好\n不是\n世界\n" {
		t.Fatalf("mixed stream = %q", got3)
	}
}
