import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp, mkdir, readdir, writeFile, copyFile, rm} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {pathToFileURL} from 'node:url';
import {createHash} from 'node:crypto';
import {PromptMonitor} from '../internal/executor/chatbridge/prompt-state.mjs';
import {promptWarning} from '../internal/quickinput/assets/chat-model.mjs';

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), 'asterhand-prompt-'));
  t.after(() => rm(root, {recursive:true, force:true}));
  const plugin = join(root, 'prompt-plugin');
  await mkdir(plugin);
  await copyFile(new URL('../internal/executor/promptplugin/index.mjs', import.meta.url), join(plugin,'index.mjs'));
  const text='Fixture product policy; private prompt is not diagnostic output.';
  const digest=createHash('sha256').update(text).digest('hex');
  const plan={version:1,instance:'runtime-instance-0001',text,digest,
    application:{revision:'default-chat-v2-'+digest.slice(0,16),mode:'append',state:'pending'}};
  const save=()=>writeFile(join(root,'prompt-policy.json'),JSON.stringify(plan));
  await save();
  const hooks=new Map();
  const loaded=await import(pathToFileURL(join(plugin,'index.mjs')).href);
  const methods=new Map();
  loaded.default.register({pluginConfig:{policyPath:join(root,'prompt-policy.json')},on(name,fn){hooks.set(name,fn);},
    registerGatewayMethod(name,handler,options){assert.equal(options.scope,'operator.read');methods.set(name,handler);}});
  const readStatus=identity=>new Promise((resolve,reject)=>methods.get('asterhand.chat.promptStatus')({params:identity,
    respond(ok,result){if(ok)resolve(result.record);else reject(Error('lookup failed'));}}));
  const context={runId:'run-identity-0001',sessionKey:'agent:main:asterhand:conversation-0001',hookInvocation:{assertActive(){}}};
  const input={runId:context.runId,sessionId:'session-identity-0001',systemPrompt:'Native identity and SOUL context\n'+text,tools:[]};
  return {root,plan,save,hooks,context,input,readStatus,monitor:()=>new PromptMonitor(plan.application,plan.instance,readStatus)};
}

test('native-preserving supplement is confirmed from effective model input, with metadata-only diagnostics',async t=>{
  const f=await fixture(t);
  const result=f.hooks.get('before_prompt_build')({},f.context);
  assert.equal(result.appendSystemContext,f.plan.text);assert.deepEqual(result.toolsAllow,[]);
  assert.equal(Object.hasOwn(result,'systemPrompt'),false);
  const monitor=f.monitor();monitor.begin(f.input.runId,f.input.sessionId);
  assert.equal((await monitor.status(f.input.runId,f.input.sessionId)).state,'pending');
  f.hooks.get('llm_input')(f.input,f.context);
  const record=await f.readStatus({instance:f.plan.instance,run:f.input.runId,session:f.input.sessionId});
  assert.equal(record.run,f.input.runId);
  const status=await monitor.status(f.input.runId,f.input.sessionId);
  assert.equal(status.state,'applied');assert.equal(promptWarning(status),'');
  const raw=JSON.stringify(record);
  assert.ok(!raw.includes(f.plan.text));assert.ok(!raw.includes('systemPrompt'));
  assert.ok(!(await readdir(f.root)).some(name=>name.startsWith('prompt-status')));
  assert.equal(await monitor.status(f.input.runId,'other-session'),null);
  await writeFile(join(f.root,'prompt-policy.json'),'{}');
  f.hooks.get('llm_input')({...f.input,systemPrompt:'native fallback on retry'},f.context);
  assert.equal((await monitor.status(f.input.runId,f.input.sessionId)).state,'degraded');
  const restarted=f.monitor();
  assert.equal(await restarted.status(f.input.runId,f.input.sessionId),null);
});

