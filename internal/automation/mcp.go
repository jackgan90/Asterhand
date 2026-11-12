package automation

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"maps"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RunMCP(ctx context.Context, args []string, stderr io.Writer, definitions []Definition) error {
	f := flag.NewFlagSet("mcp", flag.ContinueOnError)
	f.SetOutput(stderr)
	home := f.String("home", config.DefaultHome(), "Existing node data directory")

	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return Fail("invalid_parameters", "Unexpected MCP positional arguments")
	}
	client, err := OpenClient(*home)
	if err != nil {
		return err
	}
	defer client.Close()
	principal, err := client.LocalPrincipal()
	if err != nil {
		return err
	}
	server := NewMCPServer(client, principal, definitions)
	return server.Run(ctx, &mcp.StdioTransport{})
}

type Caller interface {
	Call(context.Context, string, string, json.RawMessage) (Response, error)
}

func NewMCPServer(client Caller, principal Principal, definitions []Definition) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "asterhand", Version: config.Version}, nil)
	for _, d := range definitions {
		if !principal.Role(d.Role) {
			continue
		}
		schema := maps.Clone(d.Schema)
		props := maps.Clone(schema["properties"].(map[string]any))
		required := append([]string{}, schema["required"].([]string)...)
		if d.Write {
			pattern := "^[0-9]{13}_[a-f0-9]{32}$"
			if d.Name == "node.start" {
				pattern = "^start:[0-9]{13}_[a-f0-9]{32}$"
			}
			props["operation_id"] = map[string]any{"type": "string", "pattern": pattern, "description": "Create once with operations.new_id (operation=node.start for the launcher), or offline CLI new-id. Reuse unchanged after disconnection; a new identity is a new side effect."}
			required = append(required, "operation_id")
		}
		schema["properties"], schema["required"] = props, required
		server.AddTool(&mcp.Tool{Name: d.Name, Description: d.Description, InputSchema: schema, OutputSchema: d.OutputSchema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: !d.Write}},
			func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				input := request.Params.Arguments
				if len(input) == 0 {
					input = json.RawMessage("{}")
				}
				if err := checkJSON(input); err != nil {
					return mcpResult(Response{Version: APIVersion, Error: PublicError(err)}), nil
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(input, &fields); err != nil {
					return mcpResult(Response{Version: APIVersion, Error: Fail("invalid_parameters", "Expected an argument object")}), nil
				}
				id := ""
				if d.Write {
					if err := json.Unmarshal(fields["operation_id"], &id); err != nil || id == "" {
						return mcpResult(Response{Version: APIVersion, Error: Fail("invalid_operation_id", "A stable operation_id is required")}), nil
					}
					delete(fields, "operation_id")
				}
				arguments, _ := json.Marshal(fields)
				if _, err := d.normalize(arguments); err != nil {
					return mcpResult(Response{Version: APIVersion, ID: id, Error: PublicError(err)}), nil
				}
				response, err := client.Call(ctx, d.Name, id, arguments)
				if err != nil {
					response = Response{Version: APIVersion, ID: id, Error: PublicError(err)}
				}
				return mcpResult(response), nil
			})
	}
	return server
}

func mcpResult(response Response) *mcp.CallToolResult {
	text := "Completed"
	if response.Error != nil {
		text = response.Error.Code + ": " + response.Error.Message
	} else if response.Receipt != nil {
		text = response.Receipt.State
	}
	return &mcp.CallToolResult{StructuredContent: response, Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: response.Error != nil}
}
