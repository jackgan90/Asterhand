package automation

import (
	"encoding/json"
	"slices"

	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/model"
)

func PatchCommand(c localcommands.Command, patch CommandPatch) (localcommands.Command, error) {
	if patch.Name != nil {
		c.Name = *patch.Name
	}
	if patch.Aliases != nil {
		c.Aliases = slices.Clone(*patch.Aliases)
	}
	if patch.Description != nil {
		c.Description = *patch.Description
	}
	if patch.Concurrent != nil {
		c.AllowConcurrent = *patch.Concurrent
	}
	if len(patch.Timeout) > 0 {
		if err := json.Unmarshal(patch.Timeout, &c.TimeoutSeconds); err != nil {
			return c, Fail("invalid_parameters", "Timeout must be an integer or null")
		}
	}
	if patch.Graph != nil && len(patch.Changes) > 0 {
		return c, Fail("invalid_parameters", "Use either a replacement graph or graph changes")
	}
	if patch.Graph != nil {
		c.Graph = patch.Graph
	}
	if c.Graph == nil || len(patch.Changes) > 1024 {
		return c, Fail("invalid_parameters", "Graph is missing or the change set is too large")
	}
	for _, change := range patch.Changes {
		if err := applyGraphChange(c.Graph, change); err != nil {
			return c, err
		}
	}
	if patch.AutoLayout {
		layoutGraph(c.Graph)
	}
	return c, nil
}

func applyGraphChange(g *localcommands.ScriptGraph, change GraphChange) error {
	index := slices.IndexFunc(g.Nodes, func(n localcommands.GraphNode) bool { return n.ID == change.StepID })
	switch change.Action {
	case "add_step":
		if change.Step == nil || change.StepID != "" || change.Edge != nil || len(change.Order) != 0 {
			return Fail("invalid_parameters", "add_step accepts only step")
		}
		g.Nodes = append(g.Nodes, *change.Step)
	case "replace_step":
		if change.Step == nil || index < 0 || change.Step.ID != change.StepID || change.Edge != nil || len(change.Order) != 0 {
			return Fail("invalid_parameters", "replace_step needs a matching step identity")
		}
		g.Nodes[index] = *change.Step
	case "remove_step":
		if index < 0 || change.Step != nil || change.Edge != nil || len(change.Order) != 0 {
			return Fail("invalid_parameters", "remove_step needs an existing step identity")
		}
		g.Nodes = slices.Delete(g.Nodes, index, index+1)
		g.Edges = slices.DeleteFunc(g.Edges, func(e localcommands.GraphEdge) bool { return e.From == change.StepID || e.To == change.StepID })
	case "connect", "disconnect":
		if change.Edge == nil || change.Step != nil || change.StepID != "" || len(change.Order) != 0 {
			return Fail("invalid_parameters", "Edge changes accept only edge")
		}
		if change.Action == "connect" {
			g.Edges = append(g.Edges, *change.Edge)
		} else {
			g.Edges = slices.DeleteFunc(g.Edges, func(e localcommands.GraphEdge) bool { return e == *change.Edge })
		}
	case "reorder":
		if len(change.Order) != len(g.Nodes) || change.Step != nil || change.StepID != "" || change.Edge != nil {
			return Fail("invalid_parameters", "reorder must name every step exactly once")
		}
		nodes := make([]localcommands.GraphNode, 0, len(g.Nodes))
		seen := map[string]bool{}
		for _, id := range change.Order {
			i := slices.IndexFunc(g.Nodes, func(n localcommands.GraphNode) bool { return n.ID == id })
			if i < 0 || seen[id] {
				return Fail("invalid_parameters", "reorder contains an unknown or duplicate step")
			}
			seen[id] = true
			nodes = append(nodes, g.Nodes[i])
		}
		g.Nodes = nodes
	default:
		return Fail("invalid_parameters", "Unknown graph change action")
	}
	return nil
}

func CloneCommand(c localcommands.Command, name string, aliases []string) localcommands.Command {
	c.ID, c.Revision, c.Name, c.Aliases = "", "", name, aliases
	ids := map[string]string{}
	for i := range c.Graph.Nodes {
		n := &c.Graph.Nodes[i]
		ids[n.ID] = model.ID()
		n.ID = ids[n.ID]
	}
	for i := range c.Graph.Edges {
		edge := &c.Graph.Edges[i]
		edge.From, edge.To = ids[edge.From], ids[edge.To]
	}
	return c
}

// The final domain validation checks cycles and reachability. Layout has a
// bounded number of passes even when the temporary candidate is incomplete.
func layoutGraph(g *localcommands.ScriptGraph) {
	level := map[string]int{}
	for pass := 0; pass < len(g.Nodes); pass++ {
		changed := false
		for _, e := range g.Edges {
			if level[e.To] <= level[e.From] {
				level[e.To] = level[e.From] + 1
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	rows := map[int]int{}
	for i := range g.Nodes {
		n := &g.Nodes[i]
		col := min(level[n.ID], 25)
		n.X, n.Y = 80+col*280, 80+rows[col]*110
		rows[col]++
	}
}
