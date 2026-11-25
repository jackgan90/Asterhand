import test from 'node:test';
import assert from 'node:assert/strict';
import {workerActionView} from '../internal/quickinput/assets/worker-action.mjs';

function makeFakeElement(id = '', tag = 'div') {
  const listeners = new Map();
  const attrs = new Map();
  const classes = new Set();
  const el = {
    id,
    tagName: tag.toUpperCase(),
    textContent: '',
    value: '',
    hidden: false,
    disabled: false,
    dataset: {},
    children: [],
    classList: {
      toggle(name, on) { if (on) classes.add(name); else classes.delete(name); },
      contains(name) { return classes.has(name); },
    },
    setAttribute(k, v) { attrs.set(k, String(v)); },
    getAttribute(k) { return attrs.get(k) ?? null; },
    addEventListener(ev, fn) {
      if (!listeners.has(ev)) listeners.set(ev, []);
      listeners.get(ev).push(fn);
    },
    dispatch(ev, payload = {}) {
      for (const fn of (listeners.get(ev) || [])) fn(payload);
    },
    appendChild(child) { this.children.push(child); return child; },
    replaceChildren(...kids) { this.children = [...kids]; },
    querySelectorAll() { return []; },
    focus() {},
    scrollHeight: 0,
    getBoundingClientRect() { return {height: el.hidden ? 0 : (el._rectHeight ?? 280)}; },
  };
  return el;
}

function makeFakeDoc() {
  const ids = [
    'action-root', 'action-collapsed', 'action-collapsed-text', 'action-expand-btn',
    'action-card', 'action-kind-badge', 'action-source', 'action-pager', 'action-prev',
    'action-counter', 'action-next', 'action-collapse-btn', 'action-queue-summary',
    'action-approval-body', 'action-tool-pill', 'action-risk-pill', 'action-project',
    'action-summary', 'action-cmd-wrap', 'action-cmd', 'action-cmd-toggle', 'action-paths',
    'action-reason', 'btn-allow-once', 'btn-allow-session', 'btn-deny', 'btn-cancel-approval',
    'action-input-body', 'action-input-project', 'action-input-note', 'action-questions', 'action-answer-input',
    'btn-send-answer', 'btn-cancel-input',
  ];
  const map = new Map(ids.map(id => [id, makeFakeElement(id)]));
  return {
    documentElement: makeFakeElement('html', 'html'),
    getElementById(id) { return map.get(id); },
    createElement(tag) { return makeFakeElement('', tag); },
    querySelectorAll() { return []; },
  };
}

