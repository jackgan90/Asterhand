package automation

import (
	"crypto/sha256"
	"crypto/subtle"
	"sync"
)

// Profile is the node's local connection record. It contains no bearer secret.
type Profile struct {
	Version      string `json:"api_version"`
	Node         string `json:"node_id"`
	Actor        string `json:"actor_id"`
	Pipe         string `json:"pipe"`
	Certificate  string `json:"certificate"`
	Installation string `json:"installation,omitempty"`
}

type Access struct {
	mu         sync.RWMutex
	home       string
	localHash  [sha256.Size]byte
	localReady bool
}

func NewAccess(home string) *Access { return &Access{home: home} }
func (a *Access) Authenticate(token string) (Principal, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	hash := sha256.Sum256([]byte(token))
	if len(token) == 64 && a.localReady && subtle.ConstantTimeCompare(hash[:], a.localHash[:]) == 1 {
		return localPrincipal(), nil
	}
	return Principal{}, Fail("forbidden", "The caller does not have the current node user's local credential")
}
