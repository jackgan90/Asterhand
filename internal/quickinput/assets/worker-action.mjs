import {webview2Bridge} from './webview2-bridge.mjs';
import {applyI18n, setLocale, t} from './i18n.mjs';

function projectBase(root) {
  if (!root) return '';
  const parts = String(root).replace(/[/\\]+$/, '').split(/[/\\]/);
  return parts[parts.length - 1] || root;
}

const SWITCH_GUARD_MS = 400;
const RESIZE_FALLBACK_MS = 120;

export function workerActionView({
  bridge,
  document,
  frame = requestAnimationFrame,
  now = () => Date.now(),
  schedule = setTimeout,
  cancelSchedule = clearTimeout,
}) {
  const byID = id => document.getElementById(id);
  const root = document.documentElement;
  const main = byID('action-root');
  const collapsedBox = byID('action-collapsed');
  const cardBox = byID('action-card');
  const answerInput = byID('action-answer-input');

  let epoch = '';
  let items = [];
  let activeRequestID = '';
  let collapsed = false;
  let cmdExpanded = false;
  let composing = false;
  let submittingID = '';
  let renderSeq = 0;
  let lastLayout = 0;
  let lastReportedHeight = 0;
  let guardUntil = 0;
  let guardTimer = null;
  let resizing = false;
  let resizeTimer = null;
  const drafts = new Map(); // request_id -> { text, selected: { [qID]: Set<string> } }

  const post = (kind, fields = {}) => bridge.send({version: 1, kind, generation: epoch, ...fields});

  function isActionGuarded() {
    return now() < guardUntil;
  }

  function clearSwitchGuard() {
    if (guardTimer !== null) {
      cancelSchedule(guardTimer);
      guardTimer = null;
    }
    guardUntil = 0;
  }

  function armSwitchGuard() {
    clearSwitchGuard();
    guardUntil = now() + SWITCH_GUARD_MS;
    guardTimer = schedule(() => {
      guardTimer = null;
      if (items.length) paint();
    }, SWITCH_GUARD_MS);
  }

  function finishResizeMask() {
    if (resizeTimer !== null) {
      cancelSchedule(resizeTimer);
      resizeTimer = null;
    }
    if (!resizing) return;
    resizing = false;
    main.classList.toggle('resizing', false);
  }

  function beginResizeMask() {
    resizing = true;
    main.classList.toggle('resizing', true);
    if (resizeTimer !== null) cancelSchedule(resizeTimer);
    resizeTimer = schedule(() => {
      resizeTimer = null;
      finishResizeMask();
    }, RESIZE_FALLBACK_MS);
  }

  function currentItem() {
    if (!items.length) return null;
    const found = items.find(it => it.request_id === activeRequestID);
    if (found) return found;
    activeRequestID = items[0].request_id;
    return items[0];
  }

  function currentIndex() {
    const cur = currentItem();
    if (!cur) return -1;
    return items.findIndex(it => it.request_id === cur.request_id);
  }

  function getDraft(reqID) {
    if (!drafts.has(reqID)) {
      drafts.set(reqID, {text: '', selected: {}});
    }
    return drafts.get(reqID);
  }

  function reportHeight() {
    const seq = ++renderSeq;
    frame(() => frame(() => {
      if (seq !== renderSeq || !items.length) return;
      const activeBox = collapsed ? collapsedBox : cardBox;
      const boxH = activeBox ? Math.ceil(activeBox.scrollHeight || activeBox.getBoundingClientRect().height || 0) : 0;
      const mainH = Math.ceil(Math.max(main.scrollHeight || 0, main.getBoundingClientRect().height || 0));
      const h = Math.max(mainH, boxH > 0 ? boxH + 2 : 0);
      if (h > 0) {
        const unchanged = h === lastReportedHeight;
        lastReportedHeight = h;
        post('rendered', {layout: lastLayout, height: h});
        if (unchanged && resizing) frame(() => finishResizeMask());
      }
    }));
  }

  function paint() {
    if (!items.length) return;
    const item = currentItem();
    const idx = currentIndex();
    const approvalCount = items.filter(it => it.kind === 'approval').length;
    const inputCount = items.length - approvalCount;

    collapsedBox.hidden = !collapsed;
    cardBox.hidden = collapsed;
    if (collapsed) {
      byID('action-collapsed-text').textContent = t('common.worker_action.collapsed_summary', {count: items.length});
      reportHeight();
      return;
    }

    const badge = byID('action-kind-badge');
    badge.dataset.kind = item.kind;
    badge.textContent = t(item.kind === 'approval' ? 'common.worker_action.badge_approval' : 'common.worker_action.badge_input');

    const alias = item.worker_alias || item.worker_id || '';
    byID('action-source').textContent = alias ? `${alias} · ${item.harness_id}` : item.harness_id;

    const pager = byID('action-pager');
    pager.hidden = items.length <= 1;
    byID('action-counter').textContent = `${idx + 1} / ${items.length}`;

    const qSum = byID('action-queue-summary');
    if (items.length > 1) {
      qSum.hidden = false;
      qSum.textContent = t('common.worker_action.queue_breakdown', {approvals: approvalCount, inputs: inputCount});
    } else {
      qSum.hidden = true;
    }

    const isApproval = item.kind === 'approval';
    byID('action-approval-body').hidden = !isApproval;
    byID('action-input-body').hidden = isApproval;
    const busy = submittingID === item.request_id;
    const actionDisabled = busy || isActionGuarded();
    const proj = projectBase(item.workspace_root);

    if (isApproval) {
      const toolPill = byID('action-tool-pill');
      toolPill.textContent = item.tool_name || 'action';
      const riskPill = byID('action-risk-pill');
      const risk = item.hard_redline ? 'critical' : (item.risk_level || (item.outside_workspace ? 'high' : 'medium'));
      riskPill.dataset.risk = risk;
      riskPill.textContent = t(
        item.hard_redline ? 'common.worker_action.risk_critical'
          : item.outside_workspace ? 'common.worker_action.risk_outside'
            : 'common.worker_action.risk_workspace'
      );
      byID('action-project').textContent = proj;
      byID('action-summary').textContent = item.summary || '';

      const cmdWrap = byID('action-cmd-wrap');
      const cmdPre = byID('action-cmd');
      const cmdToggle = byID('action-cmd-toggle');
      if (item.command) {
        cmdWrap.hidden = false;
        cmdPre.textContent = item.command;
        cmdPre.classList.toggle('expanded', cmdExpanded);
        const longCmd = item.command.length > 140 || item.command.includes('\n');
        cmdToggle.hidden = !longCmd;
        cmdToggle.textContent = t(cmdExpanded ? 'common.worker_action.cmd_collapse' : 'common.worker_action.cmd_expand');
      } else {
        cmdWrap.hidden = true;
      }

      const pathsEl = byID('action-paths');
      if (item.paths && item.paths.length) {
        pathsEl.hidden = false;
        pathsEl.textContent = item.paths.join(', ');
      } else {
        pathsEl.hidden = true;
      }

      const reasonEl = byID('action-reason');
      if (item.reason) {
        reasonEl.hidden = false;
        reasonEl.textContent = item.reason;
      } else {
        reasonEl.hidden = true;
      }

      byID('btn-allow-once').disabled = actionDisabled;
      const allowSessBtn = byID('btn-allow-session');
      allowSessBtn.hidden = !item.allow_session_ok || Boolean(item.hard_redline);
      allowSessBtn.disabled = actionDisabled;
      byID('btn-deny').disabled = actionDisabled;
      byID('btn-cancel-approval').disabled = actionDisabled;
    } else {
      byID('action-input-project').textContent = proj;
      const noteEl = byID('action-input-note');
      if (noteEl) {
        if (item.escalation_note) {
          noteEl.hidden = false;
          noteEl.textContent = t('common.worker_action.escalation_note', {note: item.escalation_note});
        } else {
          noteEl.hidden = true;
        }
      }
      const draft = getDraft(item.request_id);
      const qWrap = byID('action-questions');
      qWrap.replaceChildren();
      const qs = item.questions && item.questions.length ? item.questions : [{id: 'q1', question: item.summary}];
      for (const q of qs) {
        const qBox = document.createElement('div');
        qBox.className = 'action-q-item';
        const qText = document.createElement('div');
        qText.className = 'action-q-text';
        qText.textContent = q.question || item.summary || '';
        qBox.appendChild(qText);
        if (q.options && q.options.length) {
          const optWrap = document.createElement('div');
          optWrap.className = 'action-q-opts';
          if (!draft.selected[q.id]) draft.selected[q.id] = new Set();
          for (const opt of q.options) {
            const chip = document.createElement('button');
            chip.type = 'button';
            chip.className = 'action-opt-chip';
            chip.textContent = opt.description ? `${opt.label} — ${opt.description}` : opt.label;
            const active = draft.selected[q.id].has(opt.label);
            chip.setAttribute('aria-pressed', active ? 'true' : 'false');
            chip.disabled = busy;
            chip.addEventListener('click', () => {
              const sel = draft.selected[q.id];
              if (q.multi_select) {
                if (sel.has(opt.label)) sel.delete(opt.label);
                else sel.add(opt.label);
              } else {
                const had = sel.has(opt.label);
                sel.clear();
                if (!had) sel.add(opt.label);
              }
              paint();
            });
            optWrap.appendChild(chip);
          }
          qBox.appendChild(optWrap);
        }
        qWrap.appendChild(qBox);
      }
      if (answerInput.value !== draft.text) {
        answerInput.value = draft.text;
      }
      answerInput.disabled = busy;
      byID('btn-send-answer').disabled = actionDisabled;
      byID('btn-cancel-input').disabled = actionDisabled;
    }

    reportHeight();
  }

  function submitApproval(decision) {
    const item = currentItem();
    if (!item || submittingID === item.request_id || isActionGuarded()) return;
    submittingID = item.request_id;
    paint();
    post('decide', {request_id: item.request_id, decision});
  }

  function submitAnswer() {
    const item = currentItem();
    if (!item || submittingID === item.request_id || isActionGuarded()) return;
    const draft = getDraft(item.request_id);
    const text = (draft.text || '').trim();
    const answers = {};
    let hasSelected = false;
    for (const [qID, set] of Object.entries(draft.selected)) {
      if (set && set.size > 0) {
        answers[qID] = Array.from(set);
        hasSelected = true;
      }
    }
    if (!text && !hasSelected) {
      answerInput.focus();
      return;
    }
    submittingID = item.request_id;
    paint();
    post('decide', {request_id: item.request_id, decision: 'answer', text, answers});
  }

  byID('action-collapse-btn').addEventListener('click', () => {
    collapsed = true;
    beginResizeMask();
    paint();
  });
  byID('action-expand-btn').addEventListener('click', () => {
    collapsed = false;
    beginResizeMask();
    paint();
  });
  byID('action-prev').addEventListener('click', () => {
    if (items.length <= 1) return;
    const idx = (currentIndex() - 1 + items.length) % items.length;
    activeRequestID = items[idx].request_id;
    cmdExpanded = false;
    beginResizeMask();
    paint();
  });
  byID('action-next').addEventListener('click', () => {
    if (items.length <= 1) return;
    const idx = (currentIndex() + 1) % items.length;
    activeRequestID = items[idx].request_id;
    cmdExpanded = false;
    beginResizeMask();
    paint();
  });
  byID('action-cmd-toggle').addEventListener('click', () => {
    cmdExpanded = !cmdExpanded;
    beginResizeMask();
    paint();
  });

  byID('btn-allow-once').addEventListener('click', () => submitApproval('allow_once'));
  byID('btn-allow-session').addEventListener('click', () => submitApproval('allow_session'));
  byID('btn-deny').addEventListener('click', () => submitApproval('deny'));
  byID('btn-cancel-approval').addEventListener('click', () => submitApproval('cancel_turn'));
  byID('btn-send-answer').addEventListener('click', submitAnswer);
  byID('btn-cancel-input').addEventListener('click', () => submitApproval('cancel_turn'));

  answerInput.addEventListener('compositionstart', () => { composing = true; });
  answerInput.addEventListener('compositionend', () => {
    composing = false;
    const item = currentItem();
    if (item) getDraft(item.request_id).text = answerInput.value;
  });
  answerInput.addEventListener('input', () => {
    const item = currentItem();
    if (item) getDraft(item.request_id).text = answerInput.value;
  });
  answerInput.addEventListener('keydown', event => {
    if (event.key === 'Enter' && !event.shiftKey && !composing && !event.isComposing) {
      event.preventDefault();
      submitAnswer();
    }
  });

  bridge.subscribe(message => {
    if (message.kind === 'init') {
      epoch = message.generation;
      root.dataset.theme = message.theme;
      setLocale(message.locale);
      applyI18n(document);
      post('initialized');
    } else if (message.kind === 'worker_actions') {
      const prevActiveID = activeRequestID;
      const wasResizing = resizing;
      lastLayout = message.layout || 0;
      items = Array.isArray(message.items) ? message.items : [];
      const liveIDs = new Set(items.map(it => it.request_id));
      for (const k of drafts.keys()) {
        if (!liveIDs.has(k)) drafts.delete(k);
      }
      if (!liveIDs.has(submittingID)) submittingID = '';
      const autoSwitched = Boolean(prevActiveID && !liveIDs.has(prevActiveID));
      if (!liveIDs.has(activeRequestID)) {
        activeRequestID = items.length ? items[0].request_id : '';
        cmdExpanded = false;
      }
      if (!items.length) {
        collapsed = false;
        lastReportedHeight = 0;
        clearSwitchGuard();
        finishResizeMask();
      } else if (autoSwitched && activeRequestID && activeRequestID !== prevActiveID) {
        armSwitchGuard();
        if (!wasResizing) beginResizeMask();
      }
      root.dataset.theme = message.theme;
      setLocale(message.locale);
      applyI18n(document);
      paint();
      if (wasResizing && items.length) {
        frame(() => finishResizeMask());
      }
    } else if (message.kind === 'resized') {
      if (resizing) frame(() => finishResizeMask());
    } else if (message.kind === 'theme') {
      root.dataset.theme = message.theme;
    } else if (message.kind === 'locale') {
      setLocale(message.locale);
      applyI18n(document);
      paint();
    }
  });

  post('ready');
}

if (globalThis.window?.chrome?.webview) workerActionView({bridge: webview2Bridge(), document});
