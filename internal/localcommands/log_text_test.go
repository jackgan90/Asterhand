package localcommands

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os/exec"
	"testing"
)

func TestPublicTextMatchesBrowserDecoder(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for decoder comparison")
	}
	random := make([]byte, 4096)
	rand.New(rand.NewSource(8403)).Read(random)
	for _, encoding := range logEncodings {
		samples := [][]byte{random, {0xef, 0xbb, 0xbf, 0xf0, 0x9f, 0x98, 0x80, 0xe4}, {0xff, 0xfe, 0x3d, 0xd8, 0x00, 0xde, 0x41}, {0x81, 0x30, 0x81, 0x30, 0x81, 0x30, 0x81, 0x20}, {0x88, 0x62, 0x88, 0x64, 0x88, 0xa3, 0x88, 0xa5}}
		for index, sample := range samples {
			var state DecoderState
			var text string
			for _, b := range sample {
				part, next, err := DecodeLogText(encoding, state, []byte{b}, false)
				if err != nil {
					t.Fatal(err)
				}
				text += part
				state = next
			}
			part, _, err := DecodeLogText(encoding, state, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			text += part
			body, _ := json.Marshal(map[string]any{"encoding": encoding, "bytes": sample})
			cmd := exec.Command(node, "--input-type=module", "-e", `import {createLogTextDecoder} from './internal/quickinput/assets/legacy-log-decoder.mjs';let s='';process.stdin.on('data',c=>s+=c);process.stdin.on('end',()=>{let x=JSON.parse(s);process.stdout.write(JSON.stringify(createLogTextDecoder(x.encoding).decode(Buffer.from(x.bytes,'base64'))));});`)
			cmd.Dir = "../.."
			cmd.Stdin = bytes.NewReader(body)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v %s", err, out)
			}
			var want string
			if err = json.Unmarshal(out, &want); err != nil {
				t.Fatal(err)
			}
			if text != want {
				gotRunes, wantRunes := []rune(text), []rune(want)
				at := 0
				for at < len(gotRunes) && at < len(wantRunes) && gotRunes[at] == wantRunes[at] {
					at++
				}
				t.Fatalf("%s sample %d mismatch at rune %d: got %q want %q", encoding, index, at, string(gotRunes[max(0, at-4):min(len(gotRunes), at+8)]), string(wantRunes[max(0, at-4):min(len(wantRunes), at+8)]))
			}
		}
	}
}
