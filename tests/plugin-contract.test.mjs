import assert from "node:assert/strict";
import { test } from "node:test";
import plugin from "../plugins/asterhand-tools/index.js";

test("tool registration matches the supervisor modes", () => {
  const expected = {
    execute: [
      "asterhand_create_text",
      "asterhand_read_text",
      "asterhand_ask_user",
    ],
    route: ["asterhand_ask_user", "asterhand_submit"],
    probe: ["asterhand_probe"],
  };
  for (const [mode, names] of Object.entries(expected)) {
    process.env.ASTERHAND_MODE = mode;
    const registered = [];
    plugin.register({
      registerTool(tool, options) {
        registered.push(tool.name);
        assert.equal(options.workspaceAccess, false);
        assert.equal(tool.parameters.additionalProperties, false);
      },
    });
    assert.deepEqual(registered, names);
  }
  process.env.ASTERHAND_MODE = "unknown";
  assert.throws(
    () =>
      plugin.register({
        registerTool() {
          assert.fail("registered unknown-mode tool");
        },
      }),
    /Unsupported/,
  );
});

test("tool callback uses supervisor credentials and preserves call identity", async () => {
  process.env.ASTERHAND_MODE = "execute";
  process.env.ASTERHAND_CALLBACK = "http://127.0.0.1:17432/internal/tool";
  process.env.ASTERHAND_TOOL_TOKEN = "synthetic-token";
  let tool;
  plugin.register({
    registerTool(t) {
      if (t.name === "asterhand_create_text") tool = t;
    },
  });
  const originalFetch = globalThis.fetch;
  try {
    globalThis.fetch = async (url, request) => {
      assert.equal(url, process.env.ASTERHAND_CALLBACK);
      assert.equal(request.headers.Authorization, "Bearer synthetic-token");
      assert.deepEqual(JSON.parse(request.body), {
        operation_id: "call-1",
        tool: "asterhand_create_text",
        arguments: { path: "hello.txt", content: "hello world" },
      });
      return { ok: true, json: async () => ({ size: 11 }) };
    };
    assert.deepEqual(
      (
        await tool.execute("call-1", {
          path: "hello.txt",
          content: "hello world",
        })
      ).details,
      { size: 11 },
    );
    globalThis.fetch = async () => ({
      ok: false,
      json: async () => ({ error: "rejected" }),
    });
    await assert.rejects(tool.execute("call-2", {}), /rejected/);
    delete process.env.ASTERHAND_TOOL_TOKEN;
    await assert.rejects(tool.execute("call-3", {}), /supervisor/);
  } finally {
    globalThis.fetch = originalFetch;
  }
});
