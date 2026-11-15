package workspace

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

func (s *Service) withTransientRead(ctx context.Context, tool, rel string) (func(), error) {
	s.mu.Lock()
	if !s.enabled || !platformSupported() {
		s.mu.Unlock()
		return nil, fail("not_authorized")
	}
	s.mu.Unlock()

	plan, err := PrepareTool(tool, DescriptorInput{WorkspaceRoot: s.root, Path: rel})
	if err != nil {
		return nil, err
	}
	sysID := "sys-" + rand.Text()
	s.sched.mu.Lock()
	if s.sched.queuedCountLocked() >= s.sched.maxQueued {
		s.sched.mu.Unlock()
		return nil, fail("queue_limit")
	}
	s.sched.applyClaimFaults(&plan)
	s.sched.nextTicket++
	entry := &schedEntry{
		id: sysID, batch: sysID, ticket: s.sched.nextTicket,
		tool: tool, plan: plan, phase: PhaseRegistered, sealed: true, system: true,
	}
	s.sched.entries[sysID] = entry
	s.sched.mu.Unlock()

	granted, err := s.sched.acquire(ctx, sysID, "", nil)
	if err != nil {
		return nil, err
	}
	s.sched.mu.Lock()
	granted.phase = PhaseRunning
	s.sched.mu.Unlock()
	for _, claim := range plan.Claims {
		s.recordTrace(sysID, tool, "granted", "", claim.Root, claim.Key, string(claim.Access))
	}
	return func() {
		s.sched.mu.Lock()
		s.sched.releaseEntryLocked(granted)
		s.sched.mu.Unlock()
		s.recordTrace(sysID, tool, "released", "", DefaultRoot, pathKey(rel), "completed")
	}, nil
}

func (s *Service) Read(name, expected string, offset int64) (Fragment, error) {
	return s.ReadContext(context.Background(), name, expected, offset)
}

func (s *Service) ReadContext(ctx context.Context, name, expected string, offset int64) (Fragment, error) {
	rel, err := relative(name, false)
	if err != nil {
		return Fragment{}, err
	}
	if offset < 0 {
		return Fragment{}, fail("invalid_offset")
	}
	release, err := s.withTransientRead(ctx, "workspace_files.read", rel)
	if err != nil {
		return Fragment{}, err
	}
	defer release()

	h, err := hold(s.root, rel, false, false)
	if err != nil {
		return Fragment{}, err
	}
	defer h.close()
	s.fireIOHook("", "workspace_files.read", EffectRead, rel)
	data, err := h.bytes(MaxFileBytes)
	if err != nil {
		return Fragment{}, err
	}
	rev := revision(data)
	if expected != "" && rev != expected {
		return Fragment{}, fail("revision_conflict")
	}
	if offset > int64(len(data)) {
		return Fragment{}, fail("invalid_offset")
	}
	end := min(offset+PageBytes, int64(len(data)))
	return Fragment{Path: rel, Revision: rev, Data: data[offset:end], Next: end, More: end < int64(len(data))}, nil
}

// NativeRead validates the exact call identity and granted claims, using only
// the owning call's pinned handle (when modifying) or an independent shared-read handle.
func (s *Service) NativeRead(id, name string) ([]byte, error) {
	rel, err := relative(name, false)
	if err != nil {
		return nil, err
	}
	c, e, err := s.enterCallIO(id, DefaultRoot, rel, EffectRead, false)
	if err != nil {
		return nil, err
	}
	defer s.leaveCallIO(e, EffectRead, DefaultRoot, rel)

	c.mu.Lock()
	own := c.held[pathKey(rel)]
	if own != nil {
		s.fireIOHook(c.op.ID, c.op.Tool, EffectRead, rel)
		data, readErr := own.bytes(MaxFileBytes)
		c.mu.Unlock()
		return data, readErr
	}
	c.mu.Unlock()

	h, err := hold(s.root, rel, false, false)
	if err != nil {
		return nil, err
	}
	defer h.close()
	s.fireIOHook(c.op.ID, c.op.Tool, EffectRead, rel)
	return h.bytes(MaxFileBytes)
}

func (s *Service) readBootstrap(ctx context.Context, name string) ([]byte, error) {
	rel, err := relative(name, false)
	if err != nil {
		return nil, err
	}
	release, err := s.withTransientRead(ctx, "workspace.bootstrap", rel)
	if err != nil {
		return nil, err
	}
	defer release()
	h, err := hold(s.root, rel, false, false)
	if err != nil {
		return nil, err
	}
	defer h.close()
	return h.bytes(MaxFileBytes)
}

func (s *Service) statBootstrap(ctx context.Context, name string) (*Entry, error) {
	rel, err := relative(name, true)
	if err != nil {
		return nil, err
	}
	release, err := s.withTransientRead(ctx, "workspace.bootstrap", rel)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.statIndependent(rel)
}

