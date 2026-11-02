package localcommands

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/model"
)

const maxCatalogBytes = 16 * 1024 * 1024

type Launcher func(Command) error

type Registry struct {
	mu           sync.RWMutex
	path         string
	catalog      Catalog
	loadErr      error
	policy       CommandPolicy
	managed      *RunManager
	closing      bool
	launch       Launcher
	probes       chan struct{}
	probeMu      sync.Mutex
	statuses     map[string]*fileStatus
	probeQueue   []probeTask
	probeWorkers int
}

// Open isolates invalid configuration to this feature and never overwrites it.
func Open(home string, launch Launcher) *Registry { return OpenWithPolicy(home, launch, commandPolicy) }

func OpenWithPolicy(home string, launch Launcher, policy CommandPolicy) *Registry {
	r := &Registry{policy: policy, path: filepath.Join(home, "local-commands.json"), launch: launch,
		catalog: Catalog{Schema: 3, Revision: model.ID(), Commands: []Command{}},
		probes:  make(chan struct{}, 4), statuses: map[string]*fileStatus{}}
	r.loadErr = r.load()
	return r
}

func (r *Registry) load() error {
	f, err := os.Open(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCatalogBytes+1))
	if err != nil || len(b) > maxCatalogBytes {
		return i18n.Err("err.localcmd.read_failed_size")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if decoder.Decode(&catalog) != nil || decoder.Decode(new(any)) != io.EOF {
		return i18n.Err("err.localcmd.invalid_json")
	}
	if catalog.Schema == 1 || catalog.Schema == 2 {
		catalog.Schema = 3
	}
	// Normalize loaded definitions before uniqueness checks, just like API edits.
	for i := range catalog.Commands {
		catalog.Commands[i], err = normalizeWithPolicy(catalog.Commands[i], r.policy)
		if err != nil {
			return err
		}
	}
	if err := validateCatalogWithPolicy(catalog, r.policy); err != nil {
		return err
	}
	if len(catalog.PendingCleanup) > 10000 {
		return problem("configuration", "err.localcmd.limit_exceeded")
	}
	for _, id := range catalog.PendingCleanup {
		if !wire.ValidID(id) {
			return problem("configuration", "err.localcmd.invalid_wire_req")
		}
	}
	r.catalog = catalog
	return nil
}

func cloneCatalog(c Catalog) Catalog {
	c.PendingCleanup = slices.Clone(c.PendingCleanup)
	c.Commands = slices.Clone(c.Commands)
	for i := range c.Commands {
		c.Commands[i].Graph = cloneGraph(c.Commands[i].Graph)
		if c.Commands[i].TimeoutSeconds != nil {
			value := *c.Commands[i].TimeoutSeconds
			c.Commands[i].TimeoutSeconds = &value
		}
		c.Commands[i].Aliases = slices.Clone(c.Commands[i].Aliases)
		c.Commands[i].Arguments = slices.Clone(c.Commands[i].Arguments)
	}
	return c
}

func (r *Registry) ready() error {
	if r.loadErr != nil {
		return problem("configuration", "err.localcmd.config_unreadable", map[string]string{"err": r.loadErr.Error()})
	}
	return nil
}

func (r *Registry) Snapshot() (Catalog, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.ready(); err != nil {
		return Catalog{}, err
	}
	return cloneCatalog(r.catalog), nil
}

func (r *Registry) save(next Catalog) error {
	next.Revision = model.ID()
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > maxCatalogBytes || len(next.Commands) > 10000 {
		return problem("invalid", "err.localcmd.limit_exceeded")
	}
	if err := config.AtomicFile(r.path, append(b, '\n'), 0600); err != nil {
		return i18n.Wrap("err.localcmd.save_failed", nil, err)
	}
	r.catalog = next
	r.probeMu.Lock()
	r.statuses = map[string]*fileStatus{}
	r.probeMu.Unlock()
	return nil
}

