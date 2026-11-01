import {t} from './i18n.mjs';

const labelKeys = {
  connecting: 'quickinput.conn.connecting',
  connected: 'quickinput.conn.connected',
  unavailable: 'quickinput.conn.unavailable',
};

// Serial, visibility-scoped reads keep delayed replies from overwriting a newer
// view. Connection status is independent of message delivery and reply feedback.
export function connectionView(node, client) {
  let active = false, disposed = false, generation = 0, timer = null, running = false, reconnect = false;
  let currentState = 'connecting';
  function show(state) {
    currentState = labelKeys[state] ? state : 'unavailable';
    node.textContent = t(labelKeys[currentState]);
    node.dataset.state = currentState;
  }
  async function read() {
    if (!active || disposed || running) return;
    running = true;
    const version = generation, connect = reconnect;
    reconnect = false;
    try {
      const response = await client.call(connect ? 'connect' : 'connection');
      if (active && version === generation) show(response.connection);
    } catch {
      if (active && version === generation) show('unavailable');
    } finally {
      running = false;
      if (active && !disposed) {
        if (version !== generation || reconnect) void read();
        else timer = setTimeout(read, 1000);
      }
    }
  }
  show('connecting');
  return {
    visible(value) {
      if (active === value || disposed) return;
      active = value; generation++;
      clearTimeout(timer);
      if (active) { reconnect = true; show('connecting'); void read(); }
    },
    refresh() {
      if (!active || disposed) return;
      generation++; reconnect = true; clearTimeout(timer); show('connecting'); void read();
    },
    unavailable() { if (!disposed) show('unavailable'); },
    relocalize() { if (!disposed) show(currentState); },
    dispose() { disposed = true; active = false; generation++; clearTimeout(timer); },
  };
}
