import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import {apiClient} from "../web/api-client.mjs";
import {taskController} from "../web/task-controller.mjs";
import {taskRequestDigest} from "../web/task-admission.mjs";
import {deferred} from "./web-fixture.mjs";

const fixtures = JSON.parse(fs.readFileSync(new URL("./fixtures/task-rejection-requests.json", import.meta.url), "utf8"));
const pendingKey = "asterhand-pending";
const receipt = fixture => ({code:"task_configuration_unavailable", error:"missing model configuration",
  request_id:fixture.request.request_id, request_digest:fixture.digest});

function controller(fixture, response) {
  const scope = {active:true}, values = new Map([[pendingKey, JSON.stringify(fixture.request)]]), writes = [], submitted = [];
  const storage = {getItem:key => values.get(key), setItem:(key,value) => values.set(key,value), removeItem:key => values.delete(key)};
  const client = apiClient({fetch:async (path, options) => {
    if (path === "/api/jobs" && options.method === "POST") {
      const request = JSON.parse(options.body);
      writes.push(request);
      return response(request, writes.length);
    }
    return {ok:true, json:async () => []};
  }});
  const view = {editing:() => false, render() {}, submissionChanged() {}, submitted:goal => submitted.push(goal)};
  let ids = 0;
  const tasks = taskController({...client, view, storage, session:() => scope, selection:() => ({info:{node_id:fixture.local_node_id}}), uuid:() => `new-request-${++ids}`});
  const {goal,target,project_id,target_explicit} = fixture.request;
  return {tasks, values, writes, submitted, scope, content:{goal,target,project_id,target_explicit}};
}

test("browser digest matches Go route normalization, omissions and Unicode/HTML escaping", async () => {
  for (const fixture of fixtures)
    assert.equal(await taskRequestDigest(fixture.request, fixture.local_node_id), fixture.digest);
});

test("definitive refusal preserves the draft and permits a fresh user submission", async () => {
  for (const fixture of fixtures) {
    const ui = controller(fixture, (_, count) => count === 1
      ? {ok:false, status:409, json:async () => receipt(fixture)}
      : {ok:true, json:async () => ({})});
    await assert.rejects(ui.tasks.submit(ui.content), /missing model configuration/);
    assert.equal(ui.values.has(pendingKey), false);
    assert.deepEqual(ui.submitted, []); // No draft-clearing success callback.
    await ui.tasks.submit(ui.content);
    assert.notEqual(ui.writes[1].request_id, fixture.request.request_id);
    assert.deepEqual(ui.submitted, [fixture.request.goal]);
  }
});

for (const kind of ["network", "malformed", "status", "code", "id", "digest", "missing-detail", "detail-object", "detail-array", "detail-boolean", "detail-number"]) {
  test(`unproven refusal (${kind}) keeps the exact pending identity`, async () => {
    const fixture = fixtures[1];
    const ui = controller(fixture, () => {
      if (kind === "network") throw Error("lost response");
      const value = receipt(fixture);
      if (kind === "code") value.code = "future_error";
      if (kind === "id") value.request_id = "different-request";
      if (kind === "digest") value.request_digest = "different-content";
      if (kind === "missing-detail") delete value.error;
      if (kind.startsWith("detail-")) value.error = {"detail-object":{},"detail-array":[],"detail-boolean":true,"detail-number":1}[kind];
      return {ok:false, status:kind === "status" ? 500 : 409, json:async () => {
        if (kind === "malformed") throw Error("truncated JSON");
        return value;
      }};
    });
    await assert.rejects(ui.tasks.submit(ui.content));
    assert.equal(ui.values.get(pendingKey), JSON.stringify(fixture.request));
    await assert.rejects(ui.tasks.submit(ui.content));
    assert.deepEqual(ui.writes[1], ui.writes[0]);
    assert.deepEqual(ui.submitted, []);
  });
}

test("a delayed refusal cannot clear a newer pending draft after pagehide", async () => {
  const fixture = fixtures[0], waiting = deferred();
  const ui = controller(fixture, async () => {
    await waiting.promise;
    return {ok:false, status:409, json:async () => receipt(fixture)};
  });
  const pending = assert.rejects(ui.tasks.submit(ui.content));
  ui.scope.active = false;
  ui.values.set(pendingKey,"newer request");
  waiting.resolve();
  await pending;
  assert.equal(ui.values.get(pendingKey),"newer request");
  assert.deepEqual(ui.submitted,[]);
});
