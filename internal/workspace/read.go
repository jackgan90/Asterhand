package workspace

import (
	"encoding/json"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

func (s *Service) Read(name, expected string, offset int64) (Fragment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, err := relative(name, false)
	if err != nil {
		return Fragment{}, err
	}
	if !s.enabled {
		return Fragment{}, fail("not_authorized")
	}
	if offset < 0 {
		return Fragment{}, fail("invalid_offset")
	}
	h, cleanup, err := s.readHandle(name)
	if err != nil {
		return Fragment{}, err
	}
	defer cleanup()
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
	return Fragment{Path: name, Revision: rev, Data: data[offset:end], Next: end, More: end < int64(len(data))}, nil
}

// NativeRead uses the same bounded, pinned I/O. The private adapter RPC has a
// separate 3 MiB limit; public reads always use PageBytes fragments.
func (s *Service) NativeRead(id, name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.active.op.ID != id {
		return nil, fail("invalid_call")
	}
	name, err := relative(name, false)
	if err != nil {
		return nil, err
	}
	h, cleanup, err := s.readHandle(name)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return h.bytes(MaxFileBytes)
}

func (s *Service) readHandle(name string) (*heldFile, func(), error) {
	if s.active != nil {
		if h := s.active.held[pathKey(name)]; h != nil {
			return h, func() {}, nil
		}
	}
	h, err := hold(s.root, name, false, false)
	if err != nil {
		return nil, nil, err
	}
	return h, h.close, nil
}

func (s *Service) Stat(name string) (*Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, err := relative(name, true)
	if err != nil {
		return nil, err
	}
	if !s.enabled {
		return nil, fail("not_authorized")
	}
	if s.active != nil {
		if h := s.active.held[pathKey(name)]; h != nil {
			if h.file == nil || h.removed {
				return nil, nil
			}
			info, err := h.file.Stat()
			if err != nil {
				return nil, err
			}
			return entry(name, info), nil
		}
	}
	d, err := directory(s.root, name)
	if err == nil {
		defer d.close()
		info, err := d.parents[len(d.parents)-1].Stat()
		if err != nil {
			return nil, err
		}
		return entry(name, info), nil
	}
	h, err := hold(s.root, name, false, false)
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
	return entry(name, info), nil
}

func entry(name string, info os.FileInfo) *Entry {
	kind := "file"
	if info.IsDir() {
		kind = "directory"
	}
	return &Entry{Path: name, Type: kind, Size: info.Size(), Modified: info.ModTime().UnixMilli()}
}

func (s *Service) ReadDirectory(name string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return nil, fail("not_authorized")
	}
	name, err := relative(name, true)
	if err != nil {
		return nil, err
	}
	return s.directoryEntries(name, MaxWorkspaceFiles+1)
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
		if item.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := path.Join(name, item.Name())
		if _, err := relative(name, false); err != nil {
			continue
		}
		info, err := item.Info()
		if err != nil {
			return nil, err
		}
		entries = append(entries, *entry(name, info))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// Search is literal, case-insensitive text/name matching. A lexical checkpoint
// advances even when no match is found; external edits may change later pages.
func (s *Service) Search(base, query, cursor string, content, recursive bool) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return Page{}, fail("not_authorized")
	}
	base, err := relative(base, true)
	if err != nil {
		return Page{}, err
	}
	if len(query) > 1024 {
		return Page{}, fail("query_limit")
	}
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
	// Match traversal order to the cursor. A directory's slash sorts after
	// siblings such as a.txt, before any a/child, so pagination cannot skip it.
	scanKey := func(item Entry) string {
		if item.Type == "directory" {
			return item.Path + "/"
		}
		return item.Path
	}
	var visit func(string) error
	visit = func(dir string) error {
		entries, err := s.directoryEntries(dir, MaxWorkspaceFiles+1)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return scanKey(entries[i]) < scanKey(entries[j]) })
		for _, item := range entries {
			key := scanKey(item)
			if key > checkpoint.After {
				if scanned >= MaxWorkspaceFiles || scannedBytes >= 64<<20 || time.Now().After(deadline) {
					page.Truncated = true
					return nil
				}
				scanned++
				match := strings.Contains(strings.ToLower(item.Path), needle)
				if content {
					match = false
					if item.Type == "file" && item.Size <= MaxFileBytes {
						h, cleanup, readErr := s.readHandle(item.Path)
						if readErr == nil {
							data, readErr := h.bytes(MaxFileBytes)
							cleanup()
							scannedBytes += int64(len(data))
							if readErr == nil {
								match = strings.Contains(strings.ToLower(string(data)), needle)
								item.Revision = revision(data)
							}
						}
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