func (s *Service) Stat(name string) (*Entry, error) {
	return s.StatWithCall("", name)
}

func (s *Service) StatWithCall(id, name string) (*Entry, error) {
	rel, err := relative(name, true)
	if err != nil {
		return nil, err
	}
	if id != "" {
		c, e, err := s.enterCallIO(id, DefaultRoot, rel, EffectStat, false)
		if err != nil {
			return nil, err
		}
		defer s.leaveCallIO(e, EffectStat, DefaultRoot, rel)
		c.mu.Lock()
		if own := c.held[pathKey(rel)]; own != nil {
			if own.file == nil || own.removed {
				c.mu.Unlock()
				return nil, nil
			}
			info, statErr := own.file.Stat()
			c.mu.Unlock()
			if statErr != nil {
				return nil, statErr
			}
			return entry(rel, info), nil
		}
		c.mu.Unlock()
		s.fireIOHook(c.op.ID, c.op.Tool, EffectStat, rel)
		return s.statIndependent(rel)
	}
	return s.statBootstrap(context.Background(), name)
}

func (s *Service) statIndependent(rel string) (*Entry, error) {
	d, err := directory(s.root, rel)
	if err == nil {
		defer d.close()
		info, err := d.parents[len(d.parents)-1].Stat()
		if err != nil {
			return nil, err
		}
		return entry(rel, info), nil
	}
	if rel == "" {
		return nil, err
	}
	h, err := hold(s.root, rel, false, false)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer h.close()
	if h.file == nil {
		return nil, nil
	}
	info, err := h.file.Stat()
	if err != nil {
		return nil, err
	}
	return entry(rel, info), nil
}

func entry(name string, info os.FileInfo) *Entry {
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	return &Entry{Path: name, Type: kind, Size: info.Size(), Modified: info.ModTime().UnixMilli()}
}

func (s *Service) ReadDirectory(name string) ([]Entry, error) {
	return s.ReadDirectoryContext(context.Background(), name)
}

func (s *Service) ReadDirectoryContext(ctx context.Context, name string) ([]Entry, error) {
	rel, err := relative(name, true)
	if err != nil {
		return nil, err
	}
	release, err := s.withTransientRead(ctx, "workspace_files.list", rel)
	if err != nil {
		return nil, err
	}
	defer release()
	s.fireIOHook("", "workspace_files.list", EffectList, rel)
	return s.directoryEntries(rel, MaxWorkspaceFiles+1)
}

func (s *Service) ReadDirectoryWithCall(id, name string) ([]Entry, error) {
	if id == "" {
		return s.ReadDirectoryContext(context.Background(), name)
	}
	rel, err := relative(name, true)
	if err != nil {
		return nil, err
	}
	c, e, err := s.enterCallIO(id, DefaultRoot, rel, EffectList, false)
	if err != nil {
		return nil, err
	}
	defer s.leaveCallIO(e, EffectList, DefaultRoot, rel)
	s.fireIOHook(c.op.ID, c.op.Tool, EffectList, rel)
	return s.directoryEntries(rel, MaxWorkspaceFiles+1)
}

func (s *Service) directoryEntries(name string, limit int) ([]Entry, error) {
	h, err := directory(s.root, name)
	if err != nil {
		return nil, err
	}
	defer h.close()
	items, err := h.parents[len(h.parents)-1].ReadDir(limit)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(items) >= limit {
		return nil, fail("directory_limit")
	}
	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		if item.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			continue
		}
		child := path.Join(name, item.Name())
		if _, err := relative(child, false); err != nil {
			continue
		}
		info, err := item.Info()
		if err != nil {
			return nil, err
		}
		if hasReparseAttribute(info) {
			continue
		}
		entries = append(entries, *entry(child, info))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func (s *Service) Search(base, query, cursor string, content, recursive bool) (Page, error) {
	return s.SearchContext(context.Background(), base, query, cursor, content, recursive)
}

func (s *Service) SearchContext(ctx context.Context, base, query, cursor string, content, recursive bool) (Page, error) {
	rel, err := relative(base, true)
	if err != nil {
		return Page{}, err
	}
	if len(query) > 1024 {
		return Page{}, fail("query_limit")
	}
	tool := "workspace_files.search"
	if !recursive && !content && query == "" {
		tool = "workspace_files.list"
	}
	release, err := s.withTransientRead(ctx, tool, rel)
	if err != nil {
		return Page{}, err
	}
	defer release()
	s.fireIOHook("", tool, EffectSearch, rel)
	return s.searchInternal(rel, query, cursor, content, recursive, func() error {
		if ctx != nil && ctx.Err() != nil {
			return fail("cancelled")
		}
		return nil
	})
}

