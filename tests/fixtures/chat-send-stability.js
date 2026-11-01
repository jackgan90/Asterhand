// Runs only through the native test's ExecuteScript entry; production modules,
// DOM and CSS are reused, while the transport holds replies before generation.
(async () => {
  const {chat: original, editor: nativeEditor} = await import('./input.js');
  original.dispose();
  const {chatPanel} = await import('./chat-ui.mjs');
  const subscribers = new Set();
  const bridge = {subscribe(fn) { subscribers.add(fn); return () => subscribers.delete(fn); }};
  const emit = value => { for (const fn of subscribers) fn(value); };
  const target = {node_id: 'fixture-node-0001', role_id: 'default'}, conversation = 'fixture-chat-0001';
  let draft = '发送布局回归', turn = null, held, transcriptReady = false;
  const editor = {
    snapshot: () => ({text: draft, revision: 1}),
    stageSubmission() { draft = ''; return true; }, restoreSubmission() {}, clearIfRevision() {},
    setExpanded: value => nativeEditor.setExpanded(value), onSubmit(fn) { this.submit = fn; },
    post(kind, value) {
      if (kind !== 'chat') return;
      const request = value.chat;
      const response = {target, can_send: true, busy: false, prompt: {mode: 'append', state: 'pending'}};
      if (request.action === 'list') response.conversations = [{id: conversation, state: 'ready'}];
      if (request.action === 'history' || request.action === 'reconcile') {
        response.conversation = {id: conversation, state: 'ready'}; response.turn = turn;
        response.page = {messages: transcriptReady ? [{id: 'native-user', role: 'user', turn_id: turn.id,
          blocks: [{type: 'text', text: '发送布局回归'}]}] : []};
      }
      const reply = () => emit({kind: 'chat_result', id: value.id, reply: {response}});
      if (request.action === 'submit') held = prompt => {
        turn = {id: 'fixture-turn-0001', operation_id: request.operation_id, conversation_id: conversation,
          delivery: 'accepted', run: 'running', persistence: 'pending', prompt};
        response.turn = turn; reply();
      };
      else queueMicrotask(reply);
    },
  };
  const panel = chatPanel(bridge, editor);
  const frame = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  const measure = () => ['chat-scroll', 'chat-status'].map(id => document.getElementById(id).getBoundingClientRect().height);
  emit({kind: 'focus'}); await frame();
  // Expansion itself changes the viewport. Take the send baseline only after
  // its exact native allocation is acknowledged and the renderer has caught up.
  const host = window.chrome.webview, post = host.postMessage;
  let resizeID = '', appliedHeight = 0;
  const allocated = event => {
    if (event.data.kind === 'layout_applied' && event.data.id === resizeID) appliedHeight = event.data.height;
  };
  host.postMessage = message => {
    if (message.kind === 'resize') { resizeID = message.id; appliedHeight = 0; }
    post.call(host, message);
  };
  host.addEventListener('message', allocated);
  try {
    document.getElementById('chat-expand').click();
    const deadline = performance.now() + 5000;
    do {
      await frame();
      if (appliedHeight > 0 && Math.abs(innerHeight - appliedHeight) <= 1) break;
      if (performance.now() >= deadline) throw Error('expanded native allocation was not confirmed');
    } while (true);
  } finally {
    host.postMessage = post;
    host.removeEventListener('message', allocated);
  }
  const baseline = measure(), measurements = [baseline], statuses = [];
  const expectWaiting = () => {
    const text = document.getElementById('chat-status').textContent;
    statuses.push(text);
    if (text !== '正在回复...') throw Error('intermediate send status: ' + text);
  };
  const submitted = editor.submit(); await frame();
  expectWaiting();
  measurements.push(measure());
  const row = document.querySelector('.chat-message');
  if (!row || document.querySelectorAll('.chat-message').length !== 1) throw Error('missing immediate single bubble');
  held({mode: 'append', state: 'degraded'}); await submitted; await frame();
  expectWaiting();
  if (document.querySelector('.chat-delivery-status') || document.querySelector('.chat-message') !== row) throw Error('durable receipt before transcript changed bubble');
  measurements.push(measure());
  transcriptReady = true;
  for (const prompt of [{mode: 'append', state: 'pending'}, {mode: 'append', state: 'applied'}, {mode: 'unsupported', state: 'degraded'}]) {
    turn = {...turn, user_message_id: 'native-user', prompt};
    document.getElementById('chat-refresh').click(); await frame();
    expectWaiting();
    measurements.push(measure());
    if (document.querySelectorAll('.chat-message').length !== 1 || document.querySelector('.chat-message') !== row) throw Error('duplicate or replaced send bubble');
    if (document.querySelector('#chat-prompt-status') || /提示词|注入|适配未确认/.test(document.body.textContent)) throw Error('prompt diagnostics shown in chat');
  }
  if (measurements.some(values => values.some((height, i) => Math.abs(height - baseline[i]) > 0.5))) throw Error('send/status changed viewport: ' + JSON.stringify(measurements));
  panel.dispose();
  window.__sendStability = {ok: true, measurements, statuses};
})().catch(error => { window.__sendStability = {ok: false, error: String(error)}; });
true;
