import {localizeMessage, t} from './i18n.mjs';

export function commandClient(bridge, post) {
  const pending = new Map();
  let sequence = 0, disposed = false;
  const unsubscribe = bridge.subscribe(message => {
    if (message.kind !== 'command_result') return;
    const entry = pending.get(message.id);
    if (!entry) return;
    pending.delete(message.id); clearTimeout(entry.timer);
    if (message.response?.error) {
      const err = message.response.error;
      const text = localizeMessage(err.message_key, err.message_args, err.message);
      entry.reject(Object.assign(new Error(text), {
        code: err.code,
        messageKey: err.message_key || '',
        messageArgs: err.message_args,
        rawMessage: err.message || '',
      }));
    } else entry.resolve(message.response ?? {});
  });
  function call(action, fields = {}) {
    if (disposed) return Promise.reject(Object.assign(new Error(t('quickinput.client.closed')), {messageKey: 'quickinput.client.closed'}));
    const id = crypto.randomUUID();
    const request = {action, ...fields, ...(action === 'launch' ? {sequence: ++sequence} : {})};
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        pending.delete(id);
        const key = action === 'launch' ? 'quickinput.commands_launch_unknown' : 'quickinput.commands_no_response';
        reject(Object.assign(new Error(t(key)), {code: 'unknown', messageKey: key}));
      }, action === 'logs' ? 22000 : 6000);
      pending.set(id, {resolve, reject, timer});
      post('command', {id, command: request});
    });
  }
  function dispose() {
    disposed = true; unsubscribe();
    for (const value of pending.values()) { clearTimeout(value.timer); value.reject(Object.assign(new Error(t('quickinput.client.closed')), {messageKey: 'quickinput.client.closed'})); }
    pending.clear();
  }
  return {call, dispose};
}
