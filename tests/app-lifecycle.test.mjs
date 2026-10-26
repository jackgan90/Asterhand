import test from "node:test";
import assert from "node:assert/strict";
import {appLifecycle} from "../web/app-lifecycle.mjs";
import {clockFixture, deferred, tick} from "./web-fixture.mjs";
import {noticeView} from "../web/ui.mjs";

test("restart leaves exactly one stream and timer; stopped stream callbacks are inert", async () => {
  const clock = clockFixture(), streams = [], updates = [];
  let reads = 0;
  const life = appLifecycle({clock, client:{stream:() => {
    const source = new EventTarget(); source.close = () => source.closed = true; streams.push(source); return source;
  }}, initialize:async () => {}, refreshPeers:async () => reads++, refreshTasks:async () => reads++, connection:v => updates.push(v), stopped:() => {}, failed:assert.fail});
  await life.start();
  const first = streams[0];
  await life.start();
  assert.equal(first.closed,true);
  assert.equal(clock.timers.size,1);
  first.dispatchEvent(new Event("refresh")); first.onopen(); first.onerror();
  await tick(); assert.equal(reads,0); assert.deepEqual(updates,[]);
  streams[1].dispatchEvent(new Event("refresh")); await clock.fire(20000);
  assert.equal(reads,2);
  life.stop(); assert.equal(streams[1].closed,true); assert.equal(clock.timers.size,0);
});

test("a late initialization cannot attach resources or report a stale error", async () => {
  const pending = deferred(), clock = clockFixture();
  let sources = 0, count = 0;
  const life = appLifecycle({clock, client:{stream:() => { sources++; return {addEventListener() {},close() {}}; }},
    initialize:async () => { if (++count === 1) await pending.promise; }, refreshPeers:() => {}, refreshTasks:() => {}, connection:() => {}, stopped:() => {}, failed:assert.fail});
  const first = life.start(); await life.start(); pending.reject(Error("old failure")); await first;
  assert.equal(sources,1); assert.equal(clock.timers.size,1); life.stop();
});

test("ending the page hides a notice and its next display owns one fresh timer", async () => {
  const clock = clockFixture(), element = {hidden:true};
  const notices = noticeView(element,clock);
  notices.show("saved"); assert.equal(element.hidden,false);
  notices.stop(); await clock.fire(7000);
  assert.equal(element.hidden,true); assert.equal(clock.timers.size,0);
  notices.show("next"); assert.equal(element.hidden,false); assert.equal(clock.timers.size,1);
  await clock.fire(7000); assert.equal(element.hidden,true);
});
