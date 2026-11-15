package localcommands

import (
	"errors"

	"github.com/jackgan90/Asterhand/internal/platform/textcodec"
)

type DecoderState = textcodec.State

func NormalizeLogEncoding(name string) (string, error) {
	canonical, err := textcodec.NormalizeEncoding(name)
	if err != nil {
		return "", problem("invalid_parameters", "err.localcmd.invalid_wire_req")
	}
	return canonical, nil
}

// DecodeLogText uses the same frame checkpoint as the UI's streaming decoder.
// Incomplete code points are carried separately and flushed only at real EOF.
func DecodeLogText(name string, state DecoderState, data []byte, final bool) (string, DecoderState, error) {
	text, next, err := textcodec.Decode(name, state, data, final, logBlockSize)
	if err != nil {
		if errors.Is(err, textcodec.ErrInvalidEncoding) {
			return "", state, problem("invalid_parameters", "err.localcmd.invalid_wire_req")
		}
		if errors.Is(err, textcodec.ErrInvalidCursor) {
			return "", state, problem("invalid_cursor", "err.localcmd.invalid_log_cursor")
		}
		return "", state, err
	}
	return text, next, nil
}

func (r *Registry) LogPage(id, run, step string, cursor int64, before bool) (LogPage, error) {
	if r.managed == nil {
		return LogPage{}, problem("unsupported", "err.localcmd.managed_unavailable")
	}
	return r.managed.readLog(id, run, step, cursor, before)
}
