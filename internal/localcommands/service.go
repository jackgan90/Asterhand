package localcommands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

type fileStatus struct {
	state, detail string
	checked       time.Time
	busy          bool
}

type probeTask struct {
	command Command
	status  *fileStatus
}

func inspect(c Command) fileStatus {
	f, err := os.Stat(c.Script)
	if errors.Is(err, os.ErrNotExist) {
		return fileStatus{state: "missing", detail: "命令脚本缺失，请更新配置"}
	}
	if err != nil || !f.Mode().IsRegular() {
		return fileStatus{state: "unavailable", detail: "命令脚本不可访问或不是普通文件"}
	}
	f, err = os.Stat(c.WorkingDirectory())
	if err != nil || !f.IsDir() {
		return fileStatus{state: "directory_missing", detail: "工作目录不存在或不可访问，请更新配置"}
	}
	return fileStatus{state: "ready"}
}

// Cached probes never block the caller or start an unbounded number of workers.
func (r *Registry) status(c Command) fileStatus {
	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	key := c.ID + c.Revision
	status := r.statuses[key]
	if status == nil {
		status = &fileStatus{state: "checking", detail: "正在检查脚本"}
		r.statuses[key] = status
	}
	if !status.busy && time.Since(status.checked) > 2*time.Second {
		status.busy = true
		r.probeQueue = append(r.probeQueue, probeTask{c, status})
		if r.probeWorkers < 4 {
			r.probeWorkers++
			go r.probeWorker()
		}
	}
	return *status
}

// FIFO scanning prevents the first page's expired cache entries from starving
// commands farther down the list. Workers disappear when the queue is drained.
func (r *Registry) probeWorker() {
	for {
		r.probeMu.Lock()
		if len(r.probeQueue) == 0 {
			r.probeWorkers--
			r.probeMu.Unlock()
			return
		}
		task := r.probeQueue[0]
		r.probeQueue[0] = probeTask{}
		r.probeQueue = r.probeQueue[1:]
		r.probeMu.Unlock()
		r.probes <- struct{}{}
		result := inspect(task.command)
		<-r.probes
		r.probeMu.Lock()
		*task.status = result
		task.status.checked = time.Now()
		r.probeMu.Unlock()
	}
}

func (r *Registry) List(request wire.Request) wire.Response {
	catalog, err := r.Snapshot()
	if err != nil {
		return Failure(err)
	}
	if request.Catalog != "" && request.Catalog != catalog.Revision {
		return Failure(problem("conflict", "命令列表已更新，请重新读取"))
	}
	slices.SortFunc(catalog.Commands, func(a, b Command) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	if request.Offset > len(catalog.Commands) {
		return Failure(problem("conflict", "命令列表已更新，请重新读取"))
	}
	end := min(request.Offset+32, len(catalog.Commands))
	reply := wire.Response{Catalog: catalog.Revision, Entries: []wire.Entry{}}
	for _, c := range catalog.Commands[request.Offset:end] {
		status := r.status(c)
		reply.Entries = append(reply.Entries, wire.Entry{ID: c.ID, Revision: c.Revision, Name: c.Name,
			Aliases: c.Aliases, Description: c.Description, State: status.state, Detail: status.detail})
		encoded, _ := json.Marshal(reply)
		if len(encoded) > 96*1024 {
			reply.Entries = reply.Entries[:len(reply.Entries)-1]
			end = request.Offset + len(reply.Entries)
			break
		}
	}
	if end < len(catalog.Commands) {
		reply.Next = end
	}
	return reply
}

func (r *Registry) Launch(ctx context.Context, id, revision string) wire.Response {
	catalog, err := r.Snapshot()
	if err != nil {
		return Failure(err)
	}
	i := slices.IndexFunc(catalog.Commands, func(c Command) bool { return c.ID == id })
	if i < 0 {
		return Failure(problem("not_found", "命令已删除，请刷新列表"))
	}
	command := catalog.Commands[i]
	if command.Revision != revision {
		return Failure(problem("conflict", "命令配置已变化，请刷新列表后重新选择"))
	}
	if err := r.check(ctx, command); err != nil {
		return Failure(err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	// Do not launch a new configuration after a slow filesystem check.
	if !slices.ContainsFunc(r.catalog.Commands, func(c Command) bool { return c.ID == id && c.Revision == revision }) {
		return Failure(problem("conflict", "命令配置已变化，请刷新列表后重新选择"))
	}
	if ctx.Err() != nil {
		return Failure(problem("unavailable", "启动前检查超时，请重试"))
	}
	if r.launch == nil {
		return Failure(problem("unsupported", "当前平台不支持本地快捷命令"))
	}
	if err := r.launch(command); err != nil {
		return Failure(err)
	}
	return wire.Response{Started: true}
}

func (r *Registry) check(ctx context.Context, c Command) error {
	select {
	case r.probes <- struct{}{}:
	case <-ctx.Done():
		return problem("unavailable", "文件检查超时，请检查磁盘状态后重试")
	}
	done := make(chan fileStatus, 1)
	go func() { defer func() { <-r.probes }(); done <- inspect(c) }()
	select {
	case status := <-done:
		if status.state != "ready" {
			return problem(status.state, status.detail)
		}
		return nil
	case <-ctx.Done():
		return problem("unavailable", "文件检查超时，请检查磁盘状态后重试")
	}
}

func Failure(err error) wire.Response {
	var public *wire.Error
	if !errors.As(err, &public) {
		public = &wire.Error{Code: "unavailable", Message: err.Error()}
	}
	return wire.Response{Error: public}
}
