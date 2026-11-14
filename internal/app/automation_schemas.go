package app

import (
	"strings"

	"github.com/jackgan90/Asterhand/internal/automation"
	"github.com/jackgan90/Asterhand/internal/conversation"
	"github.com/jackgan90/Asterhand/internal/launch"
	"github.com/jackgan90/Asterhand/internal/localcommands"
	"github.com/jackgan90/Asterhand/internal/notifications"
	"github.com/jackgan90/Asterhand/internal/operations"
	"github.com/jackgan90/Asterhand/internal/quickinput"
	"github.com/jackgan90/Asterhand/internal/workspace"
)

func resultObject(text, flags, numbers string, extra map[string]any) map[string]any {
	props := map[string]any{}
	for _, group := range []struct{ names, kind string }{{text, "string"}, {flags, "boolean"}, {numbers, "integer"}} {
		for _, name := range strings.Fields(group.names) {
			props[name] = map[string]any{"type": group.kind}
		}
	}
	for name, schema := range extra {
		props[name] = schema
	}
	return map[string]any{"type": "object", "properties": props}
}
func arrayOf(schema map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": schema}
}

func describeAutomationResults(r *automation.Registry) {
	set := func(names string, schema map[string]any) {
		for _, name := range strings.Fields(names) {
			r.DescribeData(name, schema)
		}
	}
	text := map[string]any{"type": "string"}
	object := map[string]any{"type": "object"}
	set("workspaces.get conversations.capabilities", automation.Schema[workspace.Status]())
	set("workspace_files.list workspace_files.search", automation.Schema[workspace.Page]())
	set("workspace_files.read artifacts.read", automation.Schema[workspace.Fragment]())
	set("workspace_files.mutate workspace_versions.get workspace_versions.restore turn_activities.get workspace_operations.get workspace_operations.resolve", automation.Schema[workspace.Operation]())
	set("workspace_versions.list turn_activities.list", resultObject("", "", "next", map[string]any{"items": arrayOf(automation.Schema[workspace.Operation]())}))
	set("artifacts.list", resultObject("", "", "next", map[string]any{"items": arrayOf(automation.Schema[workspace.Artifact]())}))
	set("artifacts.get", automation.Schema[workspace.Artifact]())
	fragment := resultObject("format text offset next", "complete", "", nil)
	set("commands.validate", automation.Schema[localcommands.Command]())
	set("commands.get", map[string]any{"anyOf": []any{automation.Schema[localcommands.Command](), fragment}})
	set("commands.create commands.patch commands.clone commands.delete", automation.Schema[localcommands.CommandReference]())
	set("commands.run runs.get runs.wait", automation.Schema[automation.RunView]())
	set("commands.list commands.search", automation.Schema[localcommands.CommandPage]())
	set("runs.list", resultObject("next", "", "", map[string]any{"items": arrayOf(automation.Schema[localcommands.RunSummary]())}))
	set("logs.read logs.tail logs.wait", automation.Schema[publicLogPage]())
	set("runs.stop", resultObject("command_id", "stop_requested", "", map[string]any{"run_ids": arrayOf(text)}))
	set("operations.get", automation.Schema[operations.Receipt]())
	set("operations.new_id", resultObject("operation_id", "", "", nil))
	set("node.version", resultObject("version api_version platform source_digest", "", "", nil))
	set("node.status node.start node.stop", resultObject("node_id instance_id version", "running stopping", "", nil))
	set("capabilities", resultObject("api_version node_id instance_id", "", "server_time_ms", map[string]any{"operations": arrayOf(resultObject("name role", "write batch", "", nil)), "limits": object}))
	set("nodes.get nodes.capabilities", resultObject("node_id name version checked_at automation_api", "cached", "age_seconds", map[string]any{"online": map[string]any{"type": []string{"boolean", "string"}}, "capabilities": object, "projects": arrayOf(object), "task_projects": arrayOf(text)}))
	set("nodes.list", resultObject("next", "", "", map[string]any{"items": arrayOf(object)}))
	chat := resultObject("connection active_conversation_id next", "can_send busy", "", map[string]any{
		"target": automation.Schema[conversation.Target](), "capabilities": automation.Schema[conversation.Capabilities](),
		"conversation": automation.Schema[conversation.Conversation](), "turn": object, "live": automation.Schema[conversation.Message](),
		"page": automation.Schema[conversation.Page](), "fragment": automation.Schema[conversation.Fragment](), "queue": object, "queue_item": object,
	})
	set("conversations.connection conversations.connect conversations.get conversations.create conversations.send turns.get turns.wait turns.stop messages.list messages.get", chat)
	set("conversations.list", resultObject("next", "", "", map[string]any{"items": arrayOf(automation.Schema[conversation.Conversation]())}))
	set("settings.get", resultObject("", "", "", map[string]any{"node": resultObject("revision name provider model", "name_default", "", nil), "desktop": automation.Schema[quickinput.Status](), "desktop_shortcut": automation.Schema[launch.ShortcutStatus](), "notifications": automation.Schema[notifications.Status]()}))
	set("settings.patch models.select", map[string]any{"anyOf": []any{resultObject("revision name provider model", "name_default", "", nil), automation.Schema[quickinput.Status]()}})
	set("settings.check_hotkey desktop.retry", automation.Schema[quickinput.Status]())
	set("settings.desktop_shortcut", automation.Schema[launch.ShortcutStatus]())
	set("models.list", resultObject("selected revision", "configured", "", map[string]any{"provider": object}))
	set("diagnostics.inspect diagnostics.run", resultObject("state operation_id message_key outcome", "", "", nil))
	set("desktop.open", resultObject("view command_id", "", "", nil))
	set("drafts.get", map[string]any{"anyOf": []any{automation.Schema[liveSnapshot](), fragment}})
	set("drafts.update drafts.commit", resultObject("draft_id revision state", "", "", nil))
	set("drafts.list", resultObject("next", "", "unavailable_windows", map[string]any{"items": arrayOf(automation.Schema[liveSnapshot]())}))
	set("batches.submit", resultObject("batch_id", "", "items", nil))
	set("batches.get", resultObject("batch_id next", "complete", "", map[string]any{"items": arrayOf(resultObject("operation_id state", "", "", map[string]any{"receipt": automation.Schema[operations.Receipt]()}))}))
	set("read.many", resultObject("next", "", "", map[string]any{"items": arrayOf(resultObject("", "", "index", map[string]any{"response": automation.Schema[automation.Response]()}))}))
	task := resultObject("id kind origin owner target_node_id project_id request_id submitted_by state attempt created updated sequence execution_outcome verification_outcome user_acceptance remote_id remote_owner", "cancel_requested", "artifact_count", map[string]any{"interaction": map[string]any{"type": []string{"object", "null"}}, "job": object})
	set("tasks.submit tasks.reconcile tasks.resolve", task)
	set("tasks.cancel tasks.answer", map[string]any{"anyOf": []any{task, resultObject("outbox_id answer_id state", "cancel_registered", "", nil)}})
	set("tasks.list", resultObject("next", "", "", map[string]any{"items": arrayOf(task)}))
	set("tasks.get", map[string]any{"anyOf": []any{task, resultObject("text offset next", "complete", "", map[string]any{"items": arrayOf(object)})}})
	set("tasks.events", resultObject("next watermark", "end", "", map[string]any{"event": object, "event_fragment": resultObject("sequence format text offset", "complete", "", nil)}))
}
