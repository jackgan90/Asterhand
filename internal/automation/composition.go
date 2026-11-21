package automation

import (
	"encoding/json"

	"github.com/jackgan90/Asterhand/internal/operations"
)

type BatchItem struct {
	Operation string          `json:"operation" description:"Target operation name"`
	Arguments json.RawMessage `json:"arguments" description:"JSON arguments object for the target operation"`
}
type BatchSubmit struct {
	Items   []BatchItem `json:"items" description:"1 to 32 write items to admit"`
	OnError string      `json:"on_error,omitempty" description:"Error policy: \"stop\" (default) or \"continue\""`
}
type BatchGet struct {
	ID     string `json:"batch_id" description:"Batch operation_id returned by batches_submit"`
	Cursor string `json:"cursor,omitempty" description:"Opaque pagination cursor for additional batch items"`
}
type MultiRead struct {
	Items  []BatchItem `json:"items" description:"1 to 100 read items to execute"`
	Cursor string      `json:"cursor,omitempty" description:"Opaque pagination cursor for remaining read items"`
}

// PrepareItem uses exactly the normal decoder and authorization definition.
// It performs no domain call or journal admission.
func (r *Registry) PrepareItem(p Principal, item BatchItem, write bool) (operations.Identity, error) {
	d, ok := r.definitions[item.Operation]
	if !ok || d.Write != write || !p.Role(d.Role) || write && !d.Batch || !write && (d.Wait || d.Name == "read_many" || d.Name == "batches_get") {
		return operations.Identity{}, Fail("invalid_parameters", "Operation is not available in this composition")
	}
	if !p.AllowsToolset(d.EffectiveToolset()) {
		return operations.Identity{}, Fail("toolset_disabled", "Operation toolset is disabled for this caller")
	}
	v, err := d.normalize(item.Arguments)
	if err != nil {
		return operations.Identity{}, err
	}
	if err = d.access(p, v); err != nil {
		return operations.Identity{}, err
	}
	return operations.Identity{Actor: p.ID, Node: r.Node, Name: d.Name, Target: d.target(v), Digest: operations.Hash(v)}, nil
}

func (r *Registry) ReserveBatch(actor string) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[actor] == nil {
		r.active[actor] = map[string]int{}
	}
	if r.active[actor]["batch"] >= 1 || r.global["batch"] >= 4 {
		return nil, Fail("capacity", "At most one active batch per subject and four per node")
	}
	r.active[actor]["batch"]++
	r.global["batch"]++
	return func() { r.mu.Lock(); defer r.mu.Unlock(); r.active[actor]["batch"]--; r.global["batch"]-- }, nil
}
