import test from 'node:test';
import assert from 'node:assert/strict';
import {messageRenderer} from '../internal/quickinput/assets/chat-renderer.mjs';
import {ChatWindow, MAX_BODY_BYTES, bodyBytes} from '../internal/quickinput/assets/chat-model.mjs';
import {setLocale} from '../internal/quickinput/assets/i18n.mjs';
import {RenderNode, rendererDocument, withClass} from './chat-renderer-fixture.mjs';

const tool = (id, name, state = 'started') => ({type: 'tool', tool: {id, tool: name, state}});
const mixed = () => ({id: 'mixed', role: 'assistant', blocks: [
  tool('first-call', 'list_dir'), {type: 'text', id: 'part-one', text: 'first words'},
  tool('second-call', 'read_file'), {type: 'text', id: 'part-two', text: 'last words', truncated: true},
]});

test('tools and text share native order and status updates preserve their DOM positions', t => {
  rendererDocument(t); setLocale('zh-CN');
  const container = new RenderNode('main');
  const expand = [];
  const render = messageRenderer(container, {copy() {}, expand: id => expand.push(id)});
  const message = mixed();
  const final = {id: 'final', role: 'assistant', blocks: [{type: 'text', text: 'final answer'}]};
  render([message, final]);
  const body = withClass(container, 'chat-message-body')[0];
  assert.deepEqual(body.children.map(node => node.className), ['tool-activity', 'chat-message-text', 'tool-activity', 'chat-message-text']);
  assert.match(container.textContent, /list_dir.*first words.*read_file.*last words.*final answer/s);
  const [firstCall, firstText, secondCall] = body.children;
  message.blocks[0].tool.state = 'completed';
  message.blocks[2].tool.state = 'failed';
  render([message, final]);
  assert.equal(withClass(container, 'chat-message-body')[0], body);
  assert.deepEqual(body.children.slice(0, 3), [firstCall, firstText, secondCall]);
  assert.match(firstCall.textContent, /已完成/);
  assert.match(secondCall.textContent, /失败/);
  const button = body.children[3].children.find(node => node.tagName === 'button');
  button.listeners.get('click')();
  assert.deepEqual(expand, ['part-two']);
  setLocale('en-US'); render.relocalize();
  assert.match(firstCall.textContent, /Completed/);
  assert.equal(body.children[1], firstText);
  assert.match(button.textContent, /full message/i);
  render.dispose(); setLocale('zh-CN');
});

test('tool-only messages contain the tool itself without an empty author bubble', t => {
  rendererDocument(t);
  const container = new RenderNode('main');
  const render = messageRenderer(container, {copy() {}, expand() {}});
  render([{id: 'only-call', role: 'assistant', blocks: [tool('call', 'write_file', 'completed')]}]);
  assert.equal(withClass(container, 'chat-author').length, 0);
  assert.equal(withClass(container, 'chat-tool-only').length, 1);
  assert.equal(withClass(container, 'tool-activity').length, 1);
  assert.match(container.textContent, /write_file/);
  render.dispose();
});

test('out-of-order parallel tool completions preserve original assistant message call order', t => {
  rendererDocument(t); setLocale('zh-CN');
  const container = new RenderNode('main');
  const render = messageRenderer(container, {copy() {}, expand() {}});
  const batchMessage = {
    id: 'parallel-batch',
    role: 'assistant',
    blocks: [
      tool('call-1', 'write_file', 'started'),
      tool('call-2', 'write_file', 'queued'),
      tool('call-3', 'read_file', 'started'),
    ],
  };
  render([batchMessage]);
  const body = withClass(container, 'chat-message-body')[0];
  assert.deepEqual(body.children.map(node => node.dataset.callId), ['call-1', 'call-2', 'call-3']);
  assert.match(body.children[1].textContent, /排队中/);
  // Call 3 finishes first, call 2 starts while call 1 is still running.
  batchMessage.blocks[2].tool.state = 'completed';
  batchMessage.blocks[1].tool.state = 'started';
  render([batchMessage]);
  assert.deepEqual(body.children.map(node => node.dataset.callId), ['call-1', 'call-2', 'call-3']);
  assert.deepEqual(body.children.map(node => node.dataset.state), ['started', 'started', 'completed']);
  // Call 2 finishes before call 1.
  batchMessage.blocks[1].tool.state = 'completed';
  batchMessage.blocks[0].tool.state = 'completed';
  render([batchMessage]);
  assert.deepEqual(body.children.map(node => node.dataset.callId), ['call-1', 'call-2', 'call-3']);
  assert.deepEqual(body.children.map(node => node.dataset.state), ['completed', 'completed', 'completed']);
  render.dispose();
});

