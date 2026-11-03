package localcommands

import (
	"slices"
	"strings"

	"github.com/jackgan90/Asterhand/internal/i18n"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

const (
	MaxGraphNodes       = 64
	MaxGraphEdges       = 512
	MaxGraphParallelism = 16
	GraphExecutionSlots = 8
)

type FailurePolicy string

const AbortGraph FailurePolicy = "abort"

// ScriptGraph belongs to one command. Edges express control dependencies only.
type ScriptGraph struct {
	Nodes       []GraphNode `json:"nodes"`
	Edges       []GraphEdge `json:"edges"`
	Parallelism int         `json:"parallelism"`
}

type GraphNode struct {
	ID             string        `json:"id"`
	Kind           string        `json:"kind"`
	Name           string        `json:"name"`
	Script         string        `json:"script,omitempty"`
	Arguments      []string      `json:"arguments,omitempty"`
	Directory      string        `json:"directory,omitempty"`
	TimeoutSeconds *int64        `json:"timeout_seconds,omitempty"`
	FailurePolicy  FailurePolicy `json:"failure_policy,omitempty"`
	X              int           `json:"x"`
	Y              int           `json:"y"`
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func graphProblem(key string, node string) error {
	return problem("invalid_graph", key, map[string]string{"node": node})
}

func cloneGraph(graph *ScriptGraph) *ScriptGraph {
	if graph == nil {
		return nil
	}
	copy := *graph
	copy.Nodes = slices.Clone(graph.Nodes)
	copy.Edges = slices.Clone(graph.Edges)
	for i := range copy.Nodes {
		copy.Nodes[i].Arguments = slices.Clone(copy.Nodes[i].Arguments)
		if value := copy.Nodes[i].TimeoutSeconds; value != nil {
			seconds := *value
			copy.Nodes[i].TimeoutSeconds = &seconds
		}
	}
	return &copy
}

func nodeScript(node GraphNode) ScriptSpec {
	return ScriptSpec{Script: node.Script, Arguments: node.Arguments, Directory: node.Directory}
}

func normalizeGraph(graph *ScriptGraph, policy CommandPolicy) (*ScriptGraph, error) {
	if graph == nil || len(graph.Nodes) < 3 || len(graph.Nodes) > MaxGraphNodes || len(graph.Edges) > MaxGraphEdges {
		return nil, graphProblem("err.graph.size", "")
	}
	copy := cloneGraph(graph)
	if copy.Parallelism == 0 {
		copy.Parallelism = 4
	}
	if copy.Parallelism < 1 || copy.Parallelism > MaxGraphParallelism {
		return nil, graphProblem("err.graph.parallelism", "")
	}
	ids := map[string]bool{}
	for i := range copy.Nodes {
		n := &copy.Nodes[i]
		if !wire.ValidID(n.ID) || ids[n.ID] {
			return nil, graphProblem("err.graph.identity", n.Name)
		}
		ids[n.ID] = true
		if err := normalizeGraphNode(n, policy); err != nil {
			return nil, err
		}
	}
	if err := validateGraphEdges(copy); err != nil {
		return nil, err
	}
	return copy, nil
}

func normalizeGraphNode(n *GraphNode, policy CommandPolicy) error {
	n.Name = strings.TrimSpace(n.Name)
	if !cleanText(n.Name, 128) || n.X < 0 || n.X > 8000 || n.Y < 0 || n.Y > 8000 {
		return graphProblem("err.graph.node", n.Name)
	}
	if n.Kind == "start" || n.Kind == "end" {
		if n.Script != "" || len(n.Arguments) > 0 || n.Directory != "" || n.TimeoutSeconds != nil || n.FailurePolicy != "" {
			return graphProblem("err.graph.node", n.Name)
		}
		return nil
	}
	if n.Kind != "script" || n.Name == "" {
		return graphProblem("err.graph.node", n.Name)
	}
	if n.FailurePolicy == "" {
		n.FailurePolicy = AbortGraph
	}
	if n.FailurePolicy != AbortGraph {
		return graphProblem("err.graph.policy", n.Name)
	}
	if err := validateTimeout(n.TimeoutSeconds); err != nil {
		return err
	}
	command, err := normalizeScript(nodeScript(*n), policy)
	if err != nil {
		if key, args, ok := i18n.ErrorDetails(err); ok {
			if args == nil {
				args = map[string]string{}
			}
			args["graph_node"] = n.Name
			return problem("invalid_graph", key, args)
		}
		return problem("invalid_graph", "err.graph.script", map[string]string{"node": n.Name, "err": err.Error()})
	}
	n.Script, n.Arguments, n.Directory = command.Script, command.Arguments, command.Directory
	return nil
}

func validateGraphEdges(graph *ScriptGraph) error {
	nodes := map[string]GraphNode{}
	forward, backward := map[string][]string{}, map[string][]string{}
	start, end := "", ""
	for _, n := range graph.Nodes {
		nodes[n.ID] = n
		switch n.Kind {
		case "start":
			if start != "" {
				return graphProblem("err.graph.endpoints", "")
			}
			start = n.ID
		case "end":
			if end != "" {
				return graphProblem("err.graph.endpoints", "")
			}
			end = n.ID
		}
	}
	if start == "" || end == "" {
		return graphProblem("err.graph.endpoints", "")
	}
	seen := map[GraphEdge]bool{}
	for _, edge := range graph.Edges {
		if edge.From == edge.To || seen[edge] || nodes[edge.From].ID == "" || nodes[edge.To].ID == "" {
			return graphProblem("err.graph.edge", "")
		}
		seen[edge] = true
		forward[edge.From] = append(forward[edge.From], edge.To)
		backward[edge.To] = append(backward[edge.To], edge.From)
	}
	if len(backward[start]) > 0 || len(forward[end]) > 0 {
		return graphProblem("err.graph.endpoints", "")
	}
	if !acyclic(graph.Nodes, forward, backward) {
		return graphProblem("err.graph.cycle", "")
	}
	fromStart, toEnd := reachable(start, forward), reachable(end, backward)
	for _, n := range graph.Nodes {
		if !fromStart[n.ID] || !toEnd[n.ID] {
			return graphProblem("err.graph.disconnected", n.Name)
		}
	}
	return nil
}

func reachable(root string, edges map[string][]string) map[string]bool {
	seen, queue := map[string]bool{root: true}, []string{root}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, next := range edges[id] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	return seen
}

func acyclic(nodes []GraphNode, forward, backward map[string][]string) bool {
	remaining, queue := map[string]int{}, []string{}
	for _, n := range nodes {
		remaining[n.ID] = len(backward[n.ID])
		if remaining[n.ID] == 0 {
			queue = append(queue, n.ID)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range forward[id] {
			remaining[next]--
			if remaining[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	return visited == len(nodes)
}

// A policy decides the graph's reaction without rewriting execution evidence.
func abortsGraph(policy FailurePolicy, result string) bool {
	return result == "failed" && policy == AbortGraph
}
