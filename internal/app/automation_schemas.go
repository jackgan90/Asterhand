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
	set("workspaces_get conversations_capabilities", automation.Schema[workspace.Status]())
	set("workspace_files_list workspace_files_search", automation.Schema[workspace.Page]())
	set("workspace_files_read artifacts_read", automation.Schema[workspace.Fragment]())
	set("workspace_files_mutate workspace_versions_get workspace_versions_restore turn_activities_get workspace_operations_get workspace_operations_resolve", automation.Schema[workspace.Operation]())
	set("workspace_versions_list turn_activities_list", resultObject("", "", "next", map[string]any{"items": arrayOf(automation.Schema[workspace.Operation]())}))
	set("artifacts_list", resultObject("", "", "next", map[string]any{"items": arrayOf(automation.Schema[workspace.Artifact]())}))
	set("artifacts_get", automation.Schema[workspace.Artifact]())
	fragment := resultObject("format text offset next", "complete", "", nil)
	set("commands_validate", automation.Schema[localcommands.Command]())
	set("commands_get", map[string]any{"anyOf": []any{automation.Schema[localcommands.Command](), fragment}})
	set("commands_create commands_patch commands_clone commands_delete", automation.Schema[localcommands.CommandReference]())
	set("commands_run runs_get runs_wait", automation.Schema[automation.RunView]())
	set("commands_list commands_search", automation.Schema[localcommands.CommandPage]())
	set("runs_list", resultObject("next", "", "", map[string]any{"items": arrayOf(automation.Schema[localcommands.RunSummary]())}))
	set("logs_read logs_tail logs_wait", automation.Schema[publicLogPage]())
	set("runs_stop", resultObject("command_id", "stop_requested", "", map[string]any{"run_ids": arrayOf(text)}))
	set("operations_get", automation.Schema[operations.Receipt]())
	set("operations_new_id", resultObject("operation_id", "", "", nil))
	set("node_version", resultObject("version api_version platform source_digest", "", "", nil))
	set("node_status node_start node_stop", resultObject("node_id instance_id version", "running stopping", "", nil))
	set("capabilities", resultObject("api_version node_id instance_id caller_id policy_revision", "", "server_time_ms", map[string]any{"roles": arrayOf(text), "toolsets": object, "operations": arrayOf(resultObject("name role toolset", "write batch", "", nil)), "limits": object}))
	set("nodes_get nodes_capabilities", resultObject("node_id name version checked_at automation_api", "cached", "age_seconds", map[string]any{"online": map[string]any{"type": []string{"boolean", "string"}}, "capabilities": object, "projects": arrayOf(object), "task_projects": arrayOf(text)}))
	set("nodes_list", resultObject("next", "", "", map[string]any{"items": arrayOf(object)}))
	chat := resultObject("connection active_conversation_id next", "can_send busy", "", map[string]any{
		"target": automation.Schema[conversation.Target](), "capabilities": automation.Schema[conversation.Capabilities](),
		"conversation": automation.Schema[conversation.Conversation](), "turn": object, "live": automation.Schema[conversation.Message](),
		"page": automation.Schema[conversation.Page](), "fragment": automation.Schema[conversation.Fragment](), "queue": object, "queue_item": object,
	})
	set("conversations_connection conversations_connect conversations_get conversations_create conversations_send turns_get turns_wait turns_stop messages_list messages_get", chat)
	set("conversations_list", resultObject("next", "", "", map[string]any{"items": arrayOf(automation.Schema[conversation.Conversation]())}))
	set("settings_get", resultObject("", "", "", map[string]any{"node": resultObject("revision name provider model", "name_default", "", nil), "desktop": automation.Schema[quickinput.Status](), "desktop_shortcut": automation.Schema[launch.ShortcutStatus](), "notifications": automation.Schema[notifications.Status]()}))
	set("settings_patch models_select", map[string]any{"anyOf": []any{resultObject("revision name provider model", "name_default", "", nil), automation.Schema[quickinput.Status]()}})
	set("settings_check_hotkey desktop_retry", automation.Schema[quickinput.Status]())
	set("settings_desktop_shortcut", automation.Schema[launch.ShortcutStatus]())
	set("models_list", resultObject("selected revision", "configured", "", map[string]any{"provider": object}))
	set("diagnostics_inspect diagnostics_run", resultObject("state operation_id message_key outcome", "", "", nil))
	set("desktop_open", resultObject("view command_id", "", "", nil))
	set("drafts_get", map[string]any{"anyOf": []any{automation.Schema[liveSnapshot](), fragment}})
	set("drafts_update drafts_commit", resultObject("draft_id revision state", "", "", nil))
	set("drafts_list", resultObject("next", "", "unavailable_windows", map[string]any{"items": arrayOf(automation.Schema[liveSnapshot]())}))
	set("batches_submit", resultObject("batch_id", "", "items", nil))
	set("batches_get", resultObject("batch_id next", "complete", "", map[string]any{"items": arrayOf(resultObject("operation_id state", "", "", map[string]any{"receipt": automation.Schema[operations.Receipt]()}))}))
	set("read_many", resultObject("next", "", "", map[string]any{"items": arrayOf(resultObject("", "", "index", map[string]any{"response": automation.Schema[automation.Response]()}))}))
	task := resultObject("id kind origin owner target_node_id project_id request_id submitted_by state attempt created updated sequence execution_outcome verification_outcome user_acceptance remote_id remote_owner", "cancel_requested", "artifact_count", map[string]any{"interaction": map[string]any{"type": []string{"object", "null"}}, "job": object})
	set("tasks_submit tasks_reconcile tasks_resolve", task)
	set("tasks_cancel tasks_answer", map[string]any{"anyOf": []any{task, resultObject("outbox_id answer_id state", "cancel_registered", "", nil)}})
	set("tasks_list", resultObject("next", "", "", map[string]any{"items": arrayOf(task)}))
	set("tasks_get", map[string]any{"anyOf": []any{task, resultObject("text offset next", "complete", "", map[string]any{"items": arrayOf(object)})}})
	set("tasks_events", resultObject("next watermark", "end", "", map[string]any{"event": object, "event_fragment": resultObject("sequence format text offset", "complete", "", nil)}))
	describeWorkerAutomationResults(r)
	describeHarnessAutomationResults(r)
}
