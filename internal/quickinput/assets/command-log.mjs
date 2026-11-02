import {webview2Bridge} from './webview2-bridge.mjs';
import {commandClient} from './command-client.mjs';
import {applyI18n, setLocale, t, localizeError, formatDateTime} from './i18n.mjs';
import {runBusy, runStoppable, runLabel, reasonLabel} from './command-run.mjs';
import {logDecoder} from './log-model.mjs';

const bridge = webview2Bridge(), byID = id => document.getElementById(id);
const output = byID('log-output'), stop = byID('log-stop'), encoding = byID('log-encoding');
let epoch = '', commandID = '', record = null, page = null, follow = true, loading = false, stopping = false;
let timer = null, visible = true, decoder = logDecoder(), characters = 0, error = null, finished = false;
const post = (kind, fields = {}) => bridge.send({version:1, kind, generation:epoch, ...fields});
const client = commandClient(bridge, post);

function renderMetadata() {
  byID('log-name').textContent = record?.command?.name || t('brand.log_window_title');
  byID('log-state').textContent = runLabel(record);
  stop.disabled = stopping || !runStoppable(record);
  const details = [];
  if (record?.started_at) details.push(t('common.command.started', {time:formatDateTime(record.started_at)}));
  if (record?.ended_at) details.push(t('common.command.ended', {time:formatDateTime(record.ended_at)}));
  if (record) details.push(t('common.command.elapsed', {seconds:record.elapsed_seconds.toFixed(1)}));
  if (record?.command?.timeout_seconds) details.push(t('common.command.timeout_value', {seconds:record.command.timeout_seconds}));
  if (record?.exit?.code !== undefined) details.push(t('common.command.exit_code', {code:record.exit.code}));
  if (reasonLabel(record)) details.push(reasonLabel(record));
  byID('log-details').replaceChildren(...details.map(text => {const el=document.createElement('span');el.textContent=text;return el;}));
  const warnings = [error && localizeError(error), record?.log_complete === false && t('common.command.log_incomplete'),
    record?.storage_error && t('common.command.storage_error'), record?.cleanup_pending && t('common.command.cleanup_pending')].filter(Boolean);
  byID('log-warning').textContent = warnings.join(' · '); byID('log-warning').hidden = !warnings.length;
  output.setAttribute('aria-label',characters ? t('common.command.logs') : t('common.command.empty_log'));
  byID('log-follow').textContent = t(follow ? 'common.command.following' : 'common.command.paused');
  byID('log-range').textContent = page ? t('common.command.range', {start:page.start,end:page.next}) : '';
  byID('log-first').disabled = loading || !page || page.start === 0;
  byID('log-previous').disabled = loading || !page || page.start === 0;
  byID('log-next').disabled = loading || !page || page.end;
  byID('log-latest').disabled = loading || !record;
}
function append(chunks) {
  const fragment = document.createDocumentFragment();
  for (const chunk of chunks) {
    if (!chunk.text) continue;
    const span = document.createElement('span');span.dataset.stream=chunk.stream;span.textContent=chunk.text;
    fragment.append(span);characters+=chunk.text.length;
  }
  output.append(fragment);
}
function resetView() { output.replaceChildren();characters=0;decoder=logDecoder(encoding.value);finished=false; }
async function loadPage(cursor, tail = false, extend = false) {
  if (!record) return;
  const result = await client.call(tail ? 'tail_log' : 'read_log', {command_id:commandID,run_id:record.run_id,...(tail ? {} : {cursor})});
  const next = result.log;
  if (!extend) {resetView();decoder.seed(next.context?.[encoding.value] ?? []);}
  page = extend && page ? {...next,start:page.start,previous:page.previous} : next;
  record=next.run;
  append(next.chunks.map(chunk=>decoder.decode(chunk)));
  if (!runBusy(record) && next.end && !finished) {append(decoder.finish());finished=true;}
  if (follow) output.scrollTop=output.scrollHeight;
}
function scheduleRefresh() {
  clearTimeout(timer);
  timer = epoch && visible ? setTimeout(refresh, 500) : null;
}
async function refresh() {
  clearTimeout(timer);
  if (!epoch || !visible) return;
  if (loading) {scheduleRefresh();return;}
  loading=true;
  try {
    const response = await client.call('status', {command_id:commandID});
    const current = response.run;
    if (!current) {record=null;page=null;resetView();error={messageKey:'err.localcmd.no_run'};return;}
    const changed = current.run_id !== record?.run_id;
    record=current;error=null;
    if (changed) {follow=true;page=null;resetView();}
    if (!page || follow && (characters>128*1024 || output.childElementCount>1024)) await loadPage(0,true);
    else if (follow) await loadPage(page.next,false,true);
  } catch (err) { error=err; if (err.code==='conflict') {record=null;page=null;resetView();} }
  finally {loading=false;renderMetadata();scheduleRefresh();}
}
async function navigate(cursor, tail = false) {
  if (loading || !record) return;
  follow=tail;loading=true;renderMetadata();
  try {await loadPage(cursor,tail);error=null;if(!tail)output.scrollTop=0;}
  catch(err){error=err;}
  finally{loading=false;renderMetadata();scheduleRefresh();}
}
byID('log-first').onclick=()=>navigate(0);
byID('log-previous').onclick=()=>navigate(page?.previous ?? 0);
byID('log-next').onclick=()=>navigate(page?.next ?? 0);
byID('log-latest').onclick=()=>navigate(0,true);
output.addEventListener('wheel', event=>{if(event.deltaY<0){follow=false;renderMetadata();}},{passive:true});
output.addEventListener('scroll',()=>{if(output.scrollHeight-output.scrollTop-output.clientHeight>40){follow=false;renderMetadata();}},{passive:true});
encoding.onchange=()=>navigate(page?.start ?? 0,follow);
stop.onclick=async()=>{
  if(stopping||!runStoppable(record))return;
  stopping=true;renderMetadata();
  try{const response=await client.call('stop',{command_id:commandID,run_id:record.run_id});record=response.run;error=null;}
  catch(err){error=err;}
  finally{stopping=false;renderMetadata();void refresh();}
};
bridge.subscribe(message=>{
  if(message.kind==='init'){epoch=message.generation;commandID=message.command_id;setLocale(message.locale);document.documentElement.dataset.theme=message.theme;applyI18n();post('initialized');void refresh();}
  if(message.kind==='theme')document.documentElement.dataset.theme=message.theme;
  if(message.kind==='locale'){setLocale(message.locale);applyI18n();renderMetadata();}
  if(message.kind==='visibility'){visible=message.visible;if(visible)void refresh();else clearTimeout(timer);}
});
post('ready');
