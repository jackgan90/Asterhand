// Real WebView2 DOM/CSS and production chat controller, controlled transport.
(async () => {
  const {chat: original, editor: nativeEditor} = await import('./input.js');
  original.dispose();
  const {chatPanel} = await import('./chat-ui.mjs');
  const listeners = new Set(), receipts = new Map(), requests = [];
  const bridge = {subscribe(fn) { listeners.add(fn); return () => listeners.delete(fn); }};
  const emit = value => { for (const fn of listeners) fn(value); };
  const target = {node_id:'send-intent-node',role_id:'default'}, id = 'send-intent-chat';
  const queue = {entries:[],failures:[],paused:false,limit:8};
  let revision = 0, draft = '', current = null;
  const editor = {
    snapshot: () => ({text:draft,revision:++revision}),
    stageSubmission() { draft=''; return true; }, restoreSubmission() {}, clearIfRevision() {},
    setExpanded: value => nativeEditor.setExpanded(value), onSubmit(fn) { this.submit=fn; },
    post(kind,value) {
      if (kind !== 'chat') return;
      const request=value.chat; requests.push(request);
      const response={target,can_send:true,busy:Boolean(queue.active)};
      if (['connect','connection'].includes(request.action)) response.connection='connected';
      if (request.action==='list') response.conversations=[{id,state:'ready'}];
      if (request.action==='enqueue') {
        const turn={id:request.operation_id,operation_id:request.operation_id,conversation_id:id,
          delivery:'queued',run:'queued',started_at:new Date().toISOString(),updated_at:new Date().toISOString()};
        const item={kind:'send',state:'queued',operation_id:request.operation_id,conversation_id:id,text:request.text,turn};
        queue.entries.push(item); receipts.set(request.operation_id,item); response.turn=turn;
      }
      if (request.action==='cancel') {
        if (queue.active?.operation_id!==request.operation_id) throw Error('wrong reply stopped');
        current={...queue.active,run:'cancelled',updated_at:new Date().toISOString()};
        receipts.get(request.operation_id).turn=current; queue.active=null; response.turn=current;
      }
      if (request.action==='queue_item') response.queue_item=receipts.get(request.operation_id);
      if (['history','select','reconcile'].includes(request.action)) {
        response.conversation={id,state:'ready'}; response.turn=current;
        response.page={messages:[]};
      }
      response.queue={...queue,revision:++revision,
        entries:queue.entries.map(({text,...item})=>item),failures:queue.failures.map(({text,...item})=>item)};
      queueMicrotask(()=>emit({kind:'chat_result',id:value.id,reply:{response}}));
    },
  };
  const panel=chatPanel(bridge,editor);
  const frame=()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
  const sync=async()=>{document.querySelector('#chat-refresh').click();await frame();};
  const stop=document.querySelector('#chat-stop'), rows=document.querySelector('#chat-messages');
  const checkControls=()=>{
    if(document.querySelector('#chat-queue-resume') || [...document.querySelectorAll('button')].some(b=>/取回编辑|取消发送|移出队列|继续发送/.test(b.textContent))) throw Error('queue management control remains');
  };
  emit({kind:'focus'});await frame();
  draft='冷启动期间提交的消息';await editor.submit();await frame();
  if(!stop.hidden || !rows.textContent.includes('冷启动期间提交的消息')) throw Error('preparing send has stop control or missing bubble');
  draft='随后提交的消息';await editor.submit();await frame();checkControls();
  const first=queue.entries.shift();first.state='settled';
  current={...first.turn,delivery:'accepted',run:'running',updated_at:new Date().toISOString()};first.turn=current;queue.active=current;
  await sync();
  if(stop.hidden || stop.textContent!=='停止回复') throw Error('accepted reply has no stop reply control');
  stop.click();await frame();
  if(queue.active || queue.paused || queue.entries.length!==1 || !rows.textContent.includes('冷启动期间提交的消息')) throw Error('stop changed message custody or paused next send');
  const second=queue.entries.shift();second.state='failed';
  second.turn={...second.turn,delivery:'rejected',run:'failed',updated_at:new Date().toISOString()};queue.failures=[second];
  await sync();checkControls();
  if(!rows.textContent.includes('消息未送达') || !stop.hidden) throw Error('failed send missing inline error');
  draft='失败后主动发送的新消息';await editor.submit();await frame();
  if(queue.entries.length!==1 || queue.paused || !stop.hidden || document.querySelector('#chat-status').textContent!=='正在回复...') throw Error('new send remained stopped');
  if(requests.some(request=>['queue_pause','queue_resume','queue_cancel'].includes(request.action))) throw Error('obsolete queue action dispatched');
  checkControls();panel.dispose();
  window.__sendIntentResult={ok:true,submissions:requests.filter(request=>request.action==='enqueue').length,
    stoppedReplies:requests.filter(request=>request.action==='cancel').length,failedMessages:queue.failures.length};
})().catch(error=>{window.__sendIntentResult={ok:false,error:String(error)};});
true;