test('workerActionView handles approval/question queue switching, hard-redline hiding, and IME-safe answers', () => {
  const doc = makeFakeDoc();
  const sent = [];
  let onMessage = null;
  const bridge = {
    send(msg) { sent.push(msg); },
    subscribe(fn) { onMessage = fn; },
  };

  workerActionView({bridge, document: doc, frame: fn => fn()});
  assert.equal(sent[0].kind, 'ready');

  onMessage({kind: 'init', generation: 'g1', theme: 'star', locale: 'zh-CN'});
  assert.equal(sent[1].kind, 'initialized');

  const items = [
    {
      request_id: 'wir_appr',
      kind: 'approval',
      worker_alias: 'minidb-dsh',
      harness_id: 'deepseek-harness',
      workspace_root: 'F:\\proj\\minidb',
      tool_name: 'pwsh',
      summary: 'Run shutdown',
      command: 'shutdown /s /t 0',
      risk_level: 'critical',
      hard_redline: true,
      allow_session_ok: false,
    },
    {
      request_id: 'wir_q1',
      kind: 'input',
      worker_alias: 'minidb-claude',
      harness_id: 'claude-code',
      workspace_root: 'F:\\proj\\minidb',
      summary: 'Pick test runner',
      escalation_note: 'Verified repo uses pytest in CI',
      questions: [{id: 'q1', question: 'Pick test runner', options: [{label: 'pytest'}, {label: 'unittest'}]}],
    },
  ];
  onMessage({kind: 'worker_actions', items, layout: 1, theme: 'star', locale: 'zh-CN'});

  // Item 0: hard-redline approval hides allow_session button.
  assert.equal(doc.getElementById('action-approval-body').hidden, false);
  assert.equal(doc.getElementById('action-input-body').hidden, true);
  assert.equal(doc.getElementById('btn-allow-session').hidden, true);
  assert.equal(doc.getElementById('action-counter').textContent, '1 / 2');

  // Switch to item 1 (question).
  doc.getElementById('action-next').dispatch('click');
  assert.equal(doc.getElementById('action-approval-body').hidden, true);
  assert.equal(doc.getElementById('action-input-body').hidden, false);
  assert.equal(doc.getElementById('action-input-note').hidden, false);
  assert.match(doc.getElementById('action-input-note').textContent, /Verified repo uses pytest in CI/);
  assert.equal(doc.getElementById('action-counter').textContent, '2 / 2');

  // Enter during IME composition must NOT submit.
  const ta = doc.getElementById('action-answer-input');
  ta.value = '使用 pytest';
  ta.dispatch('compositionstart');
  ta.dispatch('input');
  let prevented = false;
  ta.dispatch('keydown', {key: 'Enter', shiftKey: false, isComposing: true, preventDefault() { prevented = true; }});
  assert.equal(prevented, false);
  assert.equal(sent.filter(m => m.kind === 'decide').length, 0);

  // After compositionend, Enter submits the answer.
  ta.dispatch('compositionend');
  ta.dispatch('keydown', {key: 'Enter', shiftKey: false, isComposing: false, preventDefault() { prevented = true; }});
  assert.equal(prevented, true);
  const decMsg = sent.find(m => m.kind === 'decide');
  assert.ok(decMsg);
  assert.equal(decMsg.request_id, 'wir_q1');
  assert.equal(decMsg.decision, 'answer');
  assert.equal(decMsg.text, '使用 pytest');
});

test('workerActionView reports natural scrollHeight across collapse and re-expand even when viewport is smaller', () => {
  const doc = makeFakeDoc();
  const main = doc.getElementById('action-root');
  const card = doc.getElementById('action-card');
  const pill = doc.getElementById('action-collapsed');
  const sent = [];
  let onMessage = null;
  const bridge = {
    send(msg) { sent.push(msg); },
    subscribe(fn) { onMessage = fn; },
  };

  workerActionView({bridge, document: doc, frame: fn => fn()});
  onMessage({kind: 'init', generation: 'g1', theme: 'star', locale: 'zh-CN'});

  // Initial tall card (360px) inside smaller 280px initial window.
  main._rectHeight = 280;
  main.scrollHeight = 360;
  card.scrollHeight = 358;
  pill.scrollHeight = 46;
  onMessage({
    kind: 'worker_actions',
    layout: 1,
    theme: 'star',
    locale: 'zh-CN',
    items: [{request_id: 'wir_1', kind: 'approval', harness_id: 'codex', summary: 'Run cmd', allow_session_ok: true}],
  });
  let rendered = sent.filter(m => m.kind === 'rendered');
  assert.equal(rendered[rendered.length - 1].height, 360);

  // Collapse to pill (48px).
  main._rectHeight = 48;
  main.scrollHeight = 48;
  doc.getElementById('action-collapse-btn').dispatch('click');
  rendered = sent.filter(m => m.kind === 'rendered');
  assert.equal(rendered[rendered.length - 1].height, 48);

  // Re-expand from 48px window -> must report full 360px card height so native window expands back!
  main._rectHeight = 48;
  main.scrollHeight = 360;
  card.scrollHeight = 358;
  doc.getElementById('action-expand-btn').dispatch('click');
  rendered = sent.filter(m => m.kind === 'rendered');
  assert.equal(rendered[rendered.length - 1].height, 360);
  // Resizing mask hides content during height expansion until native window acks via worker_actions/resized.
  assert.equal(main.classList.contains('resizing'), true);
  onMessage({
    kind: 'worker_actions',
    layout: 2,
    theme: 'star',
    locale: 'zh-CN',
    items: [{request_id: 'wir_1', kind: 'approval', harness_id: 'codex', summary: 'Run cmd', allow_session_ok: true}],
  });
  assert.equal(main.classList.contains('resizing'), false);
});

