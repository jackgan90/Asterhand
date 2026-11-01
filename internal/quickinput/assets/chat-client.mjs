// The client speaks semantic conversation operations through the sealed bridge.
// No runtime address, credential, cookie, or browser storage is involved.
export function conversationClient(bridge, editor, onEnvelope) {
  const pending = new Map();
  const reads = new Map();
  let target = null;
  const unsubscribe = bridge.subscribe(message => {
    if (message.kind !== 'chat_result' || !message.reply) return;
    const envelope = message.reply;
    const entry = pending.get(message.id);
    const response = envelope.response ?? {};
    if (response.target?.node_id) target = response.target;
    if (envelope.response?.connection === undefined && !['connect', 'connection'].includes(entry?.action)) onEnvelope(envelope);
    if (!entry) return;
    pending.delete(message.id);
    clearTimeout(entry.timer);
    if (response.error) entry.reject(Object.assign(new Error(response.error.message), {code: response.error.code, response, envelope}));
    else entry.resolve({...response, envelope});
  });

  function dispatch(action, fields) {
    const priority = ['select','prepare','enqueue','queue','queue_item','queue_cancel','queue_pause','queue_resume'].includes(action);
    if (pending.size >= (priority ? 8 : 4)) return Promise.reject(Object.assign(new Error('会话入口正在处理请求'), {notDispatched: true}));
    if (action !== 'hello' && !target) return Promise.reject(Object.assign(new Error('请先连接本机助手'), {notDispatched: true}));
    const id = crypto.randomUUID();
    const request = {action, target: action === 'hello' ? {} : target, ...fields};
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(Object.assign(new Error('响应尚未确认，请刷新核对；草稿已保留'), {code: 'unknown'}));
      }, 100000);
      pending.set(id, {resolve, reject, timer, action});
      editor.post('chat', {id, revision: fields.draft_revision ?? 0, chat: request});
    });
  }

  function call(action, fields = {}) {
    if (!['hello','list','history','message','queue','queue_item'].includes(action)) return dispatch(action, fields);
    const key = JSON.stringify([action, fields]);
    if (!reads.has(key)) reads.set(key, dispatch(action, fields).finally(() => reads.delete(key)));
    return reads.get(key);
  }

  function dispose() {
    unsubscribe();
    for (const entry of pending.values()) { clearTimeout(entry.timer); entry.reject(new Error('输入界面已关闭')); }
    pending.clear();
  }
  return {call, dispose};
}
