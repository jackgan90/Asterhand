package localcommands

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackgan90/Asterhand/internal/model"
)

type graphStarted struct {
	command   ScriptSpec
	id        string
	execution *fakeExecution
}
type graphBackend struct {
	created chan graphStarted
	empty   atomic.Bool
	mu      sync.Mutex
	checked []string
}

func (b *graphBackend) Available() bool { return true }
func (b *graphBackend) Start(c ScriptSpec, id string) (Execution, error) {
	e := newFakeExecution()
	b.created <- graphStarted{c, id, e}
	return e, nil
}
func (b *graphBackend) Reconcile(id string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checked = append(b.checked, id)
	return b.empty.Load(), nil
}

func graphCommand(t *testing.T) Command {
	c := testCommand(t, "graph_command")
	c.ID = model.ID()
	c.Revision = model.ID()
	script := c.Graph.Nodes[1].Script
	c.Graph = &ScriptGraph{Parallelism: 4}
	for _, name := range []string{"start", "prepare", "left", "right", "join", "end"} {
		n := GraphNode{ID: model.ID(), Kind: "script", Name: name, Script: script, Arguments: []string{name}, FailurePolicy: AbortGraph}
		if name == "start" || name == "end" {
			n = GraphNode{ID: n.ID, Kind: name, Name: ""}
		}
		c.Graph.Nodes = append(c.Graph.Nodes, n)
	}
	for _, pair := range [][2]int{{0, 1}, {1, 2}, {1, 3}, {2, 4}, {3, 4}, {4, 5}} {
		c.Graph.Edges = append(c.Graph.Edges, GraphEdge{c.Graph.Nodes[pair[0]].ID, c.Graph.Nodes[pair[1]].ID})
	}
	return c
}
func graphFixture(t *testing.T) (*RunManager, *graphBackend, Command) {
	b := &graphBackend{created: make(chan graphStarted, 128)}
	b.empty.Store(true)
	m := NewRunManager(t.TempDir(), b, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m, b, graphCommand(t)
}
func nextGraphNode(t *testing.T, b *graphBackend) graphStarted {
	t.Helper()
	select {
	case value := <-b.created:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("no scheduled node")
		return graphStarted{}
	}
}
func noGraphStart(t *testing.T, b *graphBackend) {
	t.Helper()
	select {
	case value := <-b.created:
		t.Fatalf("unexpected node %s", value.command.Arguments)
	case <-time.After(40 * time.Millisecond):
	}
}

func TestGraphValidationAndIndependentSnapshots(t *testing.T) {
	c := graphCommand(t)
	mutations := map[string]func(*ScriptGraph){
		"cycle":            func(g *ScriptGraph) { g.Edges = append(g.Edges, GraphEdge{g.Nodes[4].ID, g.Nodes[1].ID}) },
		"disconnected":     func(g *ScriptGraph) { g.Edges = g.Edges[1:] },
		"duplicate":        func(g *ScriptGraph) { g.Edges = append(g.Edges, g.Edges[0]) },
		"missing endpoint": func(g *ScriptGraph) { g.Edges[0].To = model.ID() },
		"two starts":       func(g *ScriptGraph) { g.Nodes[3] = GraphNode{ID: g.Nodes[3].ID, Kind: "start"} },
		"unknown policy":   func(g *ScriptGraph) { g.Nodes[1].FailurePolicy = "ignore" },
		"parallelism":      func(g *ScriptGraph) { g.Parallelism = 17 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copy := cloneGraph(c.Graph)
			mutate(copy)
			if _, err := normalizeGraph(copy, commandPolicy); err == nil {
				t.Fatal("invalid graph accepted")
			}
		})
	}
	r := Open(t.TempDir())
	c.ID = ""
	saved := saveCommand(t, r, c)
	snapshot := mustSnapshot(t, r)
	snapshot.Commands[0].Graph.Nodes[1].Arguments[0] = "changed"
	if mustSnapshot(t, r).Commands[0].Graph.Nodes[1].Arguments[0] == "changed" {
		t.Fatal("snapshot mutated registry")
	}
	copy := c
	copy.Name = "other_graph"
	other := saveCommand(t, r, copy)
	if other.ID == saved.ID {
		t.Fatal("graph nodes merged into another command")
	}
	reopened := Open(filepath.Dir(r.path))
	if len(mustSnapshot(t, reopened).Commands) != 2 {
		t.Fatal("graph did not round trip")
	}
}

