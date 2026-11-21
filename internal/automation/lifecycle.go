package automation

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackgan90/Asterhand/internal/launch"
	"github.com/jackgan90/Asterhand/internal/operations"
)

func (c *Client) startNode(ctx context.Context, id string, arguments json.RawMessage) (Response, error) {
	if _, err := Decode[Empty](arguments); err != nil {
		return Response{}, err
	}
	if err := operations.CheckName(id, "node_start"); err != nil {
		return Response{}, err
	}
	check := func() error {
		p, err := c.LocalPrincipal()
		if err != nil {
			return err
		}
		if !p.Role("execute") {
			return Fail("forbidden", "Node start requires execute permission")
		}
		return p.Require("node", c.Profile.Node)
	}
	if err := check(); err != nil {
		return Response{}, err
	}
	if c.Profile.Installation == "" {
		// An already running development node can acknowledge the operation.
		// Only an explicitly granted installed launcher may start an offline one.
		return c.remoteCall(ctx, "node_start", id, arguments)
	}
	identity := operations.Identity{Actor: c.Profile.Actor, Node: c.Profile.Node, ID: id, Name: "node_start", Digest: operations.Hash(Empty{}), Target: c.Profile.Node}
	receipt, err := launch.StartOperation(ctx, c.Profile.Installation, c.home, identity, check)
	response := Response{Version: APIVersion, Node: c.Profile.Node, Instance: "launcher", ID: id}
	if receipt.ID != "" {
		response.Receipt = &receipt
	}
	if err != nil {
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

func (c *Client) startReceipt(arguments json.RawMessage) (Response, bool, error) {
	v, err := Decode[struct {
		ID string `json:"operation_id"`
	}](arguments)
	if err != nil {
		return Response{}, true, err
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
