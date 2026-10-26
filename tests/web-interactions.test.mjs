import test from "node:test";
import assert from "node:assert/strict";
import {domFixture, clockFixture, tick, descendants, deferred} from "./web-fixture.mjs";

let sequence = 0;
async function page(t, handler = () => undefined) {
  let app;
  const dom = domFixture(), clock = clockFixture(), requests = [], streams = [];
  const info = {node_id:"local", name:"Test node", ready:true, version:"test", projects:[{id:"project",name:"Workspace",root:"test-workspace"}]};
  const storage = new Map();
  const restore = [];
  const globals = {
    document:dom.document, window:new EventTarget(), location:{hash:"",pathname:"/"}, history:{replaceState() {}},
    sessionStorage:{getItem:k => storage.get(k), setItem:(k,v) => storage.set(k,v), removeItem:k => storage.delete(k)},
    FormData:class { constructor(form) { this.form = form; } get(key) { const f = this.form.elements[key]; return f?.checked ? "on" : f?.value || ""; } },
    confirm:() => false, prompt:() => null,
    navigator:{clipboard:{writeText:async () => {}}},
    ...Object.fromEntries(["setTimeout","clearTimeout","setInterval","clearInterval"].map(key => [key, clock[key].bind(clock)])),
    EventSource:class extends EventTarget { constructor() { super(); streams.push(this); } close() { this.closed = true; } },
    fetch:async (path, options = {}) => {
      const body = options.body && JSON.parse(options.body);
      requests.push({path, options, body});
      const result = await handler(path, body, options);
      const data = result ?? ({"/api/session":{csrf:"test-csrf"}, "/api/info":info, "/api/peers":[], "/api/jobs":[], "/api/outbox":[]}[path] || {});
      return {ok:true, json:async () => data};
    },
  };
  for (const [key, value] of Object.entries(globals)) {
    restore.push([key, Object.getOwnPropertyDescriptor(globalThis,key)]);
    Object.defineProperty(globalThis,key,{value, configurable:true,writable:true});
  }
  t.after(() => {
    globals.window.dispatchEvent(new Event("pagehide"));
    app?.dispose();
    for (const [key, descriptor] of restore) descriptor ? Object.defineProperty(globalThis,key,descriptor) : delete globalThis[key];
  });
  ({app} = await import(`../web/app.js?test=${++sequence}`));
  await tick();
  return {...dom, clock, requests, streams, storage, info, app, window:globals.window};
}

test("lost submission response retries the same request ID and target; confirmation clears pending", async t => {
  const submissions = [];
  const ui = await page(t, (path, body) => {
    if (path === "/api/jobs" && body) { submissions.push(body); if (submissions.length === 1) throw Error("lost response"); return {}; }
  });
  ui.select("#goal").value = "create file";
  ui.select("#task-form").fire("submit"); await tick();
  assert.ok(ui.storage.has("asterhand-pending"));
  assert.equal(ui.select("#goal").value, "create file");
  ui.select("#task-form").fire("submit"); await tick();
  assert.deepEqual(submissions[1], submissions[0]);
  assert.equal(submissions[0].target,"local");
  assert.equal(ui.storage.has("asterhand-pending"),false);
  assert.equal(ui.select("#goal").value,"");
});

test("a failed login keeps its input and a successful login clears it", async t => {
  const ui = await page(t, (path,body) => {
    if (path === "/api/login") {
      if (body.token === "bad") throw Error("invalid login");
      return {csrf:"new-session"};
    }
  });
  const form = ui.select("#login-form");
  form.elements.token.value = "bad"; form.fire("submit"); await tick();
  assert.equal(form.elements.token.value,"bad");
  assert.equal(ui.select("#login").hidden,false);
  form.elements.token.value = "good"; form.fire("submit"); await tick();
  assert.equal(form.elements.token.value,"");
  assert.equal(ui.select("#login").hidden,true);
});

test("answer editing pauses refresh and a lost response reuses the answer ID", async t => {
  const answers = [];
  const job = {id:"job", owner:"local", state:"waiting_input", request:{goal:"question"}, interaction:{id:"interaction",attempt:1,state:"pending",question:"answer?"}};
  const ui = await page(t, (path, body) => {
    if (path === "/api/jobs" && !body) return [job];
    if (path.endsWith("/answer")) { answers.push(body); throw Error("lost answer"); }
  });
  const input = descendants(ui.select("#tasks")).find(el => el.tagName === "INPUT");
  const form = descendants(ui.select("#tasks")).find(el => el.tagName === "FORM");
  input.value = "my answer"; input.focus();
  const reads = ui.requests.filter(r => r.path === "/api/jobs").length;
  ui.streams[0].dispatchEvent(new Event("refresh")); await tick();
  assert.equal(ui.requests.filter(r => r.path === "/api/jobs").length,reads);
  assert.equal(input.value,"my answer");
  form.fire("submit"); await tick(); form.fire("submit"); await tick();
  assert.equal(answers.length,2);
  assert.equal(answers[0].answer_id,answers[1].answer_id);
});

