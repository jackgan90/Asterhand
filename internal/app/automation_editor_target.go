package app

import (
	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/drafts"
	"github.com/jackgan90/Asterhand/internal/localcommands/wire"
)

func (a *App) validateLiveTarget(kind string, t drafts.Target) error {
	if t.Kind != "" && t.Kind != kind {
		return automation.Fail("invalid_parameters", "Mismatched draft target")
	}
	want := drafts.Target{Kind: kind, Node: t.Node, Baseline: t.Baseline}
	if t.Node == "" || len(t.Baseline) > 128 {
		return automation.Fail("invalid_parameters", "Invalid draft target or baseline")
	}
	switch kind {
	case "command":
		want.Command = t.Command
		if t.Command != "" && !wire.ValidID(t.Command) || t.Command != "" && t.Baseline == "" {
			return automation.Fail("invalid_parameters", "Existing commands require an identity and baseline")
		}
	case "chat":
		want.Conversation, want.Role = t.Conversation, t.Role
		if t.Conversation != "" && !conversation.ValidID(t.Conversation) || t.Role != conversation.DefaultRole {
			return automation.Fail("invalid_parameters", "Invalid conversation target")
		}
	case "settings":
		want.Settings = t.Settings
		if t.Baseline == "" || t.Settings != "node" && t.Settings != "desktop" {
			return automation.Fail("invalid_parameters", "Settings require an object and baseline")
		}
	case "task_goal":
		want.Project = t.Project
		if t.Project == "" {
			return automation.Fail("invalid_parameters", "Task project is required")
		}
	case "task_answer":
		want.Project, want.Task, want.TaskKind, want.Attempt, want.Interaction = t.Project, t.Task, t.TaskKind, t.Attempt, t.Interaction
		if t.Project == "" || t.Task == "" || t.Attempt == "" || t.Interaction == "" || t.TaskKind != "job" && t.TaskKind != "outbox" {
			return automation.Fail("invalid_parameters", "Task answer must identify the exact attempt and question")
		}
	default:
		return automation.Fail("invalid_parameters", "Unknown draft type")
	}
	t.Kind = kind
	if t != want {
		return automation.Fail("invalid_parameters", "Draft target contains fields belonging to another operation")
	}
	return nil
}