test('expanding a text segment preserves neighboring tools and text within the window budget', () => {
  const window = new ChatWindow();
  window.apply({messages: [mixed(), ...Array.from({length: 50}, (_, i) => ({id: String(i), role: 'assistant',
    blocks: [{type: 'text', text: 'x'.repeat(30000)}]}))]});
  const original = window.messages[0];
  const firstCall = original.blocks[0], firstText = original.blocks[1], secondCall = original.blocks[2];
  const budget = window.makeRoom('part-two', 1024 * 1024);
  assert.ok(budget >= 1024 * 1024);
  window.replace('part-two', 'full segment', true, 8192, 16384);
  assert.equal(window.messages[0], original);
  assert.deepEqual(original.blocks.slice(0, 3), [firstCall, firstText, secondCall]);
  assert.equal(window.fragmentNext('part-two'), 16384);
  assert.equal(original.blocks[3].text, 'full segment');
  assert.ok(window.messages.reduce((sum, m) => sum + bodyBytes(m), 0) <= MAX_BODY_BYTES);
  assert.equal(window.pendingTools(), true);
  firstCall.tool.state = secondCall.tool.state = 'completed';
  assert.equal(window.pendingTools(), false);
});

test('toolActivity renders Option A target summaries, tooltips, in-place updates, and relocalization', t => {
  rendererDocument(t); setLocale('zh-CN');
  const container = new RenderNode('main');
  const render = messageRenderer(container, {copy() {}, expand() {}});
  const msg = {
    id: 'summary-msg',
    role: 'assistant',
    blocks: [
      {type: 'text', id: 'txt-1', text: 'checking files'},
      {type: 'tool', tool: {id: 'c-read', tool: 'read_file', state: 'started', summary: 'docs/plan.md:1-40'}},
      {type: 'tool', tool: {id: 'c-prog', tool: 'progress_card', state: 'completed'}},
    ],
  };
  render([msg]);
  const heads = withClass(container, 'tool-activity-head');
  assert.equal(heads.length, 2);
  assert.equal(heads[0].textContent, 'read_file docs/plan.md:1-40 · 执行中');
  assert.equal(heads[0].title, 'read_file docs/plan.md:1-40 · 执行中');
  assert.equal(withClass(heads[0], 'tool-activity-target')[0].textContent, 'docs/plan.md:1-40');
  assert.equal(heads[1].textContent, 'progress_card · 已完成');
  assert.equal(heads[1].title, '');
  assert.equal(withClass(heads[1], 'tool-activity-target').length, 0);

  const body = withClass(container, 'chat-message-body')[0];
  const [textNode, readNode, progNode] = body.children;
  msg.blocks[1].tool = {id: 'c-read', tool: 'read_file', state: 'completed', summary: 'docs/plan.md:1-60'};
  render([msg]);
  assert.equal(body.children[0], textNode);
  assert.equal(body.children[1], readNode);
  assert.equal(body.children[2], progNode);
  assert.equal(heads[0].textContent, 'read_file docs/plan.md:1-60 · 已完成');
  assert.equal(heads[0].title, 'read_file docs/plan.md:1-60 · 已完成');

  setLocale('en-US'); render.relocalize();
  assert.equal(heads[0].textContent, 'read_file docs/plan.md:1-60 · Completed');
  assert.equal(heads[0].title, 'read_file docs/plan.md:1-60 · Completed');
  assert.equal(heads[1].textContent, 'progress_card · Completed');
  assert.equal(heads[1].title, '');
  render.dispose(); setLocale('zh-CN');
});
