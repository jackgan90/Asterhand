// Package hostipc defines the closed, bounded protocol of the input-only host.
// It has no operating system, browser, node, or task execution dependencies.
package hostipc

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

const Version = 1
const MaxFrameBytes = 128 * 1024
const MaxTextBytes = 16000

type Kind string

const (
	Initialize   Kind = "initialize"
	Ready        Kind = "ready"
	Toggle       Kind = "toggle"
	Theme        Kind = "theme"
	Snapshot     Kind = "snapshot"
	SnapshotDone Kind = "snapshot_done"
	Edit         Kind = "edit"
	Accepted     Kind = "accepted"
	State        Kind = "state"
	Failure      Kind = "failure"
	Quit         Kind = "quit"
)

// Message contains only input-surface capabilities. Request is a command ID;
// Sequence is an editor revision within one Session. Revision belongs to the
// parent draft, and Epoch invalidates edits after an explicit draft reset.
type Message struct {
	Version    int    `json:"version"`
	Session    string `json:"session"`
	Kind       Kind   `json:"kind"`
	Request    uint64 `json:"request,omitempty"`
	Sequence   uint64 `json:"sequence,omitempty"`
	Epoch      uint64 `json:"epoch,omitempty"`
	Revision   uint64 `json:"revision,omitempty"`
	Text       string `json:"text,omitempty"`
	Theme      string `json:"theme,omitempty"`
	Home       string `json:"home,omitempty"`
	AppVersion string `json:"app_version,omitempty"`
	Source     string `json:"source,omitempty"`
	Runtime    string `json:"runtime,omitempty"`
	Code       string `json:"code,omitempty"`
	Visible    bool   `json:"visible,omitempty"`
	Recovered  bool   `json:"recovered,omitempty"`
}

var ErrProtocol = errors.New("invalid input host protocol")

func (m Message) Validate(fromHost bool) error {
	if m.Version != Version || len(m.Session) < 16 || len(m.Session) > 64 || !ValidText(m.Text) {
		return ErrProtocol
	}
	if len(m.Home) > 32768 || len(m.AppVersion) > 64 || len(m.Source) > 64 || len(m.Runtime) > 64 || len(m.Code) > 64 {
		return ErrProtocol
	}
	var allowed bool
	if fromHost {
		switch m.Kind {
		case Ready:
			allowed = m.AppVersion != "" && m.Source != "" && m.Runtime != ""
		case Edit:
			allowed = m.Epoch != 0 && m.Sequence != 0
		case SnapshotDone:
			allowed = m.Request != 0
		case State, Failure:
			allowed = true
		}
	} else {
		switch m.Kind {
		case Initialize:
			allowed = m.Home != "" && m.AppVersion != "" && m.Source != "" && m.Epoch != 0 && validTheme(m.Theme)
		case Toggle, Snapshot:
			allowed = m.Request != 0
		case Theme:
			allowed = m.Request != 0 && validTheme(m.Theme)
		case Accepted:
			allowed = m.Epoch != 0 && m.Sequence != 0
		case Quit:
			allowed = true
		}
	}
	if !allowed {
		return ErrProtocol
	}
	// Reject fields outside the capability that owns them; unused data must not
	// become an accidental extension to the privileged parent's protocol.
	if m.Kind != Initialize && m.Home != "" {
		return ErrProtocol
	}
	if m.Kind != Initialize && m.Recovered {
		return ErrProtocol
	}
	if m.Kind != Initialize && m.Kind != Ready && (m.AppVersion != "" || m.Source != "") {
		return ErrProtocol
	}
	if m.Kind != Initialize && m.Kind != Edit && m.Text != "" {
		return ErrProtocol
	}
	if m.Kind != Initialize && m.Kind != Theme && m.Theme != "" {
		return ErrProtocol
	}
	if m.Kind != Ready && m.Runtime != "" {
		return ErrProtocol
	}
	return nil
}

func validTheme(value string) bool { return value == "star" || value == "frost" }

func ValidText(text string) bool {
	return len(text) <= MaxTextBytes && utf8.ValidString(text) && !bytes.ContainsRune([]byte(text), '\r')
}

func Read(r io.Reader, fromHost bool) (Message, error) {
	var message Message
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return message, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > MaxFrameBytes {
		return message, ErrProtocol
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(r, data); err != nil {
		return message, err
	}
	if !utf8.Valid(data) || !PairedSurrogates(string(data)) {
		return message, ErrProtocol
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&message); err != nil {
		return Message{}, ErrProtocol
	}
	if d.Decode(new(any)) != io.EOF {
		return Message{}, ErrProtocol
	}
	return message, message.Validate(fromHost)
}

// Write requires one writer per pipe; callers serialize through a bounded queue.
func Write(w io.Writer, m Message, fromHost bool) error {
	if err := m.Validate(fromHost); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil || len(data) > MaxFrameBytes {
		return ErrProtocol
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(data)))
	for _, part := range [][]byte{header[:], data} {
		for len(part) != 0 {
			n, err := w.Write(part)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}