test('append remains explicit and unsupported, invalid, or rejected hooks never block chat',async t=>{
  const f=await fixture(t);
  f.plan.application.mode='append';await f.save();
  const result=f.hooks.get('before_prompt_build')({},f.context);
  assert.equal(result.appendSystemContext,f.plan.text);
  assert.equal(Object.hasOwn(result,'systemPrompt'),false,'append discarded native persona');
  f.hooks.get('llm_input')({...f.input,systemPrompt:'Native policy\n'+f.plan.text},f.context);
  const monitor=f.monitor();monitor.begin(f.input.runId,f.input.sessionId);
  const status=await monitor.status(f.input.runId,f.input.sessionId);
  assert.equal(status.state,'applied');assert.equal(promptWarning(status),'');
  f.plan.application.mode='replace';await f.save();
  assert.equal(f.hooks.get('before_prompt_build')({},f.context).systemPrompt,f.plan.text);
  f.plan.application.revision=f.plan.application.revision.replace('-v2-','-v1-');await f.save();
  assert.equal(f.hooks.get('before_prompt_build')({},f.context).systemPrompt,f.plan.text);
  f.plan.application.mode='unsupported';await f.save();
  assert.equal(f.hooks.get('before_prompt_build')({},f.context),undefined);
  f.plan.application.mode='replace';f.plan.digest='invalid';await f.save();
  assert.equal(f.hooks.get('before_prompt_build')({},f.context),undefined);
  await rm(join(f.root,'prompt-policy.json'));
  assert.doesNotThrow(()=>f.hooks.get('before_prompt_build')({},f.context));
  assert.doesNotThrow(()=>f.hooks.get('llm_input')(f.input,f.context));
  assert.match(promptWarning({mode:'unsupported',state:'degraded'}),/聊天仍可使用/);
});

test('stale and mismatched policy observations cannot claim success',async t=>{
  const f=await fixture(t);
  assert.equal(f.hooks.get('before_prompt_build')({}, {...f.context,sessionKey:'agent:main:other'}),undefined);
  assert.equal(f.hooks.get('before_prompt_build')({}, {...f.context,hookInvocation:{assertActive(){throw Error('expired');}}}),undefined);
  f.hooks.get('llm_input')({...f.input,systemPrompt:'Native policy was not replaced'},f.context);
  const monitor=f.monitor();monitor.begin(f.input.runId,f.input.sessionId);
  assert.equal((await monitor.status(f.input.runId,f.input.sessionId)).state,'degraded');
  f.hooks.get('llm_input')(f.input,f.context);
  const next=new PromptMonitor(f.plan.application,'new-runtime-instance',f.readStatus);
  next.begin(f.input.runId,f.input.sessionId);
  assert.equal((await next.status(f.input.runId,f.input.sessionId)).state,'pending');
  assert.equal((await next.status(f.input.runId,f.input.sessionId,true)).state,'degraded');
  for(let i=0;i<20;i++)next.begin('run-'+i,'session');
  assert.equal(next.runs.size,16);
});


test('later fallback and failed optional observation never reuse earlier applied status',async t=>{
  const f=await fixture(t), monitor=f.monitor();
  monitor.begin(f.input.runId,f.input.sessionId);
  f.hooks.get('llm_input')(f.input,f.context);
  assert.equal((await monitor.status(f.input.runId,f.input.sessionId)).state,'applied');
  // The former disk-status path is unusable; live metadata must remain accurate.
  await mkdir(join(f.root,'prompt-status.json.tmp'));
  await writeFile(join(f.root,'prompt-policy.json'),'{}');
  assert.equal(f.hooks.get('before_prompt_build')({},f.context),undefined);
  f.hooks.get('llm_input')({...f.input,systemPrompt:'native fallback'},f.context);
  assert.equal((await monitor.status(f.input.runId,f.input.sessionId,true)).state,'degraded');
  f.hooks.get('llm_input')(f.input,f.context);
  assert.equal((await monitor.status(f.input.runId,f.input.sessionId)).state,'applied');
  monitor.readStatus=async()=>{throw Error('observation transport unavailable');};
  assert.equal((await monitor.status(f.input.runId,f.input.sessionId,true)).state,'degraded');
  monitor.readStatus=async()=>null;
  assert.equal((await monitor.status(f.input.runId,f.input.sessionId,true)).state,'degraded');
  monitor.readStatus=f.readStatus;
  f.hooks.get('llm_input')({...f.input,systemPrompt:42},f.context);
  assert.equal((await monitor.status(f.input.runId,f.input.sessionId,true)).state,'degraded');
  for(let i=0;i<20;i++)f.hooks.get('llm_input')({...f.input,runId:'run-identity-'+String(i).padStart(4,'0')},f.context);
  assert.equal(await f.readStatus({instance:f.plan.instance,run:f.input.runId,session:f.input.sessionId}),null);
});
