import test from 'node:test';
import assert from 'node:assert/strict';
import {ConnectionState} from '../internal/executor/chatbridge/connection.mjs';

test('connection uses socket authentication and reconnect lifecycle without RPC or model calls', () => {
  const state = new ConnectionState();
  const gateway = {socket: {readyState: 1}, authenticated: false};
  assert.equal(state.snapshot(gateway).state, 'unavailable');
  gateway.authenticated = true;
  assert.equal(state.snapshot(gateway).state, 'connected');
  gateway.socket.readyState = 3;
  assert.equal(state.snapshot(gateway).state, 'unavailable');
  gateway.reconnecting = Promise.resolve();
  assert.equal(state.snapshot(gateway).state, 'connecting');
  gateway.reconnecting = null; gateway.socket.readyState = 1;
  assert.equal(state.snapshot(gateway).state, 'connected');
});

test('actual model failure remains unavailable until a successful reply; abort and text do not clear it', () => {
  const state = new ConnectionState(), gateway = {socket: {readyState: 1}, authenticated: true};
  const event = (status, extra = {}) => state.event({event: 'chat', payload: {runId:'run1', sessionKey:'session1', state:status, ...extra}});
  event('error', {errorMessage: '401 invalid API key'});
  assert.deepEqual(state.snapshot(gateway), {state: 'unavailable'});
  event('aborted'); event('delta', {deltaText: '已连接'});
  assert.equal(state.snapshot(gateway).state, 'unavailable');
  event('final', {message: {stopReason: 'error'}});
  assert.equal(state.snapshot(gateway).state, 'unavailable');
  event('final', {message: {stopReason: 'stop'}});
  assert.equal(state.snapshot(gateway).state, 'connected');
  state.event({event:'other', payload:{state:'error'}});
  assert.equal(state.snapshot(gateway).state, 'connected');
});
