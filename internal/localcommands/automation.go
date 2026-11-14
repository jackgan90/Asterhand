package localcommands

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
	"github.com/jackgan90/Asterhand/internal/operations"
)

type CommandReference struct {
	ID       string `json:"command_id"`
	Revision string `json:"revision,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
}

func (r *Registry) SetJournal(j operations.Journal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.journal = j
	if err := r.completePublication(); err != nil {
		return err
	}
	if r.managed != nil {
		return r.managed.ReconcilePublications(j)
	}
	return nil
}

// The catalog's marker is the publication authority. A subsequent UI or agent
// write cannot erase it until its durable receipt has been completed.
func (r *Registry) completePublication() error {
	p := r.catalog.Publication
	if p == nil {
		return nil
	}
	if r.journal == nil {
		return problem("unavailable", "err.localcmd.config_unreadable")
	}
	receipt, err := r.journal.Lookup(p.Actor, p.Node, p.ID)
	if errors.Is(err, operations.ErrMissing) {
		if at, e := operations.IDTime(p.ID); e == nil && time.Since(at) > operations.Retention {
			return nil
		}
	}
	if err != nil {
		return err
	}
	if receipt.Identity != p.Identity {
		return operations.ErrConflict
	}
	if receipt.Terminal() {
		return nil
	}
	receipt.State, receipt.Accepted, receipt.Result = "completed", true, p.Result
	return r.journal.Finish(receipt, time.Now())
}

func (r *Registry) Get(id string) (Command, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.ready(); err != nil {
		return Command{}, err
	}
	i := slices.IndexFunc(r.catalog.Commands, func(c Command) bool { return c.ID == id })
	if i < 0 {
		return Command{}, problem("not_found", "err.localcmd.deleted_refresh")
	}
	c := r.catalog.Commands[i]
	c.Graph, c.Aliases = cloneGraph(c.Graph), slices.Clone(c.Aliases)
	if c.TimeoutSeconds != nil {
		value := *c.TimeoutSeconds
		c.TimeoutSeconds = &value
	}
	return c, nil
}

func (r *Registry) Validate(command Command) (Command, error) {
	return normalizeWithPolicy(command, r.policy)
}

func (r *Registry) SaveOperation(command Command, revision string, identity operations.Identity) (CommandReference, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, err := r.saveLocked(Mutation{Command: command}, revision, &identity)
	ref := CommandReference{ID: result.ID}
	if result.ID != "" {
		for _, c := range r.catalog.Commands {
			if c.ID == result.ID {
				ref.Revision = c.Revision
				break
			}
		}
	}
	return ref, err
}

func (r *Registry) DeleteOperation(id, revision string, identity operations.Identity) (CommandReference, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	catalog, err := r.deleteLocked(id, revision, &identity)
	ref := CommandReference{}
	if catalog.Revision != "" {
		ref.ID, ref.Deleted = id, true
	}
	return ref, err
}

type CommandPage struct {
	Revision string       `json:"revision"`
	Items    []wire.Entry `json:"items"`
	Next     string       `json:"next,omitempty"`
}

// Page copies summaries, never the full graph catalog. Cursors bind the catalog
// revision, query and permitted identity set in the public adapter.
func (r *Registry) Page(query, revision, after string, limit int, allowed func(string) bool) (CommandPage, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.ready(); err != nil {
		return CommandPage{}, err
	}
	if limit < 1 || limit > 200 {
		return CommandPage{}, problem("invalid", "err.localcmd.limit_exceeded")
	}
	if revision != "" && revision != r.catalog.Revision {
		return CommandPage{}, problem("conflict", "err.localcmd.list_updated")
	}
	indices := []int{}
	for i, c := range r.catalog.Commands {
		if !allowed(c.ID) {
			continue
		}
		if commandScore(c, query) < 4 {
			indices = append(indices, i)
		}
	}
	compare := commandComparator(query)
	slices.SortFunc(indices, func(i, j int) int { return compare(r.catalog.Commands[i], r.catalog.Commands[j]) })
	page := CommandPage{Revision: r.catalog.Revision, Items: []wire.Entry{}}
	passed := after == ""
	for _, index := range indices {
		c := r.catalog.Commands[index]
		if !passed {
			passed = c.ID == after
			continue
		}
		if len(page.Items) >= limit {
			page.Next = page.Items[len(page.Items)-1].ID
			break
		}
		status := r.status(c)
		item := r.managed.Summary(c.ID)
		item.ID, item.Revision, item.Name = c.ID, c.Revision, c.Name
		item.NotifyOnCompletion = c.NotifyOnCompletion.Enabled()
		item.Aliases, item.Description, item.AllowConcurrent = slices.Clone(c.Aliases), c.Description, c.AllowConcurrent
		item.State, item.DetailKey = status.state, status.detailKey
		page.Items = append(page.Items, item)
		b, _ := json.Marshal(page)
		if len(b) > 56<<10 {
			page.Items = page.Items[:len(page.Items)-1]
			if len(page.Items) == 0 {
				return CommandPage{}, problem("invalid", "err.localcmd.limit_exceeded")
			}
			page.Next = page.Items[len(page.Items)-1].ID
			break
		}
	}
	if !passed {
		return CommandPage{}, problem("conflict", "err.localcmd.list_updated")
	}
	return page, nil
}
