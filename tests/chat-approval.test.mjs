import test from 'node:test';
import assert from 'node:assert/strict';
import {messageRenderer} from '../internal/quickinput/assets/chat-renderer.mjs';
import {ChatWindow} from '../internal/quickinput/assets/chat-model.mjs';
import {setLocale} from '../internal/quickinput/assets/i18n.mjs';
import {RenderNode, rendererDocument, withClass} from './chat-renderer-fixture.mjs';

test('pending approval card expands in place with non-technical copy, hides always for generic shell, and collapses once resolved', async t => {
  rendererDocument(t);
  setLocale('zh-CN');
  const container = new RenderNode('main');
  const decisions = [];
  const render = messageRenderer(container, {
    copy() {},
    expand() {},
    approve: async (id, decision) => { decisions.push([id, decision]); },
  });

  const windowState = new ChatWindow();
  windowState.apply({messages: [{id: 'm1', role: 'user', turn_id: 't1', blocks: [{type: 'text', text: 'run'}]}]});
  windowState.mergeActivities([{
    id: 'call-1',
    turn_id: 't1',
    tool: 'exec_command',
    summary: 'powershell ...',
    state: 'awaiting_approval',
    sequence: 1,
    approval: {
      id: 'ap_1234567890123456',
      status: 'pending',
      kind: 'folder',
      target: 'D:\\Reports',
      reason: '需要写入导出报表',
      command_preview: 'python export.py',
      can_allow_session: true,
      can_allow_always: true,
    },
  }], 't1');
  // Streaming live text must not wipe the transient awaiting_approval row.
  windowState.live({id: 'live-t1', role: 'assistant', turn_id: 't1', transient: true, blocks: [{type: 'text', text: 'working...'}]});
  render(windowState.messages);

  let cards = withClass(container, 'tool-approval-card');
  assert.equal(cards.length, 1);
  assert.match(cards[0].textContent, /请求访问文件夹.*D:\\Reports.*需要写入导出报表.*当前 Windows 账户权限运行/s);
  assert.doesNotMatch(cards[0].textContent, /Tier 2|OrdinaryToken|WRITE_RESTRICTED|TOCTOU/);
  const buttons = cards[0].children.find(c => c.className === 'tool-approval-actions').children;
  assert.deepEqual(buttons.map(b => b.dataset.decision), ['allow_session', 'allow_once', 'allow_always', 'deny']);

  setLocale('en-US');
  render.relocalize();
  assert.match(cards[0].textContent, /Folder access request.*current Windows account permissions.*Allow in this chat/s);

  // Simulate click on allow_session, then server resolving the operation.
  buttons[0].listeners.get('click')();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.deepEqual(decisions, [['ap_1234567890123456', 'allow_session']]);

  windowState.mergeActivities([{
    id: 'call-1',
    turn_id: 't1',
    tool: 'exec_command',
    summary: 'powershell ...',
    state: 'completed',
    sequence: 2,
    approval: {id: 'ap_1234567890123456', status: 'approved', kind: 'folder', target: 'D:\\Reports', reason: '需要写入导出报表'},
  }], 't1');
  render(windowState.messages);
  assert.equal(withClass(container, 'tool-approval-card').length, 0);

  // Generic workspace shell omits allow_always button cleanly.
  windowState.mergeActivities([{
    id: 'call-2',
    turn_id: 't1',
    tool: 'exec_command',
    state: 'awaiting_approval',
    sequence: 3,
    approval: {
      id: 'ap_9876543210987654',
      status: 'pending',
      kind: 'workspace_escalated',
      reason: 'Access system network',
      can_allow_session: true,
      can_allow_always: false,
    },
  }], 't1');
  render(windowState.messages);
  cards = withClass(container, 'tool-approval-card');
  assert.equal(cards.length, 1);
  const shellDecisions = cards[0].children.find(c => c.className === 'tool-approval-actions').children.map(b => b.dataset.decision);
  assert.deepEqual(shellDecisions, ['allow_session', 'allow_once', 'deny']);

  // Multi-folder request renders one pill per target folder.
  windowState.mergeActivities([{
    id: 'call-multi',
    turn_id: 't1',
    tool: 'exec_command',
    state: 'awaiting_approval',
    sequence: 4,
    approval: {
      id: 'ap_multi1234567890',
      status: 'pending',
      kind: 'folder',
      target: 'D:\\Alpha, E:\\Beta',
      targets: ['D:\\Alpha', 'E:\\Beta'],
      reason: 'Compare two folders',
      can_allow_session: true,
      can_allow_always: true,
    },
  }], 't1');
  render(windowState.messages);
  const pills = withClass(container, 'tool-approval-pill').map(node => node.textContent);
  assert.deepEqual(pills, ['D:\\Alpha', 'E:\\Beta']);

  // Denied and approval_timeout activities show distinct non-completed statuses.
  setLocale('zh-CN');
  windowState.mergeActivities([
    {id: 'call-multi', turn_id: 't1', tool: 'exec_command', state: 'denied', detail: 'denied_by_user', sequence: 5},
    {id: 'call-2', turn_id: 't1', tool: 'exec_command', state: 'failed', detail: 'approval_timeout', sequence: 6},
  ], 't1');
  render(windowState.messages);
  const stateNodes = withClass(container, 'tool-activity-state').map(n => n.textContent);
  assert.ok(stateNodes.some(text => text.includes('未允许')));
  assert.ok(stateNodes.some(text => text.includes('确认超时')));
  setLocale('en-US');
  render.relocalize();
  const enStateNodes = withClass(container, 'tool-activity-state').map(n => n.textContent);
  assert.ok(enStateNodes.some(text => text.includes('Not allowed')));
  assert.ok(enStateNodes.some(text => text.includes('Timed out')));
});

