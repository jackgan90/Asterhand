package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Service) pinJournalLocked(id string) {
	if id != "" {
		s.pinnedJournals[id]++
	}
}

func (s *Service) unpinJournalLocked(id string) {
	if n := s.pinnedJournals[id]; n <= 1 {
		delete(s.pinnedJournals, id)
	} else {
		s.pinnedJournals[id] = n - 1
	}
}

func (s *Service) totalUnsettledLocked(exclude *call) (int64, int) {
	var b int64
	f := 0
	for _, c := range s.callsByID {
		if c == exclude {
			continue
		}
		b += c.unsettledBytes
		f += c.unsettledFiles
	}
	return b, f
}

func (s *Service) storedVersionBytesLocked() int64 {
	var total int64
	for id := range s.records {
		if sz, ok := s.journalSizes[id]; ok {
			total += sz
			continue
		}
		if info, err := os.Stat(filepath.Join(s.journal, id+".json")); err == nil {
			s.journalSizes[id] = info.Size()
			total += info.Size()
		}
	}
	return total
}

func (s *Service) storedActivityBytesLocked() int64 {
	var total int64
	for id, op := range s.records {
		if op.State == "started" && s.callsByID[id] == nil {
			// Still covered by s.reservedActivityBytes (MaxOperationBytes).
			continue
		}
		data, _ := json.Marshal(publicOperation(op))
		total += int64(len(data))
	}
	return total
}

func (s *Service) pruneAndReserveActivityLocked(extra int64) error {
	if err := s.pruneLocked(); err != nil {
		return err
	}
	if s.storedActivityBytesLocked()+s.reservedActivityBytes+extra > MaxActivityBytes {
		return fail("activity_limit")
	}
	return nil
}

func (s *Service) measureWorkspaceDisk(needBytes int64, needFiles int) (int64, int, error) {
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
			if files+needFiles > MaxWorkspaceFiles || time.Now().After(deadline) {
				return fail("workspace_limit")
			}
			if item.Type == "directory" {
				if err := visit(item.Path); err != nil {
					return err
				}
			} else {
				bytes += item.Size
			}
			if files+needFiles > MaxWorkspaceFiles || bytes+needBytes > MaxWorkspaceBytes {
				return fail("workspace_limit")
			}
		}
		return nil
	}
	if err := visit(""); err != nil {
		return 0, 0, err
	}
	return bytes, files, nil
}

