import test from 'node:test';
import assert from 'node:assert/strict';
import {localCommands} from '../web/local-commands.mjs';
import {domFixture, deferred, tick, descendants} from './web-fixture.mjs';
import {setLocale, t as translate} from '../web/i18n.mjs';

async function harness(t) {
  const dom = domFixture(), panel = dom.select('#local-commands'), form = dom.select('#command-form');
  const requests = [], selections = [], scope = {active:true};
  for (const name of ['name','aliases','description','allow_concurrent','timeout','timeout_unit']) form.elements[name] = dom.document.createElement('input');
  form.elements.namedItem = name => form.elements[name];
  panel.querySelectorAll = () => [...dom.elements.values()];
  form.querySelectorAll = () => Object.values(form.elements).filter(value => typeof value !== "function");
  dom.select('#command-arguments').querySelectorAll = () => [];
  globalThis.document = dom.document;
  const controller = localCommands({
    api: async (path, options) => {
      if (path === '/api/local-commands/state') return {runs:{},managed_available:true};
      if (path === '/api/local-commands') return {revision:'catalog',commands:[]};
      requests.push({path,...options}); const pending = deferred(); selections.push(pending); return pending.promise;
    },
    post: async () => {throw Error('browse must not save');}, session:() => scope,
    listen:(target,name,callback) => target.addEventListener(name,callback), guarded:callback=>callback(), actions:{confirm:()=>true},
  });
  t.after(() => {controller.stop(); delete globalThis.document;});
  await controller.open();
  const graphFields=descendants(dom.select('#command-graph-editor'));
  const browse=graphFields.find(el=>el.tagName==='BUTTON'&&el.textContent===translate('web.commands.browse'));
  const script=graphFields.find(el=>el.className==='command-script-path').children[0];
  return {...dom, controller, requests, selections, scope, script, browse};
}

test('Browse fills an absolute path without saving; cancellation preserves the typed path and releases controls', async t => {
  const h = await harness(t), button = h.browse;
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
  h.browse.fire('click');
  h.controller.stop();
  assert.equal(h.requests[0].signal.aborted,true);
  h.selections[0].resolve({path:'C:\\late.bat'}); await tick();
  assert.equal(h.script.value,'C:\\original.bat');
});

test('graph validation reason and node context both relocalize in the command form', async t => {
  const h = await harness(t);
  setLocale('zh-CN');
  h.browse.fire('click');
  h.selections[0].reject({code:'invalid_graph',messageKey:'err.localcmd.unsupported_ext',messageArgs:{graph_node:'build'}});
  await tick();
  for(const locale of ['zh-CN','en-US']){
    setLocale(locale);h.controller.relocalize();
    assert.equal(h.select('#commands-status').textContent,translate('err.graph.script',{node:'build',err:translate('err.localcmd.unsupported_ext')}));
  }
});

test('saving the open command keeps the graph view through the success reload', async t => {
  globalThis.requestAnimationFrame = callback => callback();
  t.after(() => delete globalThis.requestAnimationFrame);
  const dom = domFixture(), panel = dom.select('#local-commands'), form = dom.select('#command-form'), scope = {active:true};
  for (const name of ['name','aliases','description','allow_concurrent','timeout','timeout_unit']) form.elements[name] = dom.document.createElement('input');
  form.elements.namedItem = name => form.elements[name];
  panel.querySelectorAll = () => [...dom.elements.values()];
  form.querySelectorAll = () => Object.values(form.elements).filter(value => typeof value !== 'function');
  globalThis.document = dom.document;
  let catalog = {revision:'r1', commands:[]}, posted = null;
  const controller = localCommands({
    api: async path => path === '/api/local-commands/state' ? {runs:{}, managed_available:true} : catalog,
    post: async (path, body) => {
      posted = body;
      const command = {...body.command, id:'command-1', revision:'c1'};
      catalog = {revision:'r2', commands:[command]};
      return {id:'command-1', catalog};
    },
    session:() => scope, listen:(target, name, callback) => target.addEventListener(name, callback), guarded:callback => callback(), actions:{confirm:() => true},
  });
  t.after(() => {controller.stop(); delete globalThis.document;});
  await controller.open();
  const editor = descendants(dom.select('#command-graph-editor'));
  editor.find(el => el.className === 'command-script-path').children[0].value = 'C:\\step.cmd';
  editor.find(el => el.className === 'command-script-path').children[0].oninput();
  form.elements.name.value = 'build';
  const stage = editor.find(el => el.className === 'graph-stage');
  editor.find(el => el.className?.includes('graph-zoom-in')).onclick();
  const view = stage.style.transform;
  form.fire('submit'); await tick(); await tick();
  assert.ok(posted, 'the command was saved');
  assert.equal(stage.style.transform, view, 'the success reload does not refit the view');
});
