package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Service) reserveLocked(newBytes int64, newFiles int, snapshotBytes int64) error {
	if err := s.reserveWorkspaceLocked(newBytes, newFiles); err != nil {
		return err
	}
	var stored int64
	for _, op := range s.records {
		info, err := os.Stat(filepath.Join(s.journal, op.ID+".json"))
		if err != nil {
			return err
		}
		stored += info.Size()
	}
	if stored+snapshotBytes > MaxVersionBytes {
		return fail("version_limit")
	}
	return nil
}

func (s *Service) reserveWorkspaceLocked(newBytes int64, newFiles int) error {
	// Known non-growing changes can release space even when an external writer
	// has exceeded the workspace quota or directory enumeration limit.
	if newBytes == 0 && newFiles == 0 {
		return nil
	}
	var bytes int64
	files := 0
	deadline := time.Now().Add(5 * time.Second)
	var visit func(string) error
	visit = func(dir string) error {
		entries, err := s.directoryEntries(dir, MaxWorkspaceFiles+1)
		if err != nil {
			return err
		}
		for _, item := range entries {
			files++
			if files+newFiles > MaxWorkspaceFiles || time.Now().After(deadline) {
				return fail("workspace_limit")
			}
			if item.Type == "directory" {
				if err := visit(item.Path); err != nil {
					return err
				}
			} else {
				bytes += item.Size
			}
			if files+newFiles > MaxWorkspaceFiles || bytes+newBytes > MaxWorkspaceBytes {
				return fail("workspace_limit")
			}
		}
		return nil
	}
	if err := visit(""); err != nil {
		return err
	}
	return nil
}

func (s *Service) pruneLocked() error {
	ops := s.sortedLocked()
	sort.Slice(ops, func(i, j int) bool { return ops[i].Started.Before(ops[j].Started) })
	counts := map[string]int{}
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	var metadata int64
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		pinned := op.State == "started" || op.State == "unknown"
		keep := pinned || op.Started.After(cutoff)
		expire := map[string]bool{}
		for _, change := range op.Changes {
			counts[pathKey(change.Path)]++
			if !pinned && change.Restorable && (counts[pathKey(change.Path)] > 20 || !op.Started.After(cutoff)) {
				expire[pathKey(change.Path)] = true
			}
		}
		if !keep {
			if err := os.Remove(filepath.Join(s.journal, op.ID+".json")); err != nil {
				return err
			}
			delete(s.records, op.ID)
			continue
		}
		if len(expire) > 0 {
			data, err := os.ReadFile(filepath.Join(s.journal, op.ID+".json"))
			if err != nil || json.Unmarshal(data, &op) != nil {
				return fail("journal_corrupt")
			}
			for i := range op.Changes {
				if expire[pathKey(op.Changes[i].Path)] {
					op.Changes[i].Content = nil
					op.Changes[i].Restorable = false
				}
			}
			if err := s.persist(op); err != nil {
				return err
			}
			s.records[op.ID] = publicOperation(op)
		}
		data, _ := json.Marshal(publicOperation(op))
		metadata += int64(len(data))
	}
	if metadata+MaxOperationBytes > MaxActivityBytes {
		return fail("activity_limit")
	}
	return nil
}

func (s *Service) sortedLocked() []Operation {
	ops := make([]Operation, 0, len(s.records))
	for _, op := range s.records {
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Sequence < ops[j].Sequence })
	return ops
}

func (s *Service) Operations(turn string, after int64) ([]Operation, int64) {
	return s.Activities("", turn, after)
}

func (s *Service) Activities(conversation, turn string, after int64) ([]Operation, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []Operation{}
	bytes := 0
	for _, op := range s.sortedLocked() {
		if op.Sequence <= after || (turn != "" && turn != op.Scope.Turn) || (conversation != "" && conversation != op.Scope.Conversation) {
			continue
		}
		op = publicOperation(op)
		op.Changes = nil
		encoded, _ := json.Marshal(op)
		if bytes+len(encoded) > PageBytes {
			break
		}
		bytes += len(encoded)
		items = append(items, op)
		after = op.Sequence
		if len(items) >= 20 {
			break
		}
	}
	return items, after
}

func (s *Service) Operation(id string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.records[id]
	if !ok {
		return Operation{}, fail("not_found")
	}
	return publicOperation(op), nil
}

func (s *Service) Resolve(id, evidence string) (Operation, error) {
	return s.ResolveRequest("", id, evidence)
}

func (s *Service) Resolution(request string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range s.records {
		if request != "" && op.Resolution == request {
			return publicOperation(op), nil
		}
	}
	return Operation{}, fail("not_found")
}