test('workerActionView arms 400ms switch guard after queue auto-advance to prevent double-click pass-through', () => {
  const doc = makeFakeDoc();
  const sent = [];
  let onMessage = null;
  let nowMs = 1000;
  const timers = [];
  const bridge = {
    send(msg) { sent.push(msg); },
    subscribe(fn) { onMessage = fn; },
  };
  const schedule = (fn, ms) => {
    const t = {fn, due: nowMs + ms, cleared: false};
    timers.push(t);
    return t;
  };
  const cancelSchedule = t => { if (t) t.cleared = true; };

  workerActionView({bridge, document: doc, frame: fn => fn(), now: () => nowMs, schedule, cancelSchedule});
  onMessage({kind: 'init', generation: 'g1', theme: 'star', locale: 'zh-CN'});

  const qItem = {
    request_id: 'wir_q1',
    kind: 'input',
    worker_alias: 'claude-ask',
    harness_id: 'claude-code',
    workspace_root: 'F:\\proj',
    summary: 'Which option?',
    questions: [{id: 'q1', question: 'Which option?', options: [{label: 'A'}]}],
  };
  const apprItem = {
    request_id: 'wir_appr2',
    kind: 'approval',
    worker_alias: 'codex-ask',
    harness_id: 'codex',
    workspace_root: 'F:\\proj',
    tool_name: 'commandExecution',
    summary: 'Write outside file',
    allow_session_ok: true,
  };
  onMessage({kind: 'worker_actions', items: [qItem, apprItem], layout: 1, theme: 'star', locale: 'zh-CN'});

  // Submit answer on wir_q1.
  const ta = doc.getElementById('action-answer-input');
  ta.value = 'A';
  ta.dispatch('input');
  doc.getElementById('btn-send-answer').dispatch('click');
  assert.equal(sent.filter(m => m.kind === 'decide').length, 1);

  // 160ms later, host pushes updated queue with wir_appr2 occupying the same bottom-left button position.
  nowMs += 160;
  onMessage({kind: 'worker_actions', items: [apprItem], layout: 2, theme: 'star', locale: 'zh-CN'});
  assert.equal(doc.getElementById('btn-allow-once').disabled, true);

  // Second click of a fast double-click MUST NOT pass through to wir_appr2.
  doc.getElementById('btn-allow-once').dispatch('click');
  assert.equal(sent.filter(m => m.kind === 'decide').length, 1);

  // After 400ms guard expires, button re-enables and intentional click succeeds.
  nowMs += 400;
  for (const t of timers.filter(x => !x.cleared && x.due <= nowMs)) {
    t.cleared = true;
    t.fn();
  }
  assert.equal(doc.getElementById('btn-allow-once').disabled, false);
  doc.getElementById('btn-allow-once').dispatch('click');
  const decides = sent.filter(m => m.kind === 'decide');
  assert.equal(decides.length, 2);
  assert.equal(decides[1].request_id, 'wir_appr2');
  assert.equal(decides[1].decision, 'allow_once');
});

