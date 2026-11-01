import test from 'node:test';
import assert from 'node:assert/strict';
import {commandQuery, filterCommands, quotedCommand} from '../internal/quickinput/assets/command-model.mjs';
import {setLocale} from '../internal/quickinput/assets/i18n.mjs';

setLocale('zh-CN');
import {commandClient} from '../internal/quickinput/assets/command-client.mjs';
import {commandResults} from '../internal/quickinput/assets/command-results.mjs';

test('command errors survive refresh, replace only their own latest result and ignore late receipts', () => {
  const results = commandResults();
  results.finish('a', results.begin('a'), 'first error');
  results.retain(['a', 'b']);
  assert.equal(results.error('a'), 'first error');
  const old = results.begin('a'), latest = results.begin('a');
  results.finish('a', latest, 'new error');
  results.finish('a', old);
  assert.equal(results.error('a'), 'new error');
  results.finish('b', results.begin('b'));
  assert.equal(results.error('a'), 'new error');
  results.finish('a', results.begin('a'));
  assert.equal(results.error('a'), '');
  const delayed = results.begin('a');
  results.retain(['b']);
  assert.equal(results.finish('a', delayed, 'deleted command error'), false);
  assert.equal(results.error('a'), '');
});

test('command mode is limited to a leading slash on a single line; quoting keeps text intact', () => {
  assert.equal(commandQuery('/'), ''); assert.equal(commandQuery('/中文  名称 '), '中文  名称');
  for (const text of ['hello /backup', '/a\nb', ' /a', '']) assert.equal(commandQuery(text), null);
  assert.equal(quotedCommand('/a```b'), '````text\n/a```b\n````');
});

test('exact, prefix, substring and subsequence search includes aliases and broken entries in stable order', () => {
  const entries = [
    {id:'2',name:'backupAll',aliases:[]}, {id:'3',name:'myBackup',aliases:[]},
    {id:'1',name:'backup',aliases:['备份文件','归档'],state:'missing'}, {id:'4',name:'browser',aliases:[]},
  ];
  assert.deepEqual(filterCommands(entries,'BACKUP').map(e=>e.id), ['1','2','3']);
  assert.deepEqual(filterCommands(entries,'bkp').map(e=>e.id), ['1','2','3']);
  assert.equal(filterCommands(entries,'归档')[0].matchedName,'归档');
  assert.equal(filterCommands(entries,'备份')[0].state,'missing');
  assert.equal(filterCommands(entries,'zzzz').length,0);
});

test('each intentional launch has a new sequence; errors are returned without retry and disposal releases pending calls', async () => {
  const listeners=new Set(), sent=[];
  const client=commandClient({subscribe(fn){listeners.add(fn);return()=>listeners.delete(fn);}}, (kind,value)=>sent.push({kind,...value}));
  const reply=(message)=>{for(const listener of listeners) listener(message);};
  const first=client.call('launch',{command_id:'one',revision:'revision'});
  const second=client.call('launch',{command_id:'one',revision:'revision'});
  assert.deepEqual(sent.map(m=>m.command.sequence),[1,2]);
  assert.notEqual(sent[0].id,sent[1].id);
  reply({kind:'command_result',id:sent[1].id,response:{started:true}});
  reply({kind:'command_result',id:sent[0].id,response:{error:{code:'missing',message:'命令脚本缺失，请更新配置'}}});
  assert.equal((await second).started,true); await assert.rejects(first,{code:'missing'});
  assert.equal(sent.length,2);
  const pending=client.call('list'); client.dispose(); await assert.rejects(pending,/已关闭/);
  assert.equal(listeners.size,0);
});
