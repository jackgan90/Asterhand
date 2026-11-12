package automation

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/operations"
)

type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("automation exited with status %d", e.Code) }

func ErrorExit(code string) int {
	switch code {
	case "invalid_parameters", "invalid_operation_id", "invalid_cursor", "version_mismatch":
		return 2
	case "forbidden", "authorization_required":
		return 3
	case "conflict", "operation_conflict", "name_conflict", "lease_revoked", "lease_expired":
		return 4
	case "result_unknown", "submission_pending":
		return 5
	case "capacity", "operation_capacity", "busy":
		return 6
	case "node_unavailable", "instance_changed":
		return 7
	default:
		return 1
	}
}

// RunCLI shares the same operation definitions and client as MCP. It never
// opens a writable node, launches a shell, or implicitly starts the service.
func RunCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, definitions []Definition) error {
	if len(args) == 0 {
		return cliError(stdout, Fail("invalid_parameters", "Usage: asterhand automation <operation|help|schema|new-id> [--home DIR] [--input FILE|-] [--operation-id ID]"))
	}
	name := args[0]
	if name == "help" || name == "schema" {
		if len(args) > 2 {
			return cliError(stdout, Fail("invalid_parameters", "Specify at most one operation name"))
		}
		if len(args) == 2 {
			for _, d := range definitions {
				if d.Name == args[1] {
					return json.NewEncoder(stdout).Encode(d)
				}
			}
			return cliError(stdout, Fail("invalid_parameters", "Unknown operation"))
		}
		return json.NewEncoder(stdout).Encode(map[string]any{"api_version": APIVersion, "operations": definitions})
	}
	if name == "new-id" {
		operation := ""
		if len(args) == 2 && args[1] == "node.start" {
			operation = args[1]
		} else if len(args) != 1 {
			return cliError(stdout, Fail("invalid_parameters", "Use new-id, or new-id node.start for the offline launcher"))
		}
		return json.NewEncoder(stdout).Encode(map[string]string{"operation_id": operations.NewIDFor(operation, time.Now())})
	}
	i := slices.IndexFunc(definitions, func(d Definition) bool { return d.Name == name })
	if i < 0 {
		return cliError(stdout, Fail("invalid_parameters", "Unknown operation; use automation help"))
	}
	d := definitions[i]
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(stderr)
	home := f.String("home", config.DefaultHome(), "Existing node data directory")

	input := f.String("input", "", "JSON argument file, or - for standard input; omitted means {}")
	id := f.String("operation-id", "", "Stable write identity; retain it across retries")
	if err := f.Parse(args[1:]); err != nil {
		return cliError(stdout, Fail("invalid_parameters", "Invalid CLI flags"))
	}
	if f.NArg() != 0 {
		return cliError(stdout, Fail("invalid_parameters", "Unexpected positional argument"))
	}
	data := []byte("{}")
	if *input != "" {
		reader := stdin
		if *input != "-" {
			file, err := os.Open(*input)
			if err != nil {
				return cliError(stdout, Fail("invalid_parameters", "Cannot open the JSON input file"))
			}
			defer file.Close()
			reader = file
		}
		var err error
		data, err = io.ReadAll(io.LimitReader(reader, MaxRequest+1))
		if err != nil || len(data) > MaxRequest {
			return cliError(stdout, Fail("request_too_large", "JSON input exceeds 1 MiB"))
		}
	}
	if _, err := d.normalize(data); err != nil {
		return cliError(stdout, PublicError(err))
	}
	if d.Write && *id == "" {
		*id = operations.NewIDFor(name, time.Now())
	}
	if !d.Write && *id != "" {
		return cliError(stdout, Fail("invalid_parameters", "Read operations do not accept an operation identity"))
	}
	if d.Write {
		if err := operations.CheckName(*id, name); err != nil {
			return cliError(stdout, PublicError(err))
		}
		_ = json.NewEncoder(stderr).Encode(map[string]string{"operation_id": *id})
	}
	client, err := OpenClient(*home)
	if err != nil {
		return cliError(stdout, PublicError(err))
	}
	defer client.Close()
	response, err := client.Call(ctx, name, *id, data)
	if err != nil {
		response = Response{Version: APIVersion, Node: client.Profile.Node, ID: *id, Error: PublicError(err)}
	}
	if err = json.NewEncoder(stdout).Encode(response); err != nil {
		return err
	}
	if response.Error != nil {
		return &ExitError{Code: ErrorExit(response.Error.Code)}
	}
	return nil
}

func cliError(out io.Writer, e *Error) error {
	if err := json.NewEncoder(out).Encode(Response{Version: APIVersion, Error: e}); err != nil {
		return err
	}
	return &ExitError{Code: ErrorExit(e.Code)}
}