test('workerActionView masks is_secret questions with password input and bypasses drafts', () => {
  const doc = makeFakeDoc();
  const sent = [];
  let onMessage = null;
  const bridge = {
    send(msg) { sent.push(msg); },
    subscribe(fn) { onMessage = fn; },
  };

  workerActionView({bridge, document: doc, frame: fn => fn()});
  onMessage({kind: 'init', generation: 'g1', theme: 'star', locale: 'zh-CN'});

  const secretItem = {
    request_id: 'wir_sec_1',
    kind: 'input',
    worker_alias: 'codex-sec',
    harness_id: 'codex',
    workspace_root: 'F:\\proj',
    summary: 'Enter API secret',
    questions: [{id: 'api_key', question: 'Enter API secret', is_secret: true}],
  };
  const normalItem = {
    request_id: 'wir_norm_2',
    kind: 'input',
    worker_alias: 'codex-sec',
    harness_id: 'codex',
    workspace_root: 'F:\\proj',
    summary: 'Pick port',
    questions: [{id: 'port', question: 'Pick port'}],
  };

  onMessage({kind: 'worker_actions', items: [secretItem, normalItem], layout: 1, theme: 'star', locale: 'zh-CN'});

  const ta = doc.getElementById('action-answer-input');
  assert.equal(ta.hidden, true, 'plaintext textarea must be hidden for secret question');
  const qWrap = doc.getElementById('action-questions');
  const secInput = qWrap.children[0].children.find(c => c.tagName === 'INPUT');
  assert.ok(secInput, 'expected password input element for is_secret question');
  assert.equal(secInput.type, 'password');
  assert.equal(secInput.autocomplete, 'new-password');

  // Switch to next card and back -> secret value must be wiped and not saved in drafts.
  secInput.value = 'temp-secret-should-clear';
  doc.getElementById('action-next').dispatch('click');
  assert.equal(secInput.value, '', 'secret value must be cleared when paging away');
  assert.equal(ta.hidden, false, 'normal card restores plaintext textarea');

  doc.getElementById('action-prev').dispatch('click');
  const secInput2 = doc.getElementById('action-questions').children[0].children.find(c => c.tagName === 'INPUT');
  assert.equal(secInput2.value, '', 'secret must not be restored from drafts');

  // Submit secret answer -> delivered in answers map and input wiped.
  secInput2.value = 'real-secret-42';
  doc.getElementById('btn-send-answer').dispatch('click');
  const decides = sent.filter(m => m.kind === 'decide');
  assert.equal(decides.length, 1);
  assert.equal(decides[0].request_id, 'wir_sec_1');
  assert.equal(decides[0].decision, 'answer');
  assert.deepEqual(decides[0].answers, {api_key: ['real-secret-42']});
  assert.equal(decides[0].text, '');
  assert.equal(secInput2.value, '', 'secret input must be cleared on submit');
});

test('workerActionView isolates is_secret option selections from ordinary drafts across card switches', () => {
  const doc = makeFakeDoc();
  const sent = [];
  let onMessage = null;
  const bridge = {
    send(msg) { sent.push(msg); },
    subscribe(fn) { onMessage = fn; },
  };

  workerActionView({bridge, document: doc, frame: fn => fn()});
  onMessage({kind: 'init', generation: 'g1', theme: 'star', locale: 'zh-CN'});

  const secretOptItem = {
    request_id: 'wir_sec_opt',
    kind: 'input',
    worker_alias: 'codex-sec',
    harness_id: 'codex',
    workspace_root: 'F:\\proj',
    summary: 'Pick secret key',
    questions: [{
      id: 'sec_choice',
      question: 'Pick secret key',
      is_secret: true,
      options: [{label: 'Key1'}, {label: 'Key2'}],
    }],
  };
  const normalItem = {
    request_id: 'wir_norm_3',
    kind: 'input',
    worker_alias: 'codex-sec',
    harness_id: 'codex',
    workspace_root: 'F:\\proj',
    summary: 'Pick mode',
    questions: [{id: 'mode', question: 'Pick mode'}],
  };

  onMessage({kind: 'worker_actions', items: [secretOptItem, normalItem], layout: 1, theme: 'star', locale: 'zh-CN'});

  const findFirstChip = () => {
    const qBox = doc.getElementById('action-questions').children[0];
    const optWrap = qBox.children.find(c => c.className === 'action-q-opts');
    return optWrap.children[0];
  };

  const chip1 = findFirstChip();
  chip1.dispatch('click');
  assert.equal(chip1.getAttribute('aria-pressed'), 'true');

  // Switch away and back -> secret option selection MUST be cleared (not saved in drafts).
  doc.getElementById('action-next').dispatch('click');
  doc.getElementById('action-prev').dispatch('click');
  const chip2 = findFirstChip();
  assert.equal(chip2.getAttribute('aria-pressed'), 'false', 'secret option selection must not persist across card switch');

  // Re-select and submit -> delivered in answers map.
  chip2.dispatch('click');
  doc.getElementById('btn-send-answer').dispatch('click');
  const decides = sent.filter(m => m.kind === 'decide');
  assert.equal(decides.length, 1);
  assert.deepEqual(decides[0].answers, {sec_choice: ['Key1']});
});
