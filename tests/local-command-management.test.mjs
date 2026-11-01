import test from 'node:test';
import assert from 'node:assert/strict';
import {localCommands} from '../web/local-commands.mjs';
import {domFixture, deferred, tick} from './web-fixture.mjs';

async function harness(t) {
  const dom = domFixture(), panel = dom.select('#local-commands'), form = dom.select('#command-form');
  const requests = [], selections = [], scope = {active:true};
  for (const name of ['name','aliases','description','script','directory','window']) form.elements[name] = dom.document.createElement('input');
  form.elements.namedItem = name => form.elements[name];
  panel.querySelectorAll = () => [...dom.elements.values()];
  dom.select('#command-arguments').querySelectorAll = () => [];
  globalThis.document = dom.document;
  const controller = localCommands({
    api: async (path, options) => {
      if (path === '/api/local-commands') return {revision:'catalog',commands:[]};
      requests.push({path,...options}); const pending = deferred(); selections.push(pending); return pending.promise;
    },
    post: async () => {throw Error('browse must not save');}, session:() => scope,
    listen:(target,name,callback) => target.addEventListener(name,callback), guarded:callback=>callback(), actions:{confirm:()=>true},
  });
  t.after(() => {controller.stop(); delete globalThis.document;});
  await controller.open();
  return {...dom, controller, requests, selections, scope, script:form.elements.script};
}

test('Browse fills an absolute path without saving; cancellation preserves the typed path and releases controls', async t => {
  const h = await harness(t), button = h.select('#command-browse');
  h.script.value = 'C:\\original.ps1';
  button.fire('click'); button.fire('click');
  assert.equal(h.requests.length,1);
  assert.equal(button.disabled,true);
  assert.equal(h.requests[0].path,'/api/local-commands/browse');
  assert.equal(h.requests[0].method,'POST');
  h.selections[0].resolve({path:'F:\\中文 folder\\script.cmd'}); await tick();
  assert.equal(h.script.value,'F:\\中文 folder\\script.cmd');
  assert.equal(button.disabled,false);
  button.fire('click');
  h.selections[1].resolve({path:'',cancelled:true}); await tick();
  assert.equal(h.script.value,'F:\\中文 folder\\script.cmd');
  assert.equal(button.disabled,false);
});

test('ending the session cancels Browse and a late selection cannot change the form', async t => {
  const h = await harness(t);
  h.script.value = 'C:\\original.bat';
  h.select('#command-browse').fire('click');
  h.controller.stop();
  assert.equal(h.requests[0].signal.aborted,true);
  h.selections[0].resolve({path:'C:\\late.bat'}); await tick();
  assert.equal(h.script.value,'C:\\original.bat');
});
