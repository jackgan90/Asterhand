package quickinput

import (
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/model"
)

func commandTestGraph(script string) *localcommands.ScriptGraph {
	start, step, end := model.ID(), model.ID(), model.ID()
	return &localcommands.ScriptGraph{Parallelism: 1,
		Nodes: []localcommands.GraphNode{{ID: start, Kind: "start"}, {ID: step, Kind: "script", Name: "step", Script: script, FailurePolicy: localcommands.AbortGraph}, {ID: end, Kind: "end"}},
		Edges: []localcommands.GraphEdge{{From: start, To: step}, {From: step, To: end}},
	}
}
