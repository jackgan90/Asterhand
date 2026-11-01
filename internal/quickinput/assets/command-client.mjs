export function commandClient(bridge, post) {
  const pending = new Map();
  let sequence = 0, disposed = false;
  const unsubscribe = bridge.subscribe(message => {
    if (message.kind !== 'command_result') return;
    const entry = pending.get(message.id);
    if (!entry) return;
    pending.delete(message.id); clearTimeout(entry.timer);
    if (message.response?.error) entry.reject(Object.assign(new Error(message.response.error.message), {code: message.response.error.code}));
    else entry.resolve(message.response ?? {});
  });
  function call(action, fields = {}) {
    if (disposed) return Promise.reject(new Error('输入界面已关闭'));
    const id = crypto.randomUUID();
    const request = {action, ...fields, ...(action === 'launch' ? {sequence: ++sequence} : {})};
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(Object.assign(new Error(action === 'launch' ? '启动结果未确认，请检查脚本；未自动重试' : '命令入口未响应，请稍后重试'), {code:'unknown'}));
      }, 6000);
      pending.set(id, {resolve, reject, timer});
      post('command', {id, command: request});
    });
  }
  function dispose() {
    disposed = true; unsubscribe();
    for (const value of pending.values()) { clearTimeout(value.timer); value.reject(new Error('输入界面已关闭')); }
    pending.clear();
  }
  return {call, dispose};
}
