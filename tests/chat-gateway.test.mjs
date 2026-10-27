import test from 'node:test';
import assert from 'node:assert/strict';
import {Gateway, submissionError} from '../internal/executor/chatbridge/gateway.mjs';
test('concurrent reconnect callers wait for authentication, not just an open socket', async () => {
  let opened, release;
  const didOpen = new Promise(resolve => opened = resolve);
  const authorized = new Promise(resolve => release = resolve);
  const gateway = new Gateway(() => {});
  gateway.connect = async () => { gateway.socket = {readyState: WebSocket.OPEN}; opened(); await authorized; };
  const first = gateway.ensureConnected();
  await didOpen;
  let secondResolved = false;
  const second = gateway.ensureConnected().then(() => { secondResolved = true; });
  await Promise.resolve(); await Promise.resolve();
  try { assert.equal(secondResolved, false, 'caller escaped before connect authentication completed'); }
  finally { release(); await Promise.all([first,second]); }
});

test('only proven native pre-admission reasons are definite rejections', () => {
  for (const reason of ['active-leaf-changed', 'session-settings-changed', 'session-routing-changed']) {
    const error = {code: 'unavailable', native: {code: 'INVALID_REQUEST', details: {reason}}};
    assert.equal(submissionError(error).code, 'rejected');
    assert.equal(submissionError(error).native, undefined);
  }
  for (const error of [{code:'unavailable'}, {native:{code:'INVALID_REQUEST'}},
    {native:{code:'INVALID_REQUEST',details:{reason:'chat-request-conflict'}}},
    {native:{code:'UNAVAILABLE',details:{reason:'active-leaf-changed'}}}]) {
    assert.equal(submissionError(error), error);
  }
});

test('business requests cannot bypass authentication readiness', async () => {
  const gateway = new Gateway(() => {});
  let sent = false;
  gateway.socket = {readyState: WebSocket.OPEN, send() { sent = true; }, close() {}};
  await assert.rejects(gateway.rpc('chat.history', {}));
  assert.equal(sent, false);
  gateway.authenticated = true;
  gateway.disconnect();
  assert.equal(gateway.authenticated, false);
});

test('failed reconnect and local pre-send checks release only definitely undispatched submissions', async () => {
  const gateway = new Gateway(() => {});
  gateway.reconnect = async () => { throw new Error('fixture offline'); };
  await assert.rejects(gateway.ensureConnected(), error => submissionError(error).code === 'rejected');
  for (const state of ['closed', 'unauthenticated', 'capacity']) {
    gateway.socket = {readyState: state === 'closed' ? WebSocket.CLOSED : WebSocket.OPEN, send() { assert.fail('pre-send refusal wrote request'); }};
    gateway.authenticated = state !== 'unauthenticated';
    gateway.pending = new Map(state === 'capacity' ? Array.from({length:16}, (_,i) => [String(i), {}]) : []);
    await assert.rejects(gateway.rpc('chat.send', {}), error => submissionError(error).code === 'rejected');
  }
});

test('write attempts and response loss remain unknown', async () => {
  const gateway = new Gateway(() => {});
  gateway.authenticated = true;
  gateway.socket = {readyState:WebSocket.OPEN, send() { throw new Error('fixture write failed'); }};
  await assert.rejects(gateway.rpc('chat.send', {}), error => submissionError(error) === error && error.notDispatched !== true);
  gateway.socket.send = () => {};
  await assert.rejects(gateway.rpc('chat.send', {}, 1), error => submissionError(error) === error && error.code === 'unavailable' && error.notDispatched !== true);
});
