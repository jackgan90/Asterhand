package workspace

import "encoding/json"

func (s *Service) Mutate(id string, scope Scope, mutations []Mutation) (Operation, error) {
	return s.MutateWithTool(id, scope, "workspace_files.mutate", mutations)
}

func (s *Service) MutateWithTool(id string, scope Scope, tool string, mutations []Mutation) (Operation, error) {
	if len(mutations) == 0 || len(mutations) > MaxTargets {
		return Operation{}, fail("batch_limit")
	}
	targets := []Target{}
	var bytes int64
	for _, mutation := range mutations {
		if mutation.Expected == "" {
			return Operation{}, fail("revision_required")
		}
		targets = append(targets, Target{Path: mutation.Path, Expected: mutation.Expected, Remove: mutation.Action == "remove" || mutation.Action == "move"})
		switch mutation.Action {
		case "write":
			bytes += int64(len(mutation.Content))
		case "remove":
		case "move":
			if mutation.DestinationExpected != MissingRevision {
				return Operation{}, fail("revision_required")
			}
			targets = append(targets, Target{Path: mutation.Destination, Expected: MissingRevision})
			bytes += MaxFileBytes
		default:
			return Operation{}, fail("invalid_action")
		}
	}
	encoded, _ := json.Marshal(mutations)
	op, err := s.Begin(Begin{ID: id, Scope: scope, Tool: tool, Targets: targets, NewBytes: bytes, Fingerprint: revision(encoded)})
	if err != nil || op.State != "started" {
		return op, err
	}
	for _, mutation := range mutations {
		switch mutation.Action {
		case "write":
			_, err = s.Write(id, mutation.Path, []byte(mutation.Content), false)
		case "remove":
			err = s.Remove(id, mutation.Path)
		case "move":
			var data []byte
			data, err = s.NativeRead(id, mutation.Path)
			if err == nil {
				_, err = s.Write(id, mutation.Destination, data, true)
			}
			if err == nil {
				err = s.Remove(id, mutation.Path)
			}
		}
		if err != nil {
			break
		}
	}
	state, detail := "completed", ""
	if err != nil {
		state, detail = "failed", Code(err)
	}
	result, endErr := s.End(id, state, detail)
	if endErr != nil {
		return result, endErr
	}
	return result, err
}
