import test from 'node:test';
import assert from 'node:assert/strict';
import {commandSelection} from '../web/command-selection.mjs';
import {domFixture, deferred, tick} from './web-fixture.mjs';
import {setLocale, t as translate} from '../web/i18n.mjs';

function harness(t, size = 3, managing = true) {
  const dom = domFixture(); globalThis.document = dom.document;
  let commands = Array.from({length:size}, (_, index) => ({id:`id-${index}`, name:`command-${index}`, revision:`r-${index}`}));
  let locked = false, confirms = true, before = true, active = true, handler;
  const requests = [], removed = [], prompts = [];
  const selection = commandSelection({
    post:async(path, body)=>{requests.push({path, body}); return handler(body);},
    confirm:message=>{prompts.push(message); return confirms;}, locked:()=>locked, lock:value=>{locked=value;},
    beforeDelete:()=>before, active:()=>active, settled:async()=>{},
    deleted:ids=>{removed.push(...ids); commands=commands.filter(command=>!ids.includes(command.id)); selection.sync(commands);},
  });
  function render() {
    selection.sync(commands);
    dom.select('#registered-commands').replaceChildren(...commands.map(command=>selection.checkbox(command)));
  }
  function all(checked = true) {dom.select('#commands-select-all').checked=checked; selection.selectAll();}
  function toggle(index, checked = true) {const input=dom.select('#registered-commands').children[index]; input.checked=checked; input.fire('change');}
  handler=body=>({results:body.items.map(item=>({operation_id:item.operation_id,receipt:{state:'completed'}}))});
  if (managing) selection.toggle();
  render();
  t.after(()=>{selection.stop(); delete globalThis.document; setLocale('en-US');});
  return {...dom, selection, requests, removed, prompts, render, all, toggle,
    setHandler:value=>{handler=value;}, confirm:value=>{confirms=value;}, before:value=>{before=value;}, active:value=>{active=value;}};
}

test('individual selection, select all and clear are independent of visible filtering', async t => {
  const h=harness(t, 3, false);
  assert.equal(h.select('#commands-selection').hidden, true);
  assert.equal(h.select('#registered-commands').children[0].hidden, true);
  h.all(); await h.selection.submit(); assert.equal(h.requests.length, 0);
  h.selection.toggle();
  h.toggle(0); h.selection.leave(); h.selection.toggle();
  assert.equal(h.select('#registered-commands').children[0].checked, false);
  assert.equal(h.select('#commands-delete-selected').disabled, true);
  h.toggle(0); h.toggle(2);
  assert.equal(h.select('#commands-select-all').indeterminate, true);
  h.select('#registered-commands').replaceChildren(); // Search hides all rows.
  h.all();
  assert.equal(h.select('#commands-select-all').checked, true);
  h.all(false);
  assert.equal(h.select('#commands-delete-selected').disabled, true);
  h.all(); await h.selection.submit();
  assert.deepEqual(h.removed, ['id-0','id-1','id-2']);
  assert.equal(h.requests[0].body.items.length, 3);
  assert.equal(h.select('#commands-delete-results').children.length, 0);
  assert.equal(h.select('#commands-delete-results').hidden, true);
});

test('confirmation cancellation and draft refusal do not submit', async t => {
  const h=harness(t); h.all(); h.confirm(false); await h.selection.submit();
  h.confirm(true); h.before(false); await h.selection.submit();
  assert.equal(h.requests.length, 0);
  assert.equal(h.select('#commands-select-all').checked, true);
});

test('selection is unlimited by the per-request bound and only rejected items stay selected', async t => {
  const h=harness(t, 70); h.all();
  h.setHandler(({items})=>({results:items.map(item=>item.command_id==='id-20' ?
    {receipt:{state:'rejected'},error:{code:'conflict',message_key:'err.localcmd.revision_conflict'}} : {receipt:{state:'completed'}})}));
  await h.selection.submit();
  assert.deepEqual(h.requests.map(request=>request.body.items.length), [32,32,6]);
  assert.equal(h.removed.length, 69);
  assert.equal(h.select('#commands-selection-count').textContent, translate('web.commands.selected_count',{count:1}));
  assert.equal(h.select('#commands-delete-results').children.length, 1);
  assert.match(h.select('#commands-delete-results').children[0].textContent, /^command-20:/);
  const result=h.select('#commands-delete-results').children[0];
  h.selection.paint();
  assert.equal(h.select('#commands-delete-results').children[0],result,'background updates keep result DOM and scroll stable');
});

test('lost response stops later chunks; explicit retry retains original identities and revisions', async t => {
  const h=harness(t, 35); h.all(); const pending=deferred();
  h.setHandler(()=>pending.promise);
  const first=h.selection.submit(); await tick();
  h.selection.toggle();
  assert.equal(h.select('#commands-selection').hidden, false, 'cannot leave management while deleting');
  await h.selection.submit(); assert.equal(h.requests.length, 1);
  pending.reject(Error('lost response')); await first;
  assert.equal(h.requests.length, 1); assert.equal(h.removed.length, 0);
  assert.equal(h.select('#commands-delete-results').children.length, 35);
  assert.ok(h.select('#commands-delete-results').children[34].textContent.includes(translate('web.commands.delete_not_sent')));
  h.selection.leave(); h.selection.toggle(); h.all();
  h.setHandler(({items})=>({results:items.map(()=>({receipt:{state:'completed'}}))}));
  await h.selection.submit();
  assert.deepEqual(h.requests[0].body.items, h.requests[1].body.items);
  assert.equal(h.removed.length, 35);
  assert.equal(h.select('#commands-delete-results').children.length, 0);
  assert.equal(h.select('#commands-delete-results').hidden, true);
});

test('locale and state refresh preserve checked IDs; teardown ignores late results', async t => {
  const h=harness(t); h.toggle(1); h.render();
  for(const locale of ['zh-CN','en-US']) {
    setLocale(locale); h.selection.paint();
    assert.equal(h.select('#commands-selection-count').textContent, translate('web.commands.selected_count',{count:1}));
    assert.equal(h.select('#registered-commands').children[1].checked, true);
  }
  const pending=deferred(); h.setHandler(()=>pending.promise);
  const sending=h.selection.submit(); await tick(); h.selection.stop();
  pending.resolve({results:[{receipt:{state:'completed'}}]}); await sending;
  assert.equal(h.removed.length, 0);
});
