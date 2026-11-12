package operations

import "encoding/json"

// State holds bounded lifecycle metadata for composed operations. Submitted
// graphs, messages and task text are never copied here.
type State struct {
	Identity
	Instance string          `json:"instance_id"`
	Updated  int64           `json:"updated_ms"`
	Complete bool            `json:"complete"`
	Data     json.RawMessage `json:"data"`
}

const StateBytes = 16 << 10
const StateLimit = 1000
