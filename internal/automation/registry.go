package automation

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackgan90/Asterhand/internal/operations"
)

type Registry struct {
	Node, Instance string
	Journal        operations.Journal
	Authorize      func(string) (Principal, error)
	Context        context.Context
	Now            func() time.Time
	Reconcile      func(operations.Receipt) (operations.Receipt, error)
	definitions    map[string]Definition
	mu             sync.Mutex
	active         map[string]map[string]int
	global         map[string]int
	cursorKey      [32]byte
}

func NewRegistry(node, instance string, journal operations.Journal) *Registry {
	r := &Registry{Node: node, Instance: instance, Journal: journal, Context: context.Background(), Now: time.Now,
		definitions: map[string]Definition{}, active: map[string]map[string]int{}, global: map[string]int{}}
	if _, err := rand.Read(r.cursorKey[:]); err != nil {
		panic(err)
	}
	return r
}

// Register is called during construction, before serving any connections.
func Register[T any](r *Registry, d Definition, target func(T) string, access func(Principal, T) error, handler func(context.Context, Call, T) (Outcome, error)) {
	if _, found := r.definitions[d.Name]; found {
		panic("duplicate automation operation: " + d.Name)
	}
	d.Schema = Schema[T]()
	d.OutputSchema = Schema[Response]()
	d.normalize = func(raw json.RawMessage) (any, error) { return Decode[T](raw) }
	d.target = func(value any) string {
		if target == nil {
			return ""
		}
		return target(value.(T))
	}
	d.access = func(p Principal, value any) error {
		if access == nil {
			return nil
		}
		return access(p, value.(T))
	}
	d.invoke = func(ctx context.Context, call Call, value any) (Outcome, error) { return handler(ctx, call, value.(T)) }
	r.definitions[d.Name] = d
}

func (r *Registry) Definition(name string) (Definition, bool) {
	d, ok := r.definitions[name]
	return d, ok
}

