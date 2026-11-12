package automation

import (
	"encoding/json"

	"github.com/jackgan90/Asterhand/internal/operations"
)

type BatchItem struct {
	Operation string          `json:"operation"`
	Arguments json.RawMessage `json:"arguments"`
}
type BatchSubmit struct {
	Items   []BatchItem `json:"items"`
	OnError string      `json:"on_error,omitempty"`
}
type BatchGet struct {
	ID     string `json:"batch_id"`
	Cursor string `json:"cursor,omitempty"`
}
type MultiRead struct {
	Items  []BatchItem `json:"items"`
	Cursor string      `json:"cursor,omitempty"`
}

// PrepareItem uses exactly the normal decoder and authorization definition.
// It performs no domain call or journal admission.
func (r *Registry) PrepareItem(p Principal, item BatchItem, write bool) (operations.Identity, error) {
	d, ok := r.definitions[item.Operation]
	if !ok || d.Write != write || !p.Role(d.Role) || write && !d.Batch || !write && (d.Wait || d.Name == "read.many" || d.Name == "batches.get") {
		return operations.Identity{}, Fail("invalid_parameters", "Operation is not available in this composition")
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
