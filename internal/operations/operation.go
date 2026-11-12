// Package operations defines durable operation identities, independently of
// transports and domain implementations. It never stores submitted document text.
package operations

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const AcceptanceWindow = 10 * time.Minute
const FutureSkew = 30 * time.Second
const Retention = 30 * 24 * time.Hour
const ReceiptLimit = 100000
const ReceiptBytes = 64 << 20
const ResultLimit = 8192

var ErrMissing = errors.New("operation_not_found")
var ErrConflict = errors.New("operation_conflict")
var ErrExpired = errors.New("operation_expired")
var ErrInvalid = errors.New("invalid_operation_id")
var ErrCapacity = errors.New("operation_capacity")

// NewID includes an immutable UTC timestamp. Replacing the timestamp is a new
// operation, never a way to retry an expired one.
func NewID(now time.Time) string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		panic(err)
	}
	return strconv.FormatInt(now.UnixMilli(), 10) + "_" + hex.EncodeToString(random[:])
}

// Start identities occupy a reserved namespace because an offline launcher
// cannot consult the running node's journal. Neither journal accepts the other's
// identities, so an explicitly reused ID cannot admit two different effects.
func NewIDFor(name string, now time.Time) string {
	id := NewID(now)
	if name == "node.start" {
		return "start:" + id
	}
	return id
}

func CheckName(id, name string) error {
	if strings.HasPrefix(id, "start:") != (name == "node.start") {
		return ErrInvalid
	}
	_, err := IDTime(id)
	return err
}

func IDTime(id string) (time.Time, error) {
	id = strings.TrimPrefix(id, "start:")
	parts := strings.Split(id, "_")
	if len(parts) != 2 || len(parts[0]) != 13 || len(parts[1]) != 32 {
		return time.Time{}, ErrInvalid
	}
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || n < 0 {
		return time.Time{}, ErrInvalid
	}
	if _, err = hex.DecodeString(parts[1]); err != nil || strings.ToLower(parts[1]) != parts[1] {
		return time.Time{}, ErrInvalid
	}
	return time.UnixMilli(n), nil
}

func CheckFresh(id string, now time.Time) error {
	t, err := IDTime(id)
	if err != nil {
		return err
	}
	if now.Sub(t) > AcceptanceWindow || t.Sub(now) > FutureSkew {
		return ErrExpired
	}
	return nil
}

func Hash(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("operation hash: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type Identity struct {
	Actor  string `json:"actor"`
	Node   string `json:"node_id"`
	ID     string `json:"operation_id"`
	Name   string `json:"operation"`
	Digest string `json:"digest"`
	Target string `json:"target,omitempty"`
}

func (i Identity) Key() string      { return Hash([]string{i.Actor, i.Node, i.ID}) }
func (i Identity) NativeID() string { return i.Key()[:32] }

// Receipt contains references and outcome metadata only. Payloads such as
// prompts, graphs and messages belong exclusively to the owning domain.
type Receipt struct {
	Identity
	State    string          `json:"state"`
	Accepted bool            `json:"accepted"`
	Result   json.RawMessage `json:"result,omitempty"`
	Code     string          `json:"code,omitempty"`
	Created  int64           `json:"created_ms"`
	Updated  int64           `json:"updated_ms"`
}

func (r Receipt) Terminal() bool { return r.State == "completed" || r.State == "rejected" }

// Journal reserves capacity before a domain is called. Begin returns an old
// receipt before freshness checks. An interrupted prepared receipt is unknown;
// it is never permission to execute the operation again.
type Journal interface {
	Begin(Identity, time.Time) (Receipt, bool, error)
	Lookup(actor, node, id string) (Receipt, error)
	Finish(Receipt, time.Time) error
	Pending() ([]Receipt, error)
}

type Publication struct {
	Identity
	Result json.RawMessage `json:"result,omitempty"`
}
