import test from "node:test";
import assert from "node:assert/strict";
import {apiClient} from "../web/api-client.mjs";
import {deferred, tick} from "./web-fixture.mjs";

test("session CSRF is instance-local; login and writes retain existing fields", async () => {
  const calls = [];
  const client = apiClient({fetch:async (path, options) => {
    calls.push({path, options});
    return {ok:true, json:async () => ({csrf:"session-token"})};
  }});
  await client.authenticate("login-token");
  await client.post("/api/jobs", {request_id:"unchanged"});
  assert.deepEqual(JSON.parse(calls[0].options.body), {token:"login-token"});
  assert.equal(calls[1].options.headers["X-Asterhand-CSRF"],"session-token");
  assert.deepEqual(JSON.parse(calls[1].options.body), {request_id:"unchanged"});
  const other = apiClient({fetch:async (_, options) => {
    assert.equal(options.headers["X-Asterhand-CSRF"],"");
    return {ok:true,json:async () => ({})};
  }});
  await other.api("/api/info");
});

for (const status of [401,403,409,500]) test(`HTTP ${status} propagates message and status without retry`, async () => {
  let calls = 0;
  const client = apiClient({fetch:async () => { calls++; return {ok:false,status,json:async () => ({error:"failure"})}; }});
  await assert.rejects(client.post("/api/jobs"), error => error.status === status && error.message === "failure");
  assert.equal(calls,1);
});

test("network failure never retries a write or allows a remote endpoint", async () => {
  let calls = 0;
  const client = apiClient({fetch:async () => {
    calls++;
    throw Error("network");
  }});
  await assert.rejects(client.post("/api/jobs"), /network/);
  assert.equal(calls,1);
  await assert.rejects(client.api("https://remote.invalid/api/jobs"));
  assert.equal(calls,1);
});

test("login transactions serialize Cookie/CSRF updates even when the old UI session ends", async () => {
  const replies = new Map(), calls = [];
  let cookie, oldActive = true;
  const client = apiClient({fetch:async (path, options) => {
    calls.push(path);
    if (path === "/api/login") {
      const token = JSON.parse(options.body).token, reply = deferred();
      replies.set(token,reply);
      await reply.promise;
      cookie = token;
      return {ok:true,json:async () => ({csrf:token})};
    }
    assert.equal(options.headers["X-Asterhand-CSRF"],cookie);
    return {ok:true,json:async () => ({})};
  }});
  const older = client.authenticate("old", () => oldActive);
  await tick(); oldActive = false;
  const newer = client.authenticate("new");
  const write = client.post("/api/jobs", {request_id:"same"});
  await tick();
  assert.deepEqual(calls,["/api/login"]);
  replies.get("old").resolve(); await older; await tick();
  assert.deepEqual(calls,["/api/login","/api/login"]);
  replies.get("new").resolve(); await newer; await write;
  assert.equal(cookie,"new");
  assert.deepEqual(calls,["/api/login","/api/login","/api/jobs"]);
});

test("failed authentication releases the queue and a queued obsolete session does not login", async () => {
  const pending = deferred(), calls = [];
  let active = true;
  const client = apiClient({fetch:async (path, options) => {
    const token = JSON.parse(options.body).token; calls.push(token);
    if (token === "failed") await pending.promise;
    return {ok:true,json:async () => ({csrf:token})};
  }});
  const first = client.authenticate("failed");
  const rejected = assert.rejects(first,/network/);
  await tick();
  const obsolete = client.authenticate("obsolete", () => active);
  const latest = client.authenticate("latest");
  active = false; pending.reject(Error("network"));
  await rejected; await obsolete; await latest;
  assert.deepEqual(calls,["failed","latest"]);
});