func (s *Service) ResolveRequest(request, id, evidence string) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil || s.turn != "" {
		return Operation{}, fail("busy")
	}
	if strings.TrimSpace(evidence) == "" || len(evidence) > 2048 {
		return Operation{}, fail("evidence_required")
	}
	op, ok := s.records[id]
	if !ok {
		return Operation{}, fail("not_found")
	}
	if op.State != "unknown" {
		if request != "" && op.Resolution != request {
			return Operation{}, fail("already_resolved")
		}
		return publicOperation(op), nil
	}
	data, err := os.ReadFile(filepath.Join(s.journal, id+".json"))
	if err != nil || json.Unmarshal(data, &op) != nil {
		return Operation{}, fail("journal_corrupt")
	}
	// Resolving records the observed state, never manufactures success or replays.
	for i := range op.Changes {
		h, err := hold(s.root, op.Changes[i].Path, false, false)
		if err != nil {
			return Operation{}, err
		}
		op.Changes[i].After = MissingRevision
		if h.file != nil {
			data, err := h.bytes(MaxFileBytes)
			h.close()
			if err != nil {
				return Operation{}, err
			}
			op.Changes[i].After = revision(data)
		} else {
			h.close()
		}
		op.Changes[i].State = "reconciled"
	}
	op.State, op.Detail, op.Finished = "reconciled", evidence, time.Now().UTC()
	op.Resolution = request
	s.sequence++
	op.Sequence = s.sequence
	if err := s.persist(op); err != nil {
		return Operation{}, err
	}
	s.records[id] = publicOperation(op)
	s.blocked = false
	for _, record := range s.records {
		if record.State == "unknown" {
			s.blocked = true
		}
	}
	return publicOperation(op), nil
}

func (s *Service) Versions(name string, after int64) ([]Operation, int64, error) {
	name, err := relative(name, false)
	if err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []Operation{}
	bytes := 0
	for _, op := range s.sortedLocked() {
		if op.Sequence <= after {
			continue
		}
		for _, change := range op.Changes {
			if pathKey(change.Path) != pathKey(name) || !change.Restorable {
				continue
			}
			op = publicOperation(op)
			op.Changes = []Change{change}
			op.Changes[0].Content = nil
			encoded, _ := json.Marshal(op)
			if bytes+len(encoded) > PageBytes {
				return items, after, nil
			}
			bytes += len(encoded)
			items = append(items, op)
			after = op.Sequence
			break
		}
		if len(items) == 20 {
			break
		}
	}
	return items, after, nil
}

func (s *Service) Restore(id string, scope Scope, versionID, name, expected string) (Operation, error) {
	s.mu.Lock()
	op, ok := s.records[versionID]
	if ok {
		data, err := os.ReadFile(filepath.Join(s.journal, versionID+".json"))
		if err != nil || json.Unmarshal(data, &op) != nil {
			s.mu.Unlock()
			return Operation{}, fail("journal_corrupt")
		}
	}
	var original *Change
	for _, change := range op.Changes {
		if pathKey(change.Path) == pathKey(name) {
			copy := change
			original = &copy
			break
		}
	}
	s.mu.Unlock()
	if !ok || original == nil {
		return Operation{}, fail("not_found")
	}
	if !original.Restorable {
		return Operation{}, fail("version_expired")
	}
	mutation := Mutation{Action: "write", Path: original.Path, Expected: expected, Content: string(original.Content)}
	if original.Before == MissingRevision {
		mutation.Action = "remove"
	}
	return s.Mutate(id, scope, []Mutation{mutation})
}

func (s *Service) Artifacts(turn string, after int64) ([]Artifact, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := []Artifact{}
	bytes := 0
	for _, op := range s.sortedLocked() {
		if op.Sequence <= after || turn != "" && op.Scope.Turn != turn {
			continue
		}
		batch := []Artifact{}
		batchBytes := 0
		for i, change := range op.Changes {
			if change.State == "changed" && change.After != MissingRevision {
				item := Artifact{ID: artifactID(op.ID, i), Operation: op.ID, Path: change.Path, Revision: change.After, Scope: op.Scope}
				encoded, _ := json.Marshal(item)
				batchBytes += len(encoded)
				batch = append(batch, item)
			}
		}
		if len(items) > 0 && (len(items)+len(batch) > MaxTargets || bytes+batchBytes > PageBytes) {
			break
		}
		items = append(items, batch...)
		bytes += batchBytes
		after = op.Sequence
	}
	return items, after
}

func (s *Service) Artifact(id string) (Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range s.records {
		for i, change := range op.Changes {
			if artifactID(op.ID, i) == id && change.State == "changed" && change.After != MissingRevision {
				return Artifact{ID: id, Operation: op.ID, Path: change.Path, Revision: change.After, Scope: op.Scope}, nil
			}
		}
	}
	return Artifact{}, fail("not_found")
}
