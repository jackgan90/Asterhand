package automation

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"

	"path/filepath"

	"time"
)

type Client struct {
	home    string
	Profile Profile
	token   string
	http    *http.Client
}

func OpenClient(home string) (*Client, error) {
	abs, err := filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	return openLocalClient(abs)
}
func (c *Client) LocalPrincipal() (Principal, error) { return localPrincipal(), nil }

func newTokenClient(profile Profile, secret string) (*Client, error) {
	tlsConfig, err := pinnedTLS(profile)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{MaxConnsPerHost: 16, MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second,
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := dialLocal(ctx, profile.Pipe)
			if err != nil {
				return nil, err
			}
			secured := tls.Client(conn, tlsConfig)
			if err = secured.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return secured, nil
		}}
	return &Client{Profile: profile, token: secret, http: &http.Client{Transport: transport, Timeout: 40 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Call(ctx context.Context, operation, id string, arguments json.RawMessage) (Response, error) {
	if operation == "node.start" {
		return c.startNode(ctx, id, arguments)
	}
	if operation == "operations.get" && c.home != "" {
		if id != "" {
			return Response{}, Fail("invalid_parameters", "Read operations do not accept an operation identity")
		}
		if result, handled, err := c.startReceipt(arguments); handled {
			return result, err
		}
	}
	return c.remoteCall(ctx, operation, id, arguments)
}

func (c *Client) remoteCall(ctx context.Context, operation, id string, arguments json.RawMessage) (Response, error) {
	request := Request{Version: APIVersion, Node: c.Profile.Node, Operation: operation, ID: id, Arguments: arguments}
	b, err := json.Marshal(request)
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://asterhand.local/v1/call", bytes.NewReader(b))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(req)
	if err != nil {
		code := "node_unavailable"
		if id != "" {
			code = "result_unknown"
		}
		return Response{}, Fail(code, "The original node could not be reached; retain the same operation identity for reconciliation")
	}
	defer response.Body.Close()
	b, err = io.ReadAll(io.LimitReader(response.Body, MaxRequest+1))
	if err != nil || len(b) > MaxRequest {
		return Response{}, Fail("invalid_response", "The node response exceeded its budget")
	}
	var result Response
	if err = json.Unmarshal(b, &result); err != nil {
		return Response{}, Fail("invalid_response", "The node returned invalid JSON")
	}
	if response.StatusCode == http.StatusForbidden {
		return result, nil
	}
	if result.Version != APIVersion || result.Node != c.Profile.Node || result.ID != id || result.Instance == "" {
		return Response{}, Fail("invalid_response", "The node response did not match the request")
	}
	return result, nil
}