func (r *Registry) Definitions(p Principal) []Definition {
	out := []Definition{}
	for _, d := range r.definitions {
		if p.Role(d.Role) && p.AllowsToolset(d.EffectiveToolset()) {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DescribeData enriches the shared envelope with the operation's result fields.
// CLI help and MCP discovery consume the same object, never separate schemas.
func (r *Registry) DescribeData(name string, schema map[string]any) {
	d, ok := r.definitions[name]
	if !ok {
		panic("schema for unknown operation: " + name)
	}
	d.OutputSchema["properties"].(map[string]any)["data"] = schema
	r.definitions[name] = d
}

func (r *Registry) Dispatch(ctx context.Context, token string, request Request) Response {
	return r.dispatchGuarded(ctx, token, request, nil)
}

func (r *Registry) dispatchGuarded(ctx context.Context, token string, request Request, guard func() error) Response {
	return r.dispatchAuthorized(ctx, func() (Principal, error) { return r.Authorize(token) }, request, guard)
}

// DispatchHuman is for an already authenticated local UI adapter. Network
// automation transports must always use Dispatch with their subject token.
func (r *Registry) DispatchHuman(ctx context.Context, request Request) Response {
	return r.dispatchAuthorized(ctx, func() (Principal, error) {
		return Principal{ID: "human:web", Name: "Local human", Roles: []string{"read", "edit", "execute"}, Scopes: map[string][]string{"node": {"*"}, "commands": {"*"}, "conversations": {"*"}, "workspaces": {"*"}, "workers": {"*"}, "projects": {"*"}}}, nil
	}, request, nil)
}

func (r *Registry) dispatchAuthorized(ctx context.Context, authorize func() (Principal, error), request Request, guard func() error) Response {
	response := Response{Version: APIVersion, Node: r.Node, Instance: r.Instance, ID: request.ID}
	p, err := authorize()
	if err != nil {
		response.Error = PublicError(err)
		return response
	}
	if request.Version != APIVersion {
		response.Error = Fail("version_mismatch", "Unsupported public API version")
		return response
	}
	if request.Node != r.Node {
		response.Error = Fail("wrong_node", "Target node does not match this endpoint")
		return response
	}
	d, found := r.definitions[request.Operation]
	if !found || !p.Role(d.Role) {
		response.Error = Fail("forbidden", "Operation is not available to this subject")
		return response
	}
	if !p.AllowsToolset(d.EffectiveToolset()) {
		response.Error = Fail("toolset_disabled", "This toolset is disabled for the current caller")
		return response
	}
	value, err := d.normalize(request.Arguments)
	if err != nil {
		response.Error = PublicError(err)
		return response
	}
	check := func() error {
		current, err := authorize()
		if err != nil {
			return err
		}
		if current.ID != p.ID || !current.Role(d.Role) {
			return Fail("forbidden", "Authorization was revoked")
		}
		if !current.AllowsToolset(d.EffectiveToolset()) {
			return Fail("toolset_disabled", "This toolset is disabled for the current caller")
		}
		if err = d.access(current, value); err != nil {
			return err
		}
		if guard != nil {
			return guard()
		}
		return nil
	}
	if err = check(); err != nil {
		response.Error = PublicError(err)
		return response
	}
	identity := operations.Identity{Actor: p.ID, Node: r.Node, ID: request.ID, Name: d.Name, Digest: operations.Hash(value), Target: d.target(value)}
	var receipt operations.Receipt
	if d.Write {
		if err = operations.CheckName(request.ID, d.Name); err != nil {
			response.Error = PublicError(err)
			return response
		}
		var fresh bool
		receipt, fresh, err = r.Journal.Begin(identity, r.Now())
		if err != nil {
			response.Error = PublicError(err)
			return response
		}
		response.Receipt = &receipt
		if !fresh {
			if r.Reconcile != nil && !receipt.Terminal() {
				receipt, err = r.Reconcile(receipt)
				if err != nil {
					response.Error = PublicError(err)
					return response
				}
				response.Receipt = &receipt
			}
			return responseForReceipt(response, receipt)
		}
	} else if request.ID != "" {
		response.Error = Fail("invalid_parameters", "Read operations do not accept an operation identity")
		return response
	}
	release, err := r.reserve(p.ID, d)
	if err != nil {
		return r.finish(response, receipt, Outcome{}, err, d.Write)
	}
	defer release()
	if err = ctx.Err(); err != nil {
		return r.finish(response, receipt, Outcome{}, err, d.Write)
	}
	if err = check(); err != nil {
		return r.finish(response, receipt, Outcome{}, err, d.Write)
	}
	callContext := ctx
	if d.Write {
		callContext = r.Context
	} // The node owns an accepted operation, not its observer.
	call := Call{Principal: p, Identity: identity, Authorize: authorize, Check: check}
	call.Run = func(inner context.Context, req Request, extra func() error) Response {
		return r.dispatchAuthorized(inner, authorize, req, func() error {
			if err := check(); err != nil {
				return err
			}
			if extra != nil {
				return extra()
			}
			return nil
		})
	}
	outcome, err := d.invoke(callContext, call, value)
	response = r.finish(response, receipt, outcome, err, d.Write)
	if checkErr := check(); checkErr != nil {
		response.Data, response.Receipt, response.Error = nil, nil, PublicError(checkErr)
		return response
	}
	limit := MaxResponse
	if d.Large {
		limit = MaxRequest
	}
	b, marshalErr := json.Marshal(response)
	if marshalErr != nil || len(b) > limit {
		response.Data = nil
		response.Error = Fail("response_too_large", "Use a smaller page or a bounded content slice; any receipt remains authoritative")
	}
	return response
}

func responseForReceipt(response Response, receipt operations.Receipt) Response {
	response.Receipt = &receipt
	switch receipt.State {
	case "completed":
		response.Error = nil
	case "rejected":
		response.Data = nil
		if response.Error == nil || response.Error.Code != receipt.Code {
			response.Error = Fail(receipt.Code, "The original operation was rejected")
		}
	case "prepared", "unknown":
		response.Error = Fail("result_unknown", "Query the original operation; do not resubmit with a new identity")
	}
	return response
}

func (r *Registry) finish(response Response, receipt operations.Receipt, outcome Outcome, err error, write bool) Response {
	response.Data = outcome.Data
	if err != nil {
		response.Error = PublicError(err)
	}
	if !write {
		return response
	}
	// A domain publication or concurrent observer may already have completed
	// this receipt. Its durable result is authoritative, including lost replies.
	if saved, e := r.Journal.Lookup(receipt.Actor, receipt.Node, receipt.ID); e == nil && saved.Terminal() {
		return responseForReceipt(response, saved)
	}
	receipt.State, receipt.Accepted = "completed", outcome.Accepted
	if outcome.Summary != nil {
		var marshalErr error
		receipt.Result, marshalErr = json.Marshal(outcome.Summary)
		if marshalErr != nil || len(receipt.Result) > operations.ResultLimit {
			receipt.Result = nil
			outcome.Unknown = true
		}
	}
	if err != nil && !outcome.Accepted {
		receipt.State, receipt.Code = "rejected", response.Error.Code
	}
	if outcome.Unknown {
		receipt.State = "unknown"
	}
	if finishErr := r.Journal.Finish(receipt, r.Now()); finishErr != nil {
		// A domain receipt may become terminal between the lookup above and the write.
		if saved, e := r.Journal.Lookup(receipt.Actor, receipt.Node, receipt.ID); e == nil && saved.Terminal() {
			return responseForReceipt(response, saved)
		}
		receipt.State = "unknown"
		response.Error = Fail("result_unknown", "The durable outcome needs reconciliation")
	}
	if receipt.State == "unknown" {
		response.Error = Fail("result_unknown", "Query the original operation before deciding what to do next")
	}
	response.Receipt = &receipt
	return response
}

func (r *Registry) reserve(actor string, d Definition) (func(), error) {
	category, perActor, globalLimit := "read", 8, 32
	if d.Write {
		category, perActor, globalLimit = "write", 4, 16
	}
	if d.Wait {
		category, perActor, globalLimit = "wait", 4, 16
	}
	if d.Control {
		category, perActor, globalLimit = "control", 4, 8
	}
	globalKey := category
	if strings.HasPrefix(actor, "human:") {
		globalKey = "human:" + category
		globalLimit = 8
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active[actor] == nil {
		r.active[actor] = map[string]int{}
	}
	if r.active[actor][category] >= perActor || r.global[globalKey] >= globalLimit {
		return nil, Fail("capacity", "Operation concurrency limit reached")
	}
	r.active[actor][category]++
	r.global[globalKey]++
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.active[actor][category]--
		r.global[globalKey]--
		if r.active[actor]["read"]+r.active[actor]["write"]+r.active[actor]["wait"]+r.active[actor]["control"]+r.active[actor]["batch"] == 0 {
			delete(r.active, actor)
		}
	}, nil
}
