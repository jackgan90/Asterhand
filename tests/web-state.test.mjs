import test from "node:test";
import assert from "node:assert/strict";
import { taskViews, submissionState } from "../web/view-model.mjs";

const job = (id, created, owner = "A") => ({id, created, owner, state:"succeeded", request:{goal:id}});
test("new remote task stays first across routing, delivery, completion and refresh", () => {
  const old = Array.from({length:18}, (_, i) => job(`old-${i}`, "2026-10-24T01:00:00Z"));
  const route = job("route", "2026-10-25T01:00:00Z");
  route.request.goal = "original user goal";
  assert.equal(taskViews([...old,route],[],"A")[0].id,"route");
  for (const state of [null,"running","succeeded"]) {
    const execution = state ? {...job("execute","2026-10-25T01:00:03Z","B"),state} : null;
    const out = {id:"route",peer_id:"B",job:execution,request:{goal:"rewritten"},updated:"2026-10-25T02:00:00Z"};
    const views = taskViews([...old,route],[out],"A");
    assert.equal(views.length,19);
    assert.equal(views[0].id,"route");
    assert.equal(views[0].created,route.created);
    assert.equal(views[0].goal,"original user goal");
  }
});
test("local route and execution yield one stable card and local actions", () => {
  const route=job("route","2026-10-25T01:00:00Z"), execution=job("execute","2026-10-25T01:00:02Z");
  const views=taskViews([route,execution],[{id:route.id,peer_id:"A",job:execution,request:route.request}],"A");
  assert.equal(views.length,1);
  assert.equal(views[0].job.id,"execute");
  assert.equal(views[0].out,null);
});
test("late updates of old remote tasks do not reorder original submissions", () => {
  const first=job("first","2026-10-24T01:00:00Z"), second=job("second","2026-10-25T01:00:00Z");
  const views=taskViews([first,second],[{id:first.id,peer_id:"B",job:job("remote",first.created,"B"),request:first.request,updated:"2026-10-26T01:00:00Z"}],"A");
  assert.deepEqual(views.map(v=>v.id),["second","first"]);
});
test("local execution uses its current state instead of an older outbox snapshot", () => {
  const route=job("route","2026-10-25T01:00:00Z"), current=job("execute",route.created);
  const out={id:route.id,peer_id:"A",job:{...current,state:"running"},request:route.request};
  const views=taskViews([route,current],[out],"A");
  assert.equal(views[0].job.state,"succeeded");
});
test("submission explains disabled prerequisites and remains busy across refresh", () => {
  const ready={ready:true}, project={id:"workspace"};
  assert.equal(submissionState(ready,ready,project,"goal",false).disabled,false);
  for (const args of [[{},ready,project,"goal"],[ready,{},project,"goal"],[ready,ready,null,"goal"],[ready,ready,project," "]]) {
    const state=submissionState(...args,false);
    assert.equal(state.disabled,true);
    assert.equal(state.busy,false);
    assert.ok(state.reason);
  }
  const busy=submissionState(ready,ready,project,"goal",true);
  assert.equal(busy.busy,true);
  assert.equal(busy.disabled,true);
});
