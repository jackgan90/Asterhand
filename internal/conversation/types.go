// Package conversation owns node-local conversation identities and delivery
// reconciliation. Runtime adapters alone own message bodies and native handles.
package conversation

import "github.com/jackgan90/Asterhand/internal/conversation/wire"

// The isolated input host imports only wire and never links the catalog/runtime.
type Target = wire.Target
type Capabilities = wire.Capabilities
type Descriptor = wire.Descriptor
type Conversation = wire.Conversation
type Turn = wire.Turn
type Block = wire.Block
type Message = wire.Message
type Page = wire.Page
type Fragment = wire.Fragment
type Request = wire.Request
type Response = wire.Response
type QueueItem = wire.QueueItem
type QueueState = wire.QueueState
type Error = wire.Error
type Transport = wire.Transport
type Client = wire.Client

const DefaultRole = wire.DefaultRole
const MaxInputBytes = wire.MaxInputBytes
const PageBytes = wire.PageBytes
const FragmentBytes = wire.FragmentBytes
const TurnTimeout = wire.TurnTimeout

func Fail(code, message string) error { return wire.Fail(code, message) }
func PublicError(err error) *Error    { return wire.PublicError(err) }
func ValidID(id string) bool          { return wire.ValidID(id) }
