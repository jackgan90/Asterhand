import {spawn} from 'node:child_process';

const delay = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));
const failure = (code, messageKey, message) =>
  Object.assign(new Error(message), {code, message_key: messageKey, publicMessage: true});

// Locked OpenClaw admission reasons, before a user turn is persisted. Other
// INVALID_REQUEST errors and transport failures remain unknown; never replay.
export function submissionError(error) {
  if (error.notDispatched === true) {
    return failure('rejected', 'err.chatbridge.not_dispatched_conn', 'Connection was not ready before sending. Draft preserved; refresh and send again.');
  }
  if (error.native?.code === 'INVALID_REQUEST' &&
    ['active-leaf-changed', 'session-settings-changed', 'session-routing-changed'].includes(error.native?.details?.reason)) {
    return failure('rejected', 'err.chatbridge.not_dispatched_session', 'Session changed before sending. Draft preserved; refresh and send again.');
  }
  return error;
}

// This transport is private to the adapter. Callers use the closed operations in
// main.mjs; browser and host requests never supply a Gateway method name.
export class Gateway {
  constructor(onEvent, onExit = () => {}) {
    this.onEvent = onEvent;
    this.onExit = onExit;
    this.pending = new Map();
    this.sequence = 0;
    this.started = Date.now();
  }

  async start({entry, port, token, startup_timeout_ms = 55000}) {
    if (!Number.isInteger(startup_timeout_ms) || startup_timeout_ms < 1 || startup_timeout_ms > 180000) {
      throw failure('invalid_request', 'err.chatbridge.invalid_wait', 'Invalid runtime startup timeout parameter.');
    }
    this.port = port;
    this.token = token;
    // Go has already assigned this bridge to its kill-on-close Job. Children
    // therefore inherit ownership before they execute runtime code.
    this.child = spawn(process.execPath, [entry, 'gateway', 'run', '--port', String(port), '--bind', 'loopback'], {
      cwd: process.cwd(), env: process.env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'],
    });
    // Go drains and filters startup output into fixed, bounded diagnostic fields.
    // After authentication, discard runtime output including all model content.
    let starting = true;
    let diagnosticBytes = 0;
    const diagnostic = chunk => {
      if (!starting || diagnosticBytes >= 256 * 1024) return;
      const part = chunk.subarray(0, 256 * 1024 - diagnosticBytes);
      diagnosticBytes += part.length;
      process.stderr.write(part);
    };
    this.child.stdout.on('data', diagnostic);
    this.child.stderr.on('data', diagnostic);
    this.child.on('error', () => { this.childFailed = true; this.onExit(); });
    this.child.on('exit', (code, signal) => {
      const safeSignal = ['SIGTERM', 'SIGKILL', 'SIGINT', 'SIGABRT', 'SIGSEGV'].includes(signal) ? signal : 'none';
      process.stderr.write(`[asterhand] gateway exited code=${Number.isInteger(code) ? code : 'null'} signal=${safeSignal}\n`);
      this.childFailed = true;
      this.disconnect();
      this.onExit();
    });
    const deadline = Date.now() + startup_timeout_ms;
    while (Date.now() < deadline) {
      if (this.childFailed) throw failure('unavailable', 'err.chatbridge.child_failed', 'Chat runtime failed to start.');
      try {
        await this.connect();
        process.stderr.write('[asterhand] gateway authenticated\n');
        starting = false;
        return;
      } catch {
        this.disconnect();
        await delay(300);
      }
    }
    process.stderr.write('[asterhand] gateway startup timeout\n');
    throw failure('unavailable', 'err.chatbridge.startup_timeout', 'Chat runtime startup timed out.');
  }

  disconnect() {
    this.authenticated = false;
    const socket = this.socket;
    this.socket = null;
    socket?.close();
    for (const entry of this.pending.values()) {
      entry.reject(failure('unavailable', 'err.conv.runtime.conn_interrupted', 'Chat connection interrupted. Check operation status.'));
    }
    this.pending.clear();
  }

