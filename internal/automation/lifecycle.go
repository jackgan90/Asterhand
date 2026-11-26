package automation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackgan90/Asterhand/internal/launch"
	"github.com/jackgan90/Asterhand/internal/operations"
)

func (c *Client) startNode(ctx context.Context, id string, arguments json.RawMessage) (Response, error) {
	actor := c.Profile.Actor
	var check func() error
	if c.callerID != "" {
		verifiedActor, err := c.verifyManagedCaller(ctx, "execute", ToolsetGeneral)
		if err != nil {
			if isManagedAuthError(err) {
				return Response{Version: APIVersion, Node: c.Profile.Node, ID: id, Error: PublicError(err)}, nil
			}
			return Response{}, err
		}
		actor = verifiedActor
		check = func() error {
			currentActor, err := c.verifyManagedCaller(ctx, "execute", ToolsetGeneral)
			if err != nil {
				return err
			}
			if currentActor != actor {
				return Fail("forbidden", "Managed caller identity changed")
			}
			return nil
		}
	} else {
		check = func() error {
			p, err := c.LocalPrincipal()
			if err != nil {
				return err
			}
			if !p.Role("execute") {
				return Fail("forbidden", "Node start requires execute permission")
			}
			return p.Require("node", c.Profile.Node)
		}
	}
	if _, err := Decode[Empty](arguments); err != nil {
		return Response{}, err
	}
	if err := operations.CheckName(id, "node_start"); err != nil {
		return Response{}, err
	}
	if c.callerID == "" {
		if err := check(); err != nil {
			return Response{}, err
		}
	}
	if c.Profile.Installation == "" {
		// An already running development node can acknowledge the operation.
		// Only an explicitly granted installed launcher may start an offline one.
		return c.remoteCall(ctx, "node_start", id, arguments)
	}
	identity := operations.Identity{Actor: actor, Node: c.Profile.Node, ID: id, Name: "node_start", Digest: operations.Hash(Empty{}), Target: c.Profile.Node}
	receipt, err := launch.StartOperation(ctx, c.Profile.Installation, c.home, identity, check)
	response := Response{Version: APIVersion, Node: c.Profile.Node, Instance: "launcher", ID: id}
	if receipt.ID != "" {
		response.Receipt = &receipt
	}
	if err != nil {
		if receipt.ID == "" && c.callerID != "" && !isManagedAuthError(err) {
			return Response{}, err
		}
		if errors.Is(err, launch.ErrStartReceiptRollback) {
			response.Error = Fail("operation_failed", "Pre-launch verification failed and rolling back the prepared start receipt failed")
			return response, nil
		}
		response.Error = PublicError(err)
		if receipt.ID != "" && !receipt.Terminal() {
			response.Error = Fail("result_unknown", "Query the original start identity; a timeout never authorizes another launch")
		}
		return response, nil
	}
	if !receipt.Terminal() {
		response.Error = Fail("result_unknown", "The original launch has no confirmed outcome")
	} else {
		response.Data = json.RawMessage(receipt.Result)
	}
	return response, nil
}

func (c *Client) startReceipt(ctx context.Context, arguments json.RawMessage) (Response, bool, error) {
	v, err := Decode[struct {
		ID string `json:"operation_id"`
	}](arguments)
	if err != nil {
		return Response{}, true, err
	}
	if c.callerID != "" {
		if !strings.HasPrefix(v.ID, "start:") {
			return Response{}, false, nil
		}
		actor, err := c.verifyManagedCaller(ctx, "read", ToolsetGeneral)
		if err != nil {
			if isManagedAuthError(err) {
				return Response{Version: APIVersion, Node: c.Profile.Node, Error: PublicError(err)}, true, nil
			}
			return Response{}, true, err
		}
		receipt, err := launch.ReadStartReceipt(c.home, actor, c.Profile.Node, v.ID)
		if errors.Is(err, operations.ErrMissing) {
			return Response{}, false, nil
		}
		if err != nil {
			return Response{}, true, err
		}
		// Re-verify the same subject and permissions immediately before returning
		// so mid-flight revocation cannot leak the start receipt.
		latestActor, err := c.verifyManagedCaller(ctx, "read", ToolsetGeneral)
		if err != nil {
			if isManagedAuthError(err) {
				return Response{Version: APIVersion, Node: c.Profile.Node, Error: PublicError(err)}, true, nil
			}
			return Response{}, true, err
		}
		if latestActor != actor {
			return Response{Version: APIVersion, Node: c.Profile.Node, Error: Fail("forbidden", "Managed caller authorization changed")}, true, nil
		}
		return Response{Version: APIVersion, Node: c.Profile.Node, Instance: "launcher", Data: receipt}, true, nil
	}
	p, err := c.LocalPrincipal()
	if err != nil {
		return Response{}, true, err
	}
	if !p.Role("read") {
		return Response{}, true, Fail("forbidden", "Receipt reads require read permission")
	}
	receipt, err := launch.ReadStartReceipt(c.home, c.Profile.Actor, c.Profile.Node, v.ID)
	if errors.Is(err, operations.ErrMissing) {
		return Response{}, false, nil
	}
	if err != nil {
		return Response{}, true, err
	}
	if err = p.Require("node", c.Profile.Node); err != nil {
		return Response{}, true, err
	}
	return Response{Version: APIVersion, Node: c.Profile.Node, Instance: "launcher", Data: receipt}, true, nil
}
