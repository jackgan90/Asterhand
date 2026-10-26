package hostipc

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestFramesRejectOversizeInvalidUnicodeAndUnknownCapability(t *testing.T) {
	var frame bytes.Buffer
	text := strings.Repeat("\x00", MaxTextBytes)
	m := Message{Version: Version, Session: "session-123456789", Kind: Edit, Epoch: 1, Sequence: 1, Text: text}
	if err := Write(&frame, m, true); err != nil {
		t.Fatal(err)
	}
	decoded, err := Read(&frame, true)
	if err != nil || decoded.Text != text {
		t.Fatal("escaped maximum draft did not roundtrip", err)
	}
	for _, body := range []string{
		`{"version":1,"session":"session-123456789","kind":"execute-command"}`,
		`{"version":1,"session":"session-123456789","kind":"edit","sequence":1,"epoch":1,"text":"\ud800"}`,
		`{"version":1,"session":"session-123456789","kind":"state","unknown":1}`,
		`{"version":1,"session":"session-123456789","kind":"state","home":"C:/private"}`,
	} {
		frame.Reset()
		_ = binary.Write(&frame, binary.LittleEndian, uint32(len(body)))
		frame.WriteString(body)
		if _, err := Read(&frame, true); err == nil {
			t.Fatalf("accepted invalid frame: %s", body)
		}
	}
	frame.Reset()
	_ = binary.Write(&frame, binary.LittleEndian, uint32(MaxFrameBytes+1))
	if _, err := Read(&frame, true); err == nil {
		t.Fatal("oversized frame accepted before body")
	}
	m.Text = strings.Repeat("a", MaxTextBytes+1)
	if Write(&frame, m, true) == nil {
		t.Fatal("oversized draft accepted")
	}
	m.Text, m.Kind = "", Toggle
	if Write(&frame, m, true) == nil {
		t.Fatal("host requested a privileged command")
	}
}
