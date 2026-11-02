import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import vm from 'node:vm';
import {logDecoder} from '../internal/quickinput/assets/log-model.mjs';

class Element {
  constructor() {
    this.dataset = {}; this.value = 'utf-8'; this.children = [];
    this.scrollTop = 0; this.scrollHeight = 0; this.clientHeight = 0;
    this.style = {removeProperty() {}};
  }
  append(...children) {this.children.push(...children);}
  replaceChildren(...children) {this.children = children;}
  setAttribute() {} removeAttribute() {} addEventListener() {}
  scrollIntoView() {} focus() {}
  querySelector() {return {getBoundingClientRect: () => ({height:20})};}
}
function dom() {
  const elements = new Map();
  const get = id => {if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id);};
  return {get, document:{getElementById:get, createElement:()=>new Element(), createDocumentFragment:()=>new Element(), documentElement:new Element()}};
}
const source = async name => (await fs.readFile(new URL('../internal/quickinput/assets/' + name, import.meta.url), 'utf8')).replace(/^import .*;$/gm, '');
const labels = {applyI18n(){}, setLocale(){}, t:key=>key, getLocale:()=>'', localizeError:()=>'', localizeMessage:()=>'', formatDateTime:()=>'', runLabel:()=>'', reasonLabel:()=>'', modeLabel:()=>''};

test('log navigation keeps polling when the refresh timer fires while awaiting a page', async () => {
  const view = dom(), timers = new Map(); let sequence = 0, pending = null;
  const record = {run_id:'run1234567890abcd', phase:'running', elapsed_seconds:0, command:{name:'test'}};
  const page = {start:0, previous:0, next:20, end:true, run:record, chunks:[]};
  const context = vm.createContext({...view, ...labels, logDecoder,
    webview2Bridge:()=>({send(){}, subscribe(){}}),
    commandClient:()=>({call:async action => action === 'status' ? {run:record} : pending ? await pending : {log:page}}),
    runBusy:r=>r?.phase !== 'finished', runStoppable:()=>true,
    setTimeout:fn=>{timers.set(++sequence,fn);return sequence;}, clearTimeout:id=>timers.delete(id),
  });
  vm.runInContext(await source('command-log.mjs') + '\nglobalThis.test={refresh,navigate,setup(){epoch="1";commandID="command1234567890";}};', context);
  context.test.setup(); await context.test.refresh(); assert.equal(timers.size,1);
  let resolve;
  pending = new Promise(done=>{resolve=done;});
  const navigation = context.test.navigate(0);
  await [...timers.values()][0]();
  resolve({log:page}); await navigation;
  assert.equal(timers.size,1);
  pending = null;
  await [...timers.values()][0]();
  assert.equal(timers.size,1);
});

test('keyboard selection refreshes launch availability even when list rendering is cached', async () => {
  const view = dom(); view.get('input').value = '/';
  const context = vm.createContext({...view, ...labels,
    commandClient:()=>({call(){}, dispose(){}}), commandQuery:()=>'', filterCommands:entries=>entries,
    commandResults:()=>({error:()=>'', retain(){}, begin(){}, finish(){}}),
    runBusy:r=>Boolean(r?.phase && r.phase !== 'finished'), runStoppable:r=>r?.phase === 'running',
    setTimeout(){}, clearTimeout(){},
  });
  const code = (await source('command-ui.mjs')).replace('export function commandPanel','function commandPanel')
    .replace('return {changed: textChanged, key, submit, relocalize, dispose};',
      'return {key,paint,setup(values){entries=values;ready=true;visible=true;managedAvailable=true;paint();}};');
  vm.runInContext(code + '\nglobalThis.factory=commandPanel;',context);
  const panel = context.factory({subscribe(){return ()=>{};}}, {input:view.get('input'), post(){}, changed(){}, sendText(){}});
  panel.setup([{id:'one',name:'running',phase:'running',run_id:'123',execution_mode:'managed',state:'ready'},
    {id:'two',name:'idle',execution_mode:'unmanaged',state:'ready'}]);
  assert.equal(view.get('command-run').disabled,true);
  panel.key({key:'ArrowDown'}); panel.paint();
  assert.equal(view.get('command-run').disabled,false);
  panel.key({key:'ArrowUp'}); panel.paint();
  assert.equal(view.get('command-run').disabled,true);
});