func (r *Registry) Save(change Mutation) (SaveResult, error) {
	command, err := normalizeWithPolicy(change.Command, r.policy)
	if err != nil {
		return SaveResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ready(); err != nil {
		return SaveResult{}, err
	}
	if change.Revision != r.catalog.Revision {
		return SaveResult{}, problem("conflict", "err.localcmd.save_conflict")
	}
	if r.closing {
		return SaveResult{}, problem("unavailable", "err.app.node_stopping")
	}
	if r.managed.Busy(command.ID) && command.ID != "" {
		return SaveResult{}, problem("busy", "err.localcmd.running")
	}
	if err := r.cleanupPending(); err != nil {
		return SaveResult{}, err
	}
	next := cloneCatalog(r.catalog)
	index := slices.IndexFunc(next.Commands, func(c Command) bool { return c.ID == command.ID })
	if command.ID != "" && index < 0 {
		return SaveResult{}, problem("not_found", "err.localcmd.deleted_refresh")
	}
	if index >= 0 {
		next.Commands = slices.Delete(next.Commands, index, index+1)
	}
	match := slices.IndexFunc(next.Commands, func(c Command) bool { return sameLaunchWithPolicy(c, command, r.policy) })
	if match >= 0 {
		if r.managed.Busy(next.Commands[match].ID) {
			return SaveResult{}, problem("busy", "err.localcmd.running")
		}
		if index >= 0 && executionMode(r.catalog.Commands[index]) == "managed" {
			return SaveResult{}, problem("conflict", "err.localcmd.managed_merge")
		}
	}
	for i, other := range next.Commands {
		if i == match {
			continue
		}
		for _, name := range commandNames(command) {
			if containsName(commandNames(other), name) {
				return SaveResult{}, problem("name_conflict", "err.localcmd.name_taken", map[string]string{"name": name})
			}
		}
	}
	if match >= 0 {
		existing := &next.Commands[match]
		for _, name := range commandNames(command) {
			if !containsName(commandNames(*existing), name) {
				existing.Aliases = append(existing.Aliases, name)
			}
		}
		if len(existing.Aliases) > 16 {
			return SaveResult{}, problem("invalid", "err.localcmd.merged_aliases_limit")
		}
		existing.Revision = model.ID()
		command.ID = existing.ID
	} else {
		if command.ID == "" {
			command.ID = model.ID()
		}
		command.Revision = model.ID()
		next.Commands = append(next.Commands, command)
	}
	if index >= 0 && executionMode(r.catalog.Commands[index]) == "managed" && command.ExecutionMode != "managed" {
		next.PendingCleanup = append(next.PendingCleanup, change.Command.ID)
	}
	if err := r.save(next); err != nil {
		return SaveResult{}, err
	}
	if err := r.cleanupPending(); err != nil {
		return SaveResult{}, err
	}
	return SaveResult{Catalog: cloneCatalog(r.catalog), ID: command.ID, Merged: match >= 0}, nil
}

func (r *Registry) Delete(id, revision string) (Catalog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ready(); err != nil {
		return Catalog{}, err
	}
	if revision != r.catalog.Revision {
		return Catalog{}, problem("conflict", "err.localcmd.delete_conflict")
	}
	if r.closing {
		return Catalog{}, problem("unavailable", "err.app.node_stopping")
	}
	if r.managed.Busy(id) {
		return Catalog{}, problem("busy", "err.localcmd.running")
	}
	next := cloneCatalog(r.catalog)
	i := slices.IndexFunc(next.Commands, func(c Command) bool { return c.ID == id })
	if i < 0 {
		return Catalog{}, problem("not_found", "err.localcmd.deleted_refresh")
	}
	if executionMode(next.Commands[i]) == "managed" {
		next.PendingCleanup = append(next.PendingCleanup, id)
	}
	next.Commands = slices.Delete(next.Commands, i, i+1)
	if err := r.save(next); err != nil {
		return Catalog{}, err
	}
	if err := r.cleanupPending(); err != nil {
		return Catalog{}, err
	}
	return cloneCatalog(r.catalog), nil
}