func TestGraphDiamondJoinsAndKeepsNodeLogs(t *testing.T) {
	m, b, c := graphFixture(t)
	run, err := m.Start(c)
	if err != nil {
		t.Fatal(err)
	}
	prepare := nextGraphNode(t, b)
	if prepare.command.Arguments[0] != "prepare" {
		t.Fatal(prepare.command)
	}
	noGraphStart(t, b)
	prepare.execution.complete()
	branches := map[string]graphStarted{}
	for range 2 {
		n := nextGraphNode(t, b)
		branches[n.command.Arguments[0]] = n
	}
	if len(branches) != 2 || branches["left"].id == branches["right"].id {
		t.Fatal("node execution identities collided")
	}
	for name, n := range branches {
		io.WriteString(n.execution.outWriter, name+" 中文\n")
		io.WriteString(n.execution.errWriter, name+" diagnostic\n")
	}
	branches["left"].execution.complete()
	noGraphStart(t, b)
	branches["right"].execution.complete()
	join := nextGraphNode(t, b)
	if join.command.Arguments[0] != "join" {
		t.Fatal(join.command)
	}
	join.execution.complete()
	result := awaitFinished(t, m, c.ID)
	if result.Result != "succeeded" || result.Command.Graph != nil {
		t.Fatal(result)
	}
	for _, n := range result.Nodes {
		if n.Result != "succeeded" {
			t.Fatal(n)
		}
	}
	page, err := m.Read(c.ID, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	logs := map[string]string{}
	for _, chunk := range page.Chunks {
		logs[chunk.NodeID] += string(chunk.Data)
	}
	if !strings.Contains(logs[c.Graph.Nodes[2].ID], "left 中文") || !strings.Contains(logs[c.Graph.Nodes[3].ID], "right diagnostic") {
		t.Fatal(logs)
	}
	reopened := NewRunManager(filepath.Dir(m.store.root), b, nil)
	if reopened.loadErr != nil || reopened.Current(c.ID).Result != "succeeded" {
		t.Fatal(reopened.loadErr)
	}
	noGraphStart(t, b)
}

func TestGraphFailureStopsPeerAndBlocksJoin(t *testing.T) {
	m, b, c := graphFixture(t)
	if _, err := m.Start(c); err != nil {
		t.Fatal(err)
	}
	nextGraphNode(t, b).execution.complete()
	first, other := nextGraphNode(t, b), nextGraphNode(t, b)
	first.execution.code = 7
	first.execution.complete()
	result := awaitFinished(t, m, c.ID)
	if result.Result != "failed" || result.Reason != "node_failed" || other.execution.stops.Load() == 0 {
		t.Fatal(result)
	}
	if result.Nodes[4].Result != "skipped" || result.Nodes[5].Result != "skipped" {
		t.Fatal(result.Nodes)
	}
	failed := 0
	for _, n := range result.Nodes {
		if n.Result == "failed" {
			failed++
			if n.NodeID != result.FailedNodeID || n.Exit.Code == nil || *n.Exit.Code != 7 {
				t.Fatal(n)
			}
		}
	}
	if failed != 1 {
		t.Fatal("peer cancellation became another failure", result.Nodes)
	}
	noGraphStart(t, b)
}

func TestGraphStopAndTimeoutDoNotStartQueuedNodes(t *testing.T) {
	for _, reason := range []string{"user_stop", "timeout", "node_timeout"} {
		t.Run(reason, func(t *testing.T) {
			m, b, c := graphFixture(t)
			clock := &fakeClock{wake: make(chan time.Time, 4)}
			m.clock = clock
			c.Graph.Parallelism = 1
			seconds := int64(2)
			if reason == "timeout" {
				c.TimeoutSeconds = &seconds
			}
			if reason == "node_timeout" {
				c.Graph.Nodes[1].TimeoutSeconds = &seconds
			}
			run, err := m.Start(c)
			if err != nil {
				t.Fatal(err)
			}
			running := nextGraphNode(t, b)
			awaitRun(t, m, c.ID, func(r *RunRecord) bool { return r.Nodes[1].Phase == "running" })
			if reason == "user_stop" {
				if _, err = m.Stop(c.ID, run.ID, reason); err != nil {
					t.Fatal(err)
				}
			} else {
				clock.now.Store(int64(3 * time.Second))
				clock.wake <- time.Now()
			}
			result := awaitFinished(t, m, c.ID)
			expected := reason
			if reason == "node_timeout" {
				expected = "node_failed"
			}
			if result.Reason != expected || running.execution.stops.Load() == 0 {
				t.Fatal(result)
			}
			noGraphStart(t, b)
		})
	}
}

func TestGraphGlobalSlotsAreBoundedAcrossCommands(t *testing.T) {
	m, b, c := graphFixture(t)
	c.Graph.Parallelism = 16
	script := c.Graph.Nodes[1].Script
	c.Graph.Nodes = append(c.Graph.Nodes[:1], c.Graph.Nodes[5])
	c.Graph.Edges = nil
	for range 12 {
		n := GraphNode{ID: model.ID(), Kind: "script", Name: "worker", Script: script, FailurePolicy: AbortGraph}
		c.Graph.Nodes = append(c.Graph.Nodes, n)
		c.Graph.Edges = append(c.Graph.Edges, GraphEdge{c.Graph.Nodes[0].ID, n.ID}, GraphEdge{n.ID, c.Graph.Nodes[1].ID})
	}
	first, err := m.Start(c)
	if err != nil {
		t.Fatal(err)
	}
	for range GraphExecutionSlots {
		nextGraphNode(t, b)
	}
	noGraphStart(t, b)
	second := c
	second.ID = model.ID()
	secondRun, err := m.Start(second)
	if err != nil {
		t.Fatal(err)
	}
	noGraphStart(t, b)
	if _, err = m.Stop(second.ID, secondRun.ID, "user_stop"); err != nil {
		t.Fatal(err)
	}
	awaitFinished(t, m, second.ID)
	if _, err = m.Stop(c.ID, first.ID, "user_stop"); err != nil {
		t.Fatal(err)
	}
	awaitFinished(t, m, c.ID)
	noGraphStart(t, b)
	if len(m.graphSlots) != 0 {
		t.Fatal("slots leaked")
	}
}

func TestGraphRecoveryChecksEveryExecutionAndNeverRestarts(t *testing.T) {
	m, b, c := graphFixture(t)
	b.empty.Store(false)
	id := model.ID()
	run := &managedRun{record: RunRecord{Version: 1, ID: id, RoundID: id, Sequence: 1, Command: c, Phase: "running", LogComplete: true}}
	initializeGraph(run)
	finishStructuralNode(&run.record.Nodes[0], "succeeded")
	finishStructuralNode(&run.record.Nodes[1], "succeeded")
	for _, i := range []int{2, 3} {
		run.record.Nodes[i].RunID = model.ID()
		run.record.Nodes[i].Phase = "running"
	}
	f, err := m.store.prepare(run.record)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = m.store.publish(c.ID, runIndex{Version: 1, RoundID: id, Sequence: 1, Latest: id, IDs: []string{id}}); err != nil {
		t.Fatal(err)
	}
	reopened := NewRunManager(filepath.Dir(m.store.root), b, nil)
	if reopened.loadErr != nil {
		t.Fatal(reopened.loadErr)
	}
	if !reopened.Busy(c.ID) {
		t.Fatal("recovered graph released unknown executions")
	}
	if _, err = reopened.Start(c); err == nil {
		t.Fatal("restarted before reconciliation")
	}
	b.empty.Store(true)
	result := awaitFinished(t, reopened, c.ID)
	if result.Result != "interrupted" || result.Nodes[1].Result != "succeeded" || result.Nodes[2].Result != "interrupted" || result.Nodes[4].Result != "skipped" {
		t.Fatal(result)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	checked := map[string]bool{}
	for _, id := range b.checked {
		checked[id] = true
	}
	for _, i := range []int{2, 3} {
		if !checked[run.record.Nodes[i].RunID] {
			t.Fatal("missing recovery identity")
		}
	}
	noGraphStart(t, b)
}

func TestGraphLogPagesSeedEachNodeAndBoundSummary(t *testing.T) {
	m, _, c := graphFixture(t)
	for i := range c.Graph.Nodes {
		if c.Graph.Nodes[i].Kind == "script" {
			c.Graph.Nodes[i].Arguments = []string{strings.Repeat("x", 4000), strings.Repeat("y", 4000)}
		}
	}
	run := &managedRun{record: RunRecord{Version: 1, ID: model.ID(), Command: c, Phase: "finished", Result: "succeeded", LogComplete: true}}
	initializeGraph(run)
	for i := range run.record.Nodes {
		finishStructuralNode(&run.record.Nodes[i], "succeeded")
	}
	f, err := m.store.prepare(run.record)
	if err != nil {
		t.Fatal(err)
	}
	appendGraphLog(f, 2, 0, []byte{0xe4, 0xb8})
	appendGraphLog(f, 3, 0, []byte{0xe6})
	cursor := int64(2*logFrameOverhead + 3)
	appendGraphLog(f, 2, 0, []byte{0xad})
	appendGraphLog(f, 3, 0, []byte{0x96, 0x87})
	f.Close()
	page, err := m.store.read(publicRun(run.record), cursor)
	if err != nil {
		t.Fatal(err)
	}
	seeds := map[string][]byte{}
	for _, chunk := range page.Context["utf-8"] {
		seeds[chunk.NodeID] = chunk.Data
	}
	if string(seeds[c.Graph.Nodes[2].ID]) != string([]byte{0xe4, 0xb8}) || string(seeds[c.Graph.Nodes[3].ID]) != string([]byte{0xe6}) {
		t.Fatal(seeds)
	}
	data, _ := json.Marshal(page)
	if len(data) > 96*1024 || strings.Contains(string(data), strings.Repeat("x", 100)) {
		t.Fatal("configuration leaked into IPC", len(data))
	}
	if _, err = m.store.loadRun(c.ID, run.record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(m.store.logPath(c.ID, run.record.ID)); err != nil {
		t.Fatal(err)
	}
}