func (s *Service) SearchWithCall(id, base, query, cursor string, content, recursive bool) (Page, error) {
	return s.SearchWithCallContext(context.Background(), id, base, query, cursor, content, recursive)
}

func (s *Service) SearchWithCallContext(ctx context.Context, id, base, query, cursor string, content, recursive bool) (Page, error) {
	if id == "" {
		return s.SearchContext(ctx, base, query, cursor, content, recursive)
	}
	rel, err := relative(base, true)
	if err != nil {
		return Page{}, err
	}
	if len(query) > 1024 {
		return Page{}, fail("query_limit")
	}
	c, e, err := s.enterCallIO(id, DefaultRoot, rel, EffectSearch, false)
	if err != nil {
		return Page{}, err
	}
	defer s.leaveCallIO(e, EffectSearch, DefaultRoot, rel)
	s.fireIOHook(c.op.ID, c.op.Tool, EffectSearch, rel)
	return s.searchInternal(rel, query, cursor, content, recursive, func() error {
		if ctx != nil && ctx.Err() != nil {
			return fail("cancelled")
		}
		if e != nil && e.isAdmissionClosed(s.sched) {
			return fail("cancelled")
		}
		return nil
	})
}

func matchFileContent(root string, item *Entry, needle string, checkCancelled func() error) (bool, int64, error) {
	if item.Type != "file" || item.Size > MaxFileBytes {
		return false, 0, nil
	}
	if checkCancelled != nil {
		if err := checkCancelled(); err != nil {
			return false, 0, err
		}
	}
	h, readErr := hold(root, item.Path, false, false)
	if readErr != nil {
		return false, 0, nil
	}
	defer h.close()
	if h.file == nil {
		return false, 0, nil
	}
	data, dataErr := h.bytes(MaxFileBytes)
	if dataErr != nil {
		return false, int64(len(data)), nil
	}
	matched := strings.Contains(strings.ToLower(string(data)), needle)
	item.Revision = revision(data)
	return matched, int64(len(data)), nil
}

func (s *Service) searchInternal(base, query, cursor string, content, recursive bool, checkCancelled func() error) (Page, error) {
	var checkpoint struct {
		Base, Query, After string
		Content, Recursive bool
	}
	if cursor != "" {
		if len(cursor) > 4096 || json.Unmarshal([]byte(cursor), &checkpoint) != nil || checkpoint.Base != base || checkpoint.Query != query || checkpoint.Content != content || checkpoint.Recursive != recursive {
			return Page{}, fail("invalid_cursor")
		}
	}
	page := Page{Items: []Entry{}}
	deadline := time.Now().Add(5 * time.Second)
	scanned, responseBytes := 0, 0
	var scannedBytes int64
	last := checkpoint.After
	needle := strings.ToLower(query)
	scanKey := func(item Entry) string {
		if item.Type == "directory" {
			return item.Path + "/"
		}
		return item.Path
	}
	var visit func(string) error
	visit = func(dir string) error {
		if checkCancelled != nil {
			if err := checkCancelled(); err != nil {
				return err
			}
		}
		entries, err := s.directoryEntries(dir, MaxWorkspaceFiles+1)
		if err != nil {
			if dir != base && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return scanKey(entries[i]) < scanKey(entries[j]) })
		for _, item := range entries {
			if checkCancelled != nil {
				if err := checkCancelled(); err != nil {
					return err
				}
			}
			key := scanKey(item)
			if key > checkpoint.After {
				if scanned >= MaxWorkspaceFiles || scannedBytes >= 64<<20 || time.Now().After(deadline) {
					page.Truncated = true
					return nil
				}
				scanned++
				match := strings.Contains(strings.ToLower(item.Path), needle)
				if content {
					var readBytes int64
					var matchErr error
					match, readBytes, matchErr = matchFileContent(s.root, &item, needle, checkCancelled)
					scannedBytes += readBytes
					if matchErr != nil {
						return matchErr
					}
				}
				if match {
					encoded, _ := json.Marshal(item)
					if len(page.Items) >= PageItems || responseBytes+len(encoded) > PageBytes {
						page.Truncated = true
						return nil
					}
					page.Items = append(page.Items, item)
					responseBytes += len(encoded)
				}
				last = key
			}
			if recursive && item.Type == "directory" && (key > checkpoint.After || strings.HasPrefix(checkpoint.After, key)) {
				if err := visit(item.Path); err != nil {
					return err
				}
				if page.Truncated {
					return nil
				}
			}
		}
		return nil
	}
	if err := visit(base); err != nil {
		return Page{}, err
	}
	if page.Truncated {
		checkpoint.Base, checkpoint.Query, checkpoint.After, checkpoint.Content, checkpoint.Recursive = base, query, last, content, recursive
		data, _ := json.Marshal(checkpoint)
		page.Next = string(data)
	}
	return page, nil
}
