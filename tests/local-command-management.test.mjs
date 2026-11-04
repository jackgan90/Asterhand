import test from 'node:test';
import assert from 'node:assert/strict';
import {localCommands} from '../web/local-commands.mjs';
import {domFixture, deferred, tick, descendants} from './web-fixture.mjs';
import {setLocale, t as translate} from '../web/i18n.mjs';
import {newGraph} from '../web/graph-model.mjs';

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

async function historyForm(t) {
  const dom = domFixture(), panel = dom.select('#local-commands'), form = dom.select('#command-form'), scope = {active:true};
  for (const name of ['name','aliases','description','allow_concurrent','timeout','timeout_unit']) form.elements[name] = dom.document.createElement('input');
  form.elements.namedItem = name => form.elements[name];
  panel.querySelectorAll = () => [...dom.elements.values()];
  form.querySelectorAll = () => Object.values(form.elements).filter(value => typeof value !== 'function');
  globalThis.document = dom.document;
  const graph = newGraph(); Object.assign(graph.nodes[1], {name:'compile', script:'C:\\step.cmd'});
  let catalog = {revision:'r1', commands:[{id:'c1', name:'build', revision:'v1', graph}]}, pending = null, posted = null, confirms = 0;
  const controller = localCommands({
    api: async path => path === '/api/local-commands/state' ? {runs:{}, managed_available:true} : catalog,
    post: async (path, body) => { posted = body; pending = deferred(); return pending.promise; },
    session:() => scope, listen:(target, name, callback) => target.addEventListener(name, callback), guarded:callback => callback(),
    actions:{confirm:() => { confirms++; return false; }},
  });
  t.after(() => {controller.stop(); delete globalThis.document;});
  await controller.open('c1'); await tick();
  const all = () => descendants(dom.select('#command-graph-editor'));
  const key = (element, type, key) => { const event = new Event(type, {cancelable:true}); Object.assign(event, {key}); element.dispatchEvent(event); };
  const nudge = () => { const card = all().find(el => el.dataset.kind === 'script'); key(card, 'keydown', 'ArrowRight'); key(card, 'keyup', 'ArrowRight'); };
  return {...dom, form, panel, controller, all, nudge, confirms:() => confirms, posted:() => posted,
    undo:() => all().find(el => el.className === 'quiet graph-undo').onclick(),
    redo:() => all().find(el => el.className === 'quiet graph-redo').onclick(),
    save:async () => { form.fire('submit'); await tick(); },
    succeed:async (normalize = command => command) => {
      catalog = {revision:'r2', commands:[{...normalize(structuredClone(posted.command)), revision:'v2'}]};
      pending.resolve({id:'c1', catalog}); await tick();
    },
    fail:async error => { pending.reject(error); await tick(); },
  };
}

test('saving retains history, revisions and dirty state tracks undo and redo across the save point', async t => {
  const h = await historyForm(t);
  h.nudge(); assert.equal(h.form.dataset.dirty, 'true');
  await h.save(); await h.succeed();
  assert.equal(h.form.dataset.dirty, 'false');
  h.undo(); assert.equal(h.form.dataset.dirty, 'true');
  h.redo(); assert.equal(h.form.dataset.dirty, 'false');
  h.form.elements.description.value = 'outside the graph'; h.form.fire('input');
  h.undo(); h.redo(); assert.equal(h.form.dataset.dirty, 'true', 'graph history cannot erase a top-level modification');
  h.select('#commands-close').fire('click'); assert.equal(h.confirms(), 1); assert.equal(h.panel.open, true);
  h.form.elements.description.value = ''; h.form.fire('input');
  assert.equal(h.form.dataset.dirty, 'false');
  h.nudge(); await h.save();
  assert.equal(h.posted().revision, 'r2'); assert.equal(h.posted().command.revision, 'v2');
  await h.succeed();
});

