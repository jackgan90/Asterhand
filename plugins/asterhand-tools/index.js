// Credentials and job identity come from the supervisor, never from model arguments.
const string = { type: "string" };
const tools = [
  [
    "asterhand_create_text",
    "Create a UTF-8 text file inside the job's authorized project. Relative path only; refuses overwrite. Content is exact, without adding newline or BOM.",
    { path: string, content: string },
    ["path", "content"],
    "execute",
  ],
  [
    "asterhand_read_text",
    "Read an existing UTF-8 text file inside the job's authorized project; relative path only.",
    { path: string },
    ["path"],
    "execute",
  ],
  [
    "asterhand_ask_user",
    "Ask the human a question and wait for their answer before continuing. Use when filename or other required information is missing, or when explicitly requested to ask first. This does not grant new permissions.",
    { question: string },
    ["question"],
    "both",
  ],
  [
    "asterhand_submit",
    "Submit the user's full goal to a known execution node and its authorized project. Use the exact node ID from the supplied node list. Honor an explicit UI selection; when the UI is only the default, resolve an explicitly named node from the request. Never execute remote work locally. Preserve requested file content.",
    { goal: string, target_id: string, project_id: string },
    ["goal", "target_id", "project_id"],
    "route",
  ],
  [
    "asterhand_probe",
    "Perform a harmless runtime readiness check. Call once when asked to verify tools.",
    {},
    [],
    "probe",
  ],
];

export default {
  id: "asterhand-tools",
  name: "Asterhand tools",
  register(api) {
    const mode = process.env.ASTERHAND_MODE || "execute";
    if (!["execute", "route", "probe"].includes(mode))
      throw new Error("Unsupported Asterhand execution mode");
    for (const [name, description, properties, required, kind] of tools) {
      if (kind !== mode && !(kind === "both" && mode !== "probe")) continue;
      api.registerTool(
        {
          name,
          description,
          parameters: {
            type: "object",
            properties,
            required,
            additionalProperties: false,
          },
          async execute(id, args) {
            const endpoint = process.env.ASTERHAND_CALLBACK;
            const token = process.env.ASTERHAND_TOOL_TOKEN;
            if (!endpoint || !token)
              throw new Error("Asterhand supervisor is required");
            const result = await fetch(endpoint, {
              method: "POST",
              headers: {
                "Content-Type": "application/json",
                Authorization: `Bearer ${token}`,
              },
              body: JSON.stringify({
                operation_id: id,
                tool: name,
                arguments: args,
              }),
            });
            const details = await result.json();
            if (!result.ok)
              throw new Error(details.error || "Asterhand tool failed");
            return {
              content: [{ type: "text", text: JSON.stringify(details) }],
              details,
            };
          },
        },
        { name, optional: true, workspaceAccess: false },
      );
    }
  },
};
