package localcommands

import (
	"errors"
	"strings"

	"golang.org/x/text/encoding"
	textunicode "golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

type DecoderState struct {
	Pending   []byte `json:"pending,omitempty"`
	IgnoreBOM bool   `json:"ignore_bom,omitempty"`
}

func NormalizeLogEncoding(name string) (string, error) {
	switch strings.ToLower(name) {
	case "", "utf-8":
		return "utf-8", nil
	case "gbk", "gb18030":
		return "gbk", nil
	case "big5":
		return "big5", nil
	case "utf-16le":
		return "utf-16le", nil
	}
	return "", problem("invalid_parameters", "err.localcmd.invalid_wire_req")
}

// DecodeLogText uses the same frame checkpoint as the UI's streaming decoder.
// Incomplete code points are carried separately and flushed only at real EOF.
func DecodeLogText(name string, state DecoderState, data []byte, final bool) (string, DecoderState, error) {
	var enc encoding.Encoding
	switch name {
	case "utf-8":
		enc = textunicode.UTF8
	case "gbk", "big5":
	case "utf-16le":
		enc = textunicode.UTF16(textunicode.LittleEndian, textunicode.IgnoreBOM)
	default:
		return "", state, problem("invalid_parameters", "err.localcmd.invalid_wire_req")
	}
	if len(state.Pending) > 3 || len(data) > logBlockSize {
		return "", state, problem("invalid_cursor", "err.localcmd.invalid_log_cursor")
	}
	src := append(append([]byte{}, state.Pending...), data...)
	var text string
	var nIn int
	var err error
	if enc == nil {
		text, nIn, err = decodeLegacy(name, src, final)
	} else {
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
		return "", state, problem("invalid_cursor", "err.localcmd.invalid_log_cursor")
	}
	if text != "" {
		if !state.IgnoreBOM && (name == "utf-8" || name == "utf-16le") {
			text = strings.TrimPrefix(text, "\ufeff")
		}
		state.IgnoreBOM = true
	}
	state.Pending = pending
	return text, state, nil
}

func (r *Registry) LogPage(id, run, step string, cursor int64, before bool) (LogPage, error) {
	if r.managed == nil {
		return LogPage{}, problem("unsupported", "err.localcmd.managed_unavailable")
	}
	return r.managed.readLog(id, run, step, cursor, before)
}