func (s *Service) reserveForCall(c *call, newBytes int64, newFiles int, snapshotBytes int64) error {
	s.mu.Lock()
	if s.overQuota && (newBytes > 0 || newFiles > 0) {
		s.mu.Unlock()
		return fail("workspace_limit")
	}
	nonAtomic := s.sched.faults != nil && s.sched.faults.nonAtomicQuotaCheck
	s.mu.Unlock()

	if newBytes > 0 || newFiles > 0 {
		for attempt := 0; attempt < 8; attempt++ {
			s.mu.Lock()
			gen := s.publishGen
			s.mu.Unlock()

			diskBytes, diskFiles, err := s.measureWorkspaceDisk(newBytes, newFiles)
			if err != nil {
				if Code(err) == "workspace_limit" {
					s.mu.Lock()
					s.overQuota = true
					s.mu.Unlock()
				}
				return err
			}

			s.mu.Lock()
			if !nonAtomic && s.publishGen != gen && attempt < 7 {
				s.mu.Unlock()
				continue
			}
			var otherBytes int64
			otherFiles := 0
			if !nonAtomic {
				otherBytes, otherFiles = s.totalUnsettledLocked(c)
			}
			if diskFiles+otherFiles+newFiles > MaxWorkspaceFiles || diskBytes+otherBytes+newBytes > MaxWorkspaceBytes {
				s.overQuota = true
				s.mu.Unlock()
				return fail("workspace_limit")
			}
			c.unsettledBytes = newBytes
			c.unsettledFiles = newFiles
			if s.storedVersionBytesLocked()+s.reservedVersionBytes+snapshotBytes > MaxVersionBytes {
				c.unsettledBytes, c.unsettledFiles = 0, 0
				s.mu.Unlock()
				return fail("version_limit")
			}
			c.resVersion = snapshotBytes
			s.reservedVersionBytes += snapshotBytes
			s.mu.Unlock()
			return nil
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storedVersionBytesLocked()+s.reservedVersionBytes+snapshotBytes > MaxVersionBytes {
		return fail("version_limit")
	}
	c.resVersion = snapshotBytes
	s.reservedVersionBytes += snapshotBytes
	return nil
}

func (s *Service) checkDynamicWriteQuota(c *call, growth int64, newFiles int) error {
	if growth == 0 && newFiles == 0 {
		return nil
	}
	s.mu.Lock()
	over := s.overQuota
	s.mu.Unlock()
	if over {
		return fail("workspace_limit")
	}
	extraBytes := max(0, growth-c.unsettledBytes)
	extraFiles := max(0, newFiles-c.unsettledFiles)
	if extraBytes == 0 && extraFiles == 0 {
		return nil
	}
	for attempt := 0; attempt < 8; attempt++ {
		s.mu.Lock()
		gen := s.publishGen
		nonAtomic := s.sched.faults != nil && s.sched.faults.nonAtomicQuotaCheck
		s.mu.Unlock()

		diskBytes, diskFiles, err := s.measureWorkspaceDisk(extraBytes, extraFiles)
		if err != nil {
			if Code(err) == "workspace_limit" {
				s.mu.Lock()
				s.overQuota = true
				s.mu.Unlock()
			}
			return err
		}
		s.mu.Lock()
		if !nonAtomic && s.publishGen != gen && attempt < 7 {
			s.mu.Unlock()
			continue
		}
		otherBytes, otherFiles := s.totalUnsettledLocked(c)
		if nonAtomic {
			otherBytes, otherFiles = 0, 0
		}
		needB := max(c.unsettledBytes, growth)
		needF := max(c.unsettledFiles, newFiles)
		if diskFiles+otherFiles+needF > MaxWorkspaceFiles || diskBytes+otherBytes+needB > MaxWorkspaceBytes {
			s.overQuota = true
			s.mu.Unlock()
			return fail("workspace_limit")
		}
		c.unsettledBytes = needB
		c.unsettledFiles = needF
		s.mu.Unlock()
		return nil
	}
	return fail("workspace_limit")
}

func (s *Service) recordCallPublished(c *call, growth int64, newFiles int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.unsettledBytes = max(0, c.unsettledBytes-growth)
	c.unsettledFiles = max(0, c.unsettledFiles-newFiles)
	s.publishGen++
}

func (s *Service) bumpPublishGen() {
	s.mu.Lock()
	s.publishGen++
	s.mu.Unlock()
}

func (s *Service) releaseCallReservationLocked(c *call) {
	c.unsettledBytes = 0
	c.unsettledFiles = 0
	if c.resVersion > 0 {
		if s.reservedVersionBytes >= c.resVersion {
			s.reservedVersionBytes -= c.resVersion
		} else {
			s.reservedVersionBytes = 0
		}
		c.resVersion = 0
	}
	if c.resActivity > 0 {
		if s.reservedActivityBytes >= c.resActivity {
			s.reservedActivityBytes -= c.resActivity
		} else {
			s.reservedActivityBytes = 0
		}
		c.resActivity = 0
	}
}

func (s *Service) pruneLocked() error {
	skipPin := s.sched.faults != nil && s.sched.faults.skipPinActiveJournals
	ops := s.sortedLocked()
	sort.Slice(ops, func(i, j int) bool { return ops[i].Started.Before(ops[j].Started) })
	counts := map[string]int{}
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		pinned := (!skipPin && s.pinnedJournals[op.ID] > 0) || (!skipPin && op.State == "started") || op.State == "unknown"
		keep := pinned || op.Started.After(cutoff)
		expire := map[string]bool{}
		for _, change := range op.Changes {
			counts[pathKey(change.Path)]++
			if !pinned && change.Restorable && (counts[pathKey(change.Path)] > 20 || !op.Started.After(cutoff)) {
				expire[pathKey(change.Path)] = true
			}
		}
		if !keep {
			if err := os.Remove(filepath.Join(s.journal, op.ID+".json")); err != nil && !os.IsNotExist(err) {
				return err
			}
			delete(s.records, op.ID)
			delete(s.journalSizes, op.ID)
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
			raw, err := s.persist(op)
			if err != nil {
				return err
			}
			s.journalSizes[op.ID] = int64(len(raw))
			s.records[op.ID] = publicOperation(op)
		}
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
	matched := make([]Operation, 0, min(len(s.records), 32))
	for _, op := range s.records {
		if op.Sequence <= after || (turn != "" && turn != op.Scope.Turn) || (conversation != "" && conversation != op.Scope.Conversation) {
			continue
		}
		matched = append(matched, op)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Sequence < matched[j].Sequence })
	items := []Operation{}
	bytes := 0
	for _, op := range matched {
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
	if s.busyLocked() {
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
	for i := range op.Changes {
		h, err := hold(s.root, op.Changes[i].Path, false, false)
		if err != nil {
			return Operation{}, err
		}
		op.Changes[i].After = MissingRevision
		if h.file != nil {
			fileData, err := h.bytes(MaxFileBytes)
			h.close()
			if err != nil {
				return Operation{}, err
			}
			op.Changes[i].After = revision(fileData)
		} else {
			h.close()
		}
		op.Changes[i].State = "reconciled"
	}
	op.State, op.Phase, op.WaitReason, op.Detail, op.Finished = "reconciled", PhaseTerminal, "", evidence, time.Now().UTC()
	op.Resolution = request
	s.sequence++
	op.Sequence = s.sequence
	raw, err := s.persist(op)
	if err != nil {
		return Operation{}, err
	}
	s.journalSizes[id] = int64(len(raw))
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
		s.pinJournalLocked(versionID)
	}
	s.mu.Unlock()
	if !ok {
		return Operation{}, fail("not_found")
	}
	defer func() {
		s.mu.Lock()
		s.unpinJournalLocked(versionID)
		s.mu.Unlock()
	}()
	data, err := os.ReadFile(filepath.Join(s.journal, versionID+".json"))
	if err != nil || json.Unmarshal(data, &op) != nil {
		return Operation{}, fail("journal_corrupt")
	}
	var original *Change
	for _, change := range op.Changes {
		if pathKey(change.Path) == pathKey(name) {
			cp := change
			original = &cp
			break
		}
	}
	if original == nil {
		return Operation{}, fail("not_found")
	}
	if !original.Restorable {
		return Operation{}, fail("version_expired")
	}
	mutation := Mutation{Action: "write", Path: original.Path, Expected: expected, Content: string(original.Content)}
	if original.Before == MissingRevision {
		mutation.Action = "remove"
	}
	return s.MutateWithTool(id, scope, "workspace_versions.restore", []Mutation{mutation})
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
