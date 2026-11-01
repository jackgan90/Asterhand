package localcommands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/jackgan90/Asterhand/internal/config"
	"github.com/jackgan90/Asterhand/internal/model"
)

const maxCatalogBytes = 16 * 1024 * 1024

type Launcher func(Command) error

type Registry struct {
	mu           sync.RWMutex
	path         string
	catalog      Catalog
	loadErr      error
	launch       Launcher
	probes       chan struct{}
	probeMu      sync.Mutex
	statuses     map[string]*fileStatus
	probeQueue   []probeTask
	probeWorkers int
}

// Open isolates invalid configuration to this feature and never overwrites it.
func Open(home string, launch Launcher) *Registry {
	r := &Registry{path: filepath.Join(home, "local-commands.json"), launch: launch,
		catalog: Catalog{Schema: 1, Revision: model.ID(), Commands: []Command{}},
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
		return fmt.Errorf("命令配置读取失败或超过 16 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if decoder.Decode(&catalog) != nil || decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("命令配置格式无效")
	}
	// Normalize loaded definitions before uniqueness checks, just like API edits.
	for i := range catalog.Commands {
		catalog.Commands[i], err = normalize(catalog.Commands[i])
		if err != nil {
			return err
		}
	}
	if err := validateCatalog(catalog); err != nil {
		return err
	}
	r.catalog = catalog
	return nil
}

func cloneCatalog(c Catalog) Catalog {
	c.Commands = slices.Clone(c.Commands)
	for i := range c.Commands {
		c.Commands[i].Aliases = slices.Clone(c.Commands[i].Aliases)
		c.Commands[i].Arguments = slices.Clone(c.Commands[i].Arguments)
	}
	return c
}

func (r *Registry) ready() error {
	if r.loadErr != nil {
		return problem("configuration", "本地命令配置无法读取，请修复节点数据目录中的 local-commands.json 后重启："+r.loadErr.Error())
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
		return problem("invalid", "命令配置超过 16 MiB 或 10000 条")
	}
	if err := config.AtomicFile(r.path, append(b, '\n'), 0600); err != nil {
		return fmt.Errorf("保存命令配置失败：%w", err)
	}
	r.catalog = next
	r.probeMu.Lock()
	r.statuses = map[string]*fileStatus{}
	r.probeMu.Unlock()
	return nil
}

func (r *Registry) Save(change Mutation) (SaveResult, error) {
	command, err := normalize(change.Command)
	if err != nil {
		return SaveResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ready(); err != nil {
		return SaveResult{}, err
	}
	if change.Revision != r.catalog.Revision {
		return SaveResult{}, problem("conflict", "命令配置已更新，请刷新后核对再保存；当前表单已保留")
	}
	next := cloneCatalog(r.catalog)
	index := slices.IndexFunc(next.Commands, func(c Command) bool { return c.ID == command.ID })
	if command.ID != "" && index < 0 {
		return SaveResult{}, problem("not_found", "命令已删除，请刷新配置")
	}
	if index >= 0 {
		next.Commands = slices.Delete(next.Commands, index, index+1)
	}
	match := slices.IndexFunc(next.Commands, func(c Command) bool { return sameLaunch(c, command) })
	for i, other := range next.Commands {
		if i == match {
			continue
		}
		for _, name := range commandNames(command) {
			if containsName(commandNames(other), name) {
				return SaveResult{}, problem("name_conflict", "名称已被其他命令使用："+name)
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
			return SaveResult{}, problem("invalid", "合并后别名超过 16 个，请先整理已有别名")
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
	if err := r.save(next); err != nil {
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
		return Catalog{}, problem("conflict", "命令配置已更新，请刷新后再删除")
	}
	next := cloneCatalog(r.catalog)
	i := slices.IndexFunc(next.Commands, func(c Command) bool { return c.ID == id })
	if i < 0 {
		return Catalog{}, problem("not_found", "命令已删除，请刷新配置")
	}
	next.Commands = slices.Delete(next.Commands, i, i+1)
	if err := r.save(next); err != nil {
		return Catalog{}, err
	}
	return cloneCatalog(r.catalog), nil
}
