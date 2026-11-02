package hostipc

import commandswire "github.com/jackgan90/Asterhand/internal/localcommands/wire"

// WindowAnchor is a physical desktop point, never an OS handle. The input
// adapter captures the center of its monitor when the user requests logs.
type WindowAnchor struct {
	X int32 `json:"x"`
	Y int32 `json:"y"`
}

func (a WindowAnchor) Valid() bool {
	const limit = 1 << 24
	return a.X >= -limit && a.X <= limit && a.Y >= -limit && a.Y <= limit
}

// LogWindowEvent carries bounded diagnostics, never command text or output.
type LogWindowEvent struct {
	ID        string `json:"id"`
	Stage     string `json:"stage"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Code      string `json:"code,omitempty"`
}

func (e LogWindowEvent) Valid() bool {
	if !commandswire.ValidID(e.ID) || e.ElapsedMS < 0 || e.ElapsedMS > 3600000 || len(e.Code) > 96 {
		return false
	}
	switch e.Stage {
	case "window_shown", "environment_ready", "controller_ready", "page_ready", "page_initialized", "initialization_failed", "hidden", "retired", "profile_cleanup_failed":
	default:
		return false
	}
	for _, c := range e.Code {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == ':' || c == '-' {
			continue
		}
		return false
	}
	return true
}