  async connect() {
    this.authenticated = false;
    const socket = new WebSocket(`ws://127.0.0.1:${this.port}`);
    this.socket = socket;
    await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(failure('unavailable', 'err.chatbridge.handshake_timeout', 'Chat handshake timed out.')), 4000);
      const fail = () => { clearTimeout(timer); reject(failure('unavailable', 'err.chatbridge.conn_unavailable', 'Chat connection is unavailable.')); };
      socket.onerror = fail;
      socket.onclose = () => {
        fail();
        if (this.socket === socket) this.disconnect();
      };
      socket.onmessage = event => {
        if (this.socket !== socket) return;
        if (typeof event.data !== 'string' || event.data.length > 40 * 1024 * 1024) {
          this.disconnect();
          return;
        }
        let frame;
        try { frame = JSON.parse(event.data); } catch { this.disconnect(); return; }
        if (frame.type === 'event' && frame.event === 'connect.challenge') {
          clearTimeout(timer);
          resolve();
        } else if (frame.type === 'res') {
          const entry = this.pending.get(frame.id);
          if (!entry) return;
          this.pending.delete(frame.id);
          if (frame.ok) entry.resolve(frame.payload);
          else entry.reject(Object.assign(failure('unavailable', 'err.chatbridge.request_incomplete', 'Runtime request did not complete. Check status.'), {native: frame.error}));
        } else if (frame.type === 'event') {
          this.onEvent(frame);
        }
      };
    });
    await this.rpc('connect', {
      minProtocol: 4, maxProtocol: 4,
      client: {id: 'cli', version: 'asterhand-chat-v1', platform: process.platform, mode: 'cli'},
      role: 'operator', scopes: ['operator.read', 'operator.write'], caps: [], auth: {token: this.token},
    });
    if (this.socket !== socket || socket.readyState !== WebSocket.OPEN) {
      throw failure('unavailable', 'err.chatbridge.auth_interrupted', 'Chat connection interrupted during authentication.');
    }
    this.authenticated = true;
  }

  async ensureConnected() {
    if (this.reconnecting) return this.reconnecting;
    if (this.authenticated && this.socket?.readyState === WebSocket.OPEN) return;
    if (!this.reconnecting) this.reconnecting = this.reconnect()
      .catch(error => { throw Object.assign(error, {notDispatched: true}); })
      .finally(() => { this.reconnecting = null; });
    return this.reconnecting;
  }

  async reconnect() {
    for (const wait of [500, 1000, 2000]) {
      if (this.childFailed) throw failure('unavailable', 'err.chatbridge.child_exited', 'Chat runtime has exited.');
      await delay(wait);
      try { await this.connect(); return; } catch { this.disconnect(); }
    }
    throw failure('unavailable', 'err.chatbridge.reconnect_failed', 'Chat reconnection failed. Refresh to check status.');
  }

  rpc(method, params, timeout = 15000) {
    if (this.socket?.readyState !== WebSocket.OPEN || method !== 'connect' && !this.authenticated || this.pending.size >= 16) {
      return Promise.reject(Object.assign(failure('unavailable', 'err.chatbridge.temp_unavailable', 'Chat connection is temporarily unavailable.'), {notDispatched: true}));
    }
    const id = String(++this.sequence);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(failure('unavailable', 'err.chatbridge.response_timeout', 'Runtime response timed out. Check operation status.'));
      }, timeout);
      this.pending.set(id, {
        resolve: value => { clearTimeout(timer); resolve(value); },
        reject: error => { clearTimeout(timer); reject(error); },
      });
      try { this.socket.send(JSON.stringify({type: 'req', id, method, params})); }
      catch (error) { this.pending.get(id)?.reject(error); this.pending.delete(id); }
    });
  }

  async readyRead(method, params) {
    const deadline = Date.now() + 55000;
    for (;;) {
      try { return await this.rpc(method, params); }
      catch (error) {
        // This native error explicitly precedes admission and persistence.
        // Other errors, especially unknown writes, are never replayed.
        if (error.native?.details?.code !== 'agent-database-inspection-pending' || Date.now() >= deadline) throw error;
        await delay(500);
      }
    }
  }
}

export {failure};
