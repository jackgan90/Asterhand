import test from 'node:test';
import assert from 'node:assert/strict';
import {replyStatusView} from '../internal/quickinput/assets/chat-status.mjs';
import {RenderNode, rendererDocument} from './chat-renderer-fixture.mjs';

test('send, preparation and generation share one reply status; hiding and errors stop animation', t => {
  rendererDocument(t);
  const container = new RenderNode();
  const view = replyStatusView(container);
  const running = {delivery: 'accepted', run: 'running', persistence: 'saved'};
  const [label, dots] = container.children;
  view.visible(true);
  for (const turn of [null, {delivery: 'queued', run: 'queued'}, {delivery: 'pending', run: 'pending'}, {delivery: 'accepted', run: 'pending'}, running]) {
    view.turn(turn, '', turn === null);
    assert.equal(container.textContent, '正在回复...'); assert.equal(dots.hidden, false);
  }
  assert.equal(label.textContent, '正在回复'); assert.equal(dots.hidden, false);
  assert.equal(dots.getAttribute('aria-hidden'), 'true');
  view.visible(false); assert.equal(dots.hidden, true);
  view.visible(true); assert.equal(dots.hidden, false);
  view.turn({...running, delivery: 'unknown'}); assert.equal(dots.hidden, true);
  view.turn({...running, run: 'completed'}); assert.equal(container.textContent, '');
  view.turn({...running, run: 'completed', persistence: 'unknown'});
  assert.equal(label.textContent, '回复保存状态待确认');
  view.turn(running); view.message('连接失败'); assert.equal(dots.hidden, true);
  view.turn(running); view.dispose(); assert.equal(dots.hidden, true);
});