test('approval card keeps exact button DOM references across polls, relocalize, and transient-to-persisted handoff', async t => {
  rendererDocument(t);
  setLocale('zh-CN');
  const container = new RenderNode('main');
  const decisions = [];
  const render = messageRenderer(container, {
    copy() {},
    expand() {},
    approve: async (id, decision) => { decisions.push([id, decision]); },
  });
  const pendingAct = (sequence = 1) => ({
    id: 'call-poll-stable',
    turn_id: 't1',
    tool: 'exec_command',
    summary: 'cmd /c echo hi',
    state: 'awaiting_approval',
    sequence,
    approval: {
      id: 'ap_stable1234567890',
      status: 'pending',
      kind: 'folder',
      target: 'D:\\Reports',
      targets: ['D:\\Reports'],
      reason: '需要写入报表',
      command_preview: 'echo hi',
      can_allow_session: true,
      can_allow_always: true,
    },
  });

  const windowState = new ChatWindow();
  windowState.apply({messages: [{id: 'm1', role: 'user', turn_id: 't1', blocks: [{type: 'text', text: 'go'}]}]});
  windowState.mergeActivities([pendingAct(1)], 't1');
  render(windowState.messages);

  const actionsBefore = withClass(container, 'tool-approval-actions')[0];
  const initialButtons = [...actionsBefore.children];
  assert.equal(initialButtons.length, 4);

  // Repeated activity polls + live text + relocalize must not replace button DOM elements.
  windowState.mergeActivities([pendingAct(2)], 't1');
  windowState.live({id: 'live-t1', role: 'assistant', turn_id: 't1', transient: true, blocks: [{type: 'text', text: 'waiting'}]});
  render(windowState.messages);
  render.relocalize();

  let actionsAfter = withClass(container, 'tool-approval-actions')[0];
  assert.equal(actionsAfter, actionsBefore);
  initialButtons.forEach((btn, idx) => {
    assert.equal(actionsAfter.children[idx], btn);
    assert.equal(btn.parent, actionsAfter);
  });

  // Transition from transient live-tool-* message to persisted assistant message (with an added text block)
  // reuses the same toolActivity & button references.
  windowState.apply({messages: [
    {id: 'm1', role: 'user', turn_id: 't1', blocks: [{type: 'text', text: 'go'}]},
    {id: 'm2-persisted', role: 'assistant', turn_id: 't1', blocks: [
      {type: 'tool', tool: pendingAct(3)},
      {type: 'text', id: 'm2-persisted_text_1', text: '正在请求确认…'},
    ]},
  ]});
  render(windowState.messages);

  actionsAfter = withClass(container, 'tool-approval-actions')[0];
  initialButtons.forEach((btn, idx) => {
    assert.equal(actionsAfter.children[idx], btn);
    assert.equal(btn.parent, actionsAfter);
  });

  // First click on a non-primary button succeeds on initial click.
  actionsAfter.children[2].listeners.get('click')();
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.deepEqual(decisions, [['ap_stable1234567890', 'allow_always']]);
});