test("cancelled stop and reconciliation prompts cause no write", async t => {
  const job = {id:"job", owner:"local", state:"reconciliation_required", request:{goal:"check"}};
  const ui = await page(t, path => path === "/api/jobs" ? [job] : undefined);
  ui.select("#stop-node").fire("click");
  descendants(ui.select("#tasks")).find(el => el.textContent === "核对后关闭").fire("click");
  await tick();
  assert.equal(ui.requests.filter(r => r.options.method === "POST").length,0);
});

test("settings save preserves payload, clears key and reports retained readiness", async t => {
  const ui = await page(t, (path, body) => {
    if (path === "/api/settings" && body) return {ready:true,restart_required:false};
    if (path === "/api/probe") return {state:"not_run"};
  });
  const form = ui.select("#settings-form");
  for (const [key,value] of Object.entries({name:"renamed", endpoint:" https://test.invalid ",peer_listen:" test:1 ",agent_runtime:"openclaw",provider:"deepseek",model:"model",credential_ref:"credential",root:" workspace ",key:"synthetic-test-key"})) form.elements[key].value = value;
  form.fire("submit"); await tick();
  const write = ui.requests.find(r => r.path === "/api/settings");
  assert.equal(write.body.endpoint,"https://test.invalid");
  assert.equal(write.body.root,"workspace");
  assert.equal(write.body.llm.provider,"deepseek");
  assert.equal(form.elements.key.value,"");
  assert.match(ui.select("#settings-status").textContent,/模型验证保持有效/);
});

test("pagehide invalidates in-flight task reads; persisted pageshow restores one subscription", async t => {
  const pending = deferred();
  let delay = false;
  const ui = await page(t, (path, body) => path === "/api/jobs" && !body && delay ? pending.promise : undefined);
  delay = true;
  ui.streams[0].dispatchEvent(new Event("refresh")); await tick();
  const children = ui.select("#tasks").children;
  ui.window.dispatchEvent(new Event("pagehide"));
  pending.resolve([{id:"late",owner:"local",state:"running",request:{goal:"stale task"}}]); await tick();
  assert.equal(ui.select("#tasks").children, children);
  assert.equal(ui.streams[0].closed,true);
  assert.equal(ui.clock.timers.size,0);
  delay = false;
  const shown = new Event("pageshow"); shown.persisted = true;
  ui.window.dispatchEvent(shown); ui.window.dispatchEvent(shown); await tick();
  assert.equal(ui.streams.filter(stream => !stream.closed).length,1);
  assert.equal([...ui.clock.timers.values()].filter(timer => timer.interval).length,1);
});

test("an answer focused while a task read is in flight is retained", async t => {
  const pending = deferred(); let delayed = false;
  const job = {id:"job", owner:"local",state:"waiting_input",request:{goal:"question"},interaction:{id:"i",attempt:1,state:"pending",question:"?"}};
  const ui = await page(t, (path,body) => path === "/api/jobs" && !body ? delayed ? pending.promise : [job] : undefined);
  const input = descendants(ui.select("#tasks")).find(el => el.tagName === "INPUT");
  delayed = true; ui.streams[0].dispatchEvent(new Event("refresh")); await tick();
  input.value = "keep draft"; input.focus(); pending.resolve([]); await tick();
  assert.ok(descendants(ui.select("#tasks")).includes(input));
  assert.equal(input.value,"keep draft");
  input.blur(); delayed = false; ui.streams[0].dispatchEvent(new Event("refresh")); await tick();
  assert.notEqual(descendants(ui.select("#tasks")).find(el => el.tagName === "INPUT"),input);
});

test("stopping subscriptions retains an unknown write and never resends it", async t => {
  const pending = deferred();
  const ui = await page(t, (path,body) => path === "/api/jobs" && body ? pending.promise : undefined);
  ui.select("#goal").value = "write"; ui.select("#task-form").fire("submit"); await tick();
  ui.window.dispatchEvent(new Event("pagehide"));
  assert.ok(ui.storage.has("asterhand-pending"));
  pending.reject(Error("lost write response")); await tick();
  await ui.app.start();
  assert.equal(ui.requests.filter(request => request.body?.goal === "write").length,1);
  assert.ok(ui.storage.has("asterhand-pending"));
  assert.equal(ui.select("#goal").value,"write");
});
