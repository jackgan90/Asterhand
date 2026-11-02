package localcommands

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os/exec"
	"testing"
)

// The real decoders serve as an independent oracle for persisted checkpoints,
// including BOM state, split characters and interleaved streams.
func TestLogPagesSeedRealDecoders(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for browser decoder conformance")
	}
	random := make([]byte, 512)
	rand.New(rand.NewSource(42)).Read(random)
	samples := map[string][]byte{
		"utf-8":    append(append([]byte{0xef, 0xbb, 0xbf}, []byte("中文😀\ufefftext")...), random...),
		"utf-16le": append([]byte{0xff, 0xfe, 0x2d, 0x4e, 0x87, 0x65, 0x3d, 0xd8, 0x00, 0xde, 0xff, 0xfe, 0x41, 0}, random...),
		"gbk":      bytes.Repeat([]byte{0xd6, 0xd0, 0xce, 0xc4, 0x81, 0x20, 0xff, 0x40, 0x81, 0x40, 0x81, 0x30, 0x81, 0x30}, 20),
		"big5":     bytes.Repeat([]byte{0xa4, 0xa4, 0xa4, 0xe5, 0x81, 0x20, 0x40}, 20),
	}
	for encoding, sample := range samples {
		t.Run(encoding, func(t *testing.T) {
			store := runStore{root: t.TempDir()}
			record := RunRecord{ID: "execution123456789", Command: Command{ID: "command1234567890"}}
			f, err := store.prepare(record)
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range sample {
				for stream := byte(0); stream < 2; stream++ {
					if err = appendLog(f, stream, []byte{b}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			var frames []LogPage
			cursor := int64(0)
			for range len(sample) * 2 {
				page, err := store.read(record, cursor)
				if err != nil || len(page.Chunks) == 0 {
					t.Fatal("read frame", err)
				}
				page.Chunks = page.Chunks[:1]
				frames = append(frames, page)
				cursor += logFrameOverhead + 1
			}
			payload, err := json.Marshal(frames)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(node, "../../tests/helpers/check-log-decoder.mjs", encoding)
			command.Stdin = bytes.NewReader(payload)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("decoder conformance: %v\n%s", err, output)
			}
		})
	}
}

func TestGB18030CheckpointTransitions(t *testing.T) {
	for _, input := range [][]byte{{0x81, 0x30, 0x81, 0x30}, {0xbf, 0x30, 0x0f}, {0x81, 0x30, 0x81, 0x20}} {
		var state logContext
		for i, b := range input {
			state.advance(0, []byte{b})
			want := i + 1
			if i == len(input)-1 {
				want = 0
			}
			if got := int(state[0][1][0] & 3); got != want {
				t.Fatalf("%x byte %d: pending %d want %d", input, i, got, want)
			}
		}
	}
}
