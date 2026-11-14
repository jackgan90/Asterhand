import test from 'node:test';
import assert from 'node:assert/strict';
import {notificationView, notificationText} from '../internal/quickinput/assets/notification.mjs';
import {runLogSelection} from '../internal/quickinput/assets/log-graph.mjs';
import {domFixture} from './web-fixture.mjs';

test('notification uses plain text, stable actions and preserves identity across appearance updates', () => {
  const dom=domFixture(), sent=[],frames=[];let receive;
  dom.select('#notification').getBoundingClientRect=()=>({height:101.25});
  const bridge={send:m=>sent.push(m),subscribe:fn=>{receive=fn}};
  notificationView({bridge,document:dom.document,frame:fn=>frames.push(fn)});
  receive({kind:'init',generation:'surface',theme:'star',locale:'en-US'});
  const card={id:1,run_id:'original-run',name:'<img src=x>',result:'succeeded',elapsed_seconds:2,started_at:'2026-11-14T00:00:00Z'};
  receive({kind:'notification',card,layout:7,theme:'star',locale:'en-US'});
  while(frames.length)frames.shift()();
  assert.deepEqual(sent.at(-1),{version:1,kind:'rendered',generation:'surface',id:1,layout:7,height:102});
  assert.equal(dom.select('#notification-name').textContent,card.name);
  assert.ok(dom.select('#notification').getAttribute('aria-label').includes('original-run'));
  dom.select('#notification').fire('pointerenter');assert.equal(sent.at(-1).paused,true);
  receive({kind:'notification',card,theme:'frost',locale:'zh-CN'});
  dom.select('#notification-open').fire('click');assert.deepEqual(sent.at(-1),{version:1,kind:'activate',generation:'surface',id:1});
  dom.select('#notification-close').fire('click');assert.equal(sent.at(-1).kind,'dismiss');
  receive({kind:'visibility',visible:false});assert.equal(sent.at(-1).paused,false);
  assert.equal(dom.document.documentElement.dataset.theme,'frost');
  assert.ok(notificationText({...card,count:9,failed:2}).detail.includes('2'));
});

test('notification target stays on retained history and keeps existing split selections', () => {
  const selection=runLogSelection();
  selection.reset({round_id:'first',latest_run_id:'old',runs:[{run_id:'old'}]});
  selection.toggle('old','node',true);
  selection.target('notification-run');
  const next={round_id:'second',latest_run_id:'latest',runs:[{run_id:'latest'}]};
  selection.reset(next);
  assert.equal(selection.inspected,'notification-run');
  assert.deepEqual(selection.choices(next,new Map()),[{runID:'old',nodeID:'node'},{runID:'notification-run',nodeID:''}]);
  assert.deepEqual(new Set(selection.required(next)),new Set(['old','notification-run']));
});