test('undo back to loaded content does not ask to discard; refresh starts a new history', async t => {
  const h = await historyForm(t);
  h.nudge(); h.undo();
  h.select('#commands-refresh').fire('click'); await tick();
  assert.equal(h.confirms(), 0);
  assert.equal(h.all().find(el => el.className === 'quiet graph-redo').disabled, true);
  h.select('#commands-close').fire('click');
  assert.equal(h.confirms(), 0); assert.equal(h.panel.open, false);
});

for (const status of [409, 500]) test(`failed save (${status}) preserves draft, history and original revision`, async t => {
  const h = await historyForm(t);
  h.nudge(); await h.save(); await h.fail({status, messageKey:'err.localcmd.revision_conflict'});
  assert.equal(h.form.dataset.dirty, 'true');
  h.undo(); assert.equal(h.form.dataset.dirty, 'false');
  h.redo(); assert.equal(h.form.dataset.dirty, 'true');
  await h.save(); assert.equal(h.posted().revision, 'r1'); assert.equal(h.posted().command.graph.nodes[1].x, 300);
  await h.succeed();
});

test('negative-coordinate export and successful save keep draft, view and undo continuous', async t => {
  const h = await historyForm(t), card = h.all().find(el => el.dataset.kind === 'script'), stage = h.all().find(el => el.className === 'graph-stage');
  const event = new Event('pointerdown'); Object.assign(event, {button:0, clientX:0, clientY:0, pointerId:1}); card.dispatchEvent(event);
  card.onpointermove({clientX:-400, clientY:-250}); card.onpointerup();
  const view = stage.style.transform;
  await h.save();
  assert.equal(h.posted().command.graph.nodes[1].x, 0);
  assert.equal(card.style.left, '-110px'); assert.equal(stage.style.transform, view);
  await h.succeed();
  assert.equal(card.style.left, '-110px'); assert.equal(stage.style.transform, view);
  h.undo(); assert.equal(card.style.left, '290px'); assert.equal(h.form.dataset.dirty, 'true');
  h.redo(); assert.equal(card.style.left, '-110px'); assert.equal(h.form.dataset.dirty, 'false');
});

test('server-normalized names, paths and aliases compare as saved independently without rewriting history', async t => {
  const h = await historyForm(t);
  const name = h.all().find(el => el.tagName === 'INPUT' && el.value === 'compile');
  const script = h.all().find(el => el.className === 'command-script-path').children[0];
  name.value = ' compile '; name.fire('input'); name.blur();
  script.value = 'C:/step.cmd'; script.fire('input'); script.blur();
  h.form.elements.aliases.value = 'build\nfast\nfast'; h.form.fire('input');
  await h.save();
  await h.succeed(command => {
    command.graph.nodes[1].name = 'compile'; command.graph.nodes[1].script = 'C:\\step.cmd';
    command.aliases = ['fast']; return command;
  });
  assert.equal(h.form.dataset.dirty, 'false');
  assert.equal(name.value, ' compile '); assert.equal(script.value, 'C:/step.cmd');
  h.undo(); assert.equal(script.value, 'C:\\step.cmd');
  assert.equal(h.form.dataset.dirty, 'false', 'a mix of submitted and canonical fields is still saved');
  h.undo(); assert.equal(name.value, 'compile'); assert.equal(h.form.dataset.dirty, 'false');
  h.form.elements.aliases.value = 'fast'; h.form.fire('input'); assert.equal(h.form.dataset.dirty, 'false');
  h.redo(); assert.equal(name.value, ' compile '); assert.equal(h.form.dataset.dirty, 'false');
  h.redo(); assert.equal(script.value, 'C:/step.cmd'); assert.equal(h.form.dataset.dirty, 'false');
  h.form.elements.aliases.value = 'other'; h.form.fire('input'); assert.equal(h.form.dataset.dirty, 'true');
  h.nudge(); h.form.elements.aliases.value = 'fast'; h.form.fire('input');
  assert.equal(h.form.dataset.dirty, 'true', 'actual graph edits still differ from both saved forms');
});
