import {conversationClient} from './chat-client.mjs';
import {ChatWindow, unsettled, newerTurn} from './chat-model.mjs';
import {messageRenderer} from './chat-renderer.mjs';
import {ChatOutbox} from './chat-outbox.mjs';
import {replyStatusView} from './chat-status.mjs';
import {operationQueue} from './chat-queue.mjs';
import {connectionView} from './chat-connection.mjs';

export function chatPanel(bridge, editor) {
  const query = id => document.getElementById(id);
  const panel = query('chat-panel'), scroll = query('chat-scroll'), status = query('chat-status');
  if (!panel) return {dispose() {}};
  const windowState = new ChatWindow();
  const outbox = new ChatOutbox();
  let sendingOperation = '';
  let connecting = null;
  const list = query('chat-conversations');
  const sendButton = query('chat-send'), stopButton = query('chat-stop'), olderButton = query('chat-older');
  let selected = '', turn = null, pending = null, expanded = false, visible = false, ready = false;
  let sending = false, polling = false, loading = false, timer = null, selection = 0, nodeBusy = false;
  let stoppingOperation = '';
  let createOperation = '', listOffset = 0;
  let creatingRequest = null;
  let draftDecision = null, historyLoaded = false, historyLoading = -1, readingOlder = false, refreshHistory = false;
  const cleanup = [];
  const listen = (node, name, fn) => { node.addEventListener(name, fn); cleanup.push(() => node.removeEventListener(name, fn)); };
  const statusView = replyStatusView(status);
  const say = message => statusView.message(message);
  const sayTurn = (suffix = '') => {
    if (queue.enabled) {
      if (queue.state.active?.conversation_id === selected) {
        if (stoppingOperation === queue.state.active.operation_id) return say('正在停止回复…');
        return statusView.turn(queue.state.active, suffix);
      }
      if (queue.awaitingReply(selected)) return statusView.turn(null, suffix, true);
      const failed = queue.failure(selected);
      if (failed?.turn) return statusView.turn(newerTurn(turn, failed.turn), suffix);
      if (failed && !turn) return say(failed.error?.message ?? '会话准备失败');
    }
    statusView.turn(turn, suffix, sending && turn?.operation_id !== sendingOperation);
  };
  const updateButtons = () => {
    sendButton.disabled = !ready || sending || (!queue.enabled && (nodeBusy || unsettled(turn) || Boolean(outbox.pending(selected)))) || Boolean(draftDecision);
    const active = queue.enabled ? queue.state.active : turn;
    stopButton.hidden = active?.conversation_id !== selected || active?.delivery !== 'accepted' || !unsettled(active);
    stopButton.disabled = Boolean(stoppingOperation);
    olderButton.hidden = !windowState.hasMore;
  };

  function envelope(value) {
    if (value.clear_sequence !== undefined) editor.clearIfRevision(value.clear_sequence);
    const response = value.response ?? {};
    queue.observe(response.queue);
    const stale = turn && (response.turn?.conversation_id === selected || response.conversation?.id === selected) &&
      newerTurn(turn, response.turn) !== response.turn;
    if (!stale) {
      pending = value.pending ?? null;
      nodeBusy = response.busy === true;
    }
    outbox.observe(response.turn);
    if (response.turn?.delivery === 'rejected') editor.restoreSubmission(response.turn.operation_id);
    if (response.target?.node_id) ready = true;
    connection.visible(visible && expanded && ready);
    updateButtons();
    paint();
  }
  const client = conversationClient(bridge, editor, envelope);
  const queue = operationQueue(client, outbox);
  const connection = connectionView(query('chat-connection'), client);
  const render = messageRenderer(query('chat-messages'), {copy, expand: expandMessage});

  const atBottom = () => scroll.scrollHeight - scroll.scrollTop - scroll.clientHeight < 48;
  const followsLatest = () => !readingOlder && atBottom();

  function paint(older = false) {
    if (!visible || !expanded) return;
    const top = scroll.scrollTop, follow = !older && followsLatest();
    const edge = scroll.getBoundingClientRect().top;
    const nodes = [...query('chat-messages').children];
    const anchor = nodes.find(node => node.getBoundingClientRect().bottom > edge);
    const offset = anchor?.getBoundingClientRect().top - edge;
    outbox.reconcile(windowState.messages, selected);
    windowState.reserve(outbox.reservedBytes);
    render(outbox.merge(windowState.messages, selected));
    const retained = anchor && [...query('chat-messages').children].find(node => node.dataset.messageId === anchor.dataset.messageId);
    if (follow) scroll.scrollTop = scroll.scrollHeight;
    else if (retained) scroll.scrollTop = top + retained.getBoundingClientRect().top - edge - offset;
    else scroll.scrollTop = top;
    updateButtons();
  }

  function setExpanded(value) {
    expanded = value;
    panel.hidden = !value;
    query('chat-expand').textContent = value ? '收起 ▾' : '展开 ▴';
    query('chat-expand').setAttribute('aria-expanded', String(value));
    editor.setExpanded(value);
    connection.visible(visible && expanded && ready);
    if (value) { paint(); if (selected && !historyLoaded) void safely(() => history()); }
    schedule();
  }

  function connect() {
    if (!connecting) connecting = connectOnce().finally(() => { connecting = null; });
    return connecting;
  }

  async function connectOnce() {
    loading = true;
    if (sending) sayTurn(); else say('正在连接本机助手…');
    try {
      const hello = await client.call('hello');
      ready = true;
      const conversations = await client.call('list');
      if (queue.enabled) {
        const intent = hello.envelope.pending;
        if (intent) {
          try { outbox.queued((await client.call('queue_item', intent)).queue_item); }
          catch (error) { if (error.code !== 'not_found') throw error; }
        }
        await queue.refresh();
      }
      const restored = hello.envelope.pending?.conversation_id || hello.envelope.selected || conversations.conversations?.[0]?.id;
      if (restored && restored !== selected) await select(restored);
      else if (selected && expanded) await history();
      else if (sending) sayTurn();
      else say(hello.can_send ? '输入内容，开始新对话' : '请先在本机设置配置模型；已有历史可继续查看');
    } catch (error) { if (!ready) connection.unavailable(); say(error.message); }
    finally { loading = false; updateButtons(); schedule(); }
  }

  async function select(id) {
    selection++;
    selected = id;
    if (sendingOperation) outbox.bind(sendingOperation, id);
    turn = null;
    historyLoaded = false;
    refreshHistory = false;
    readingOlder = false;
    windowState.clear();
    list.hidden = true;
    query('chat-conversation-name').textContent = '对话 ' + id.slice(0, 8);
    paint();
    if (queue.enabled) sayTurn();
    if (queue.enabled) void safely(() => client.call('select', {conversation_id:id}));
    if (expanded && !queue.creating(id)) {
      if (queue.enabled) void safely(() => history()); else await history();
    }
    schedule();
  }

  async function history(older = false) {
    if (!selected) return;
    const version = selection, id = selected;
    if (historyLoading !== -1) {
      if (historyLoading !== version) refreshHistory = true;
      return;
    }
    historyLoading = version;
    try {
      const response = await client.call('history', {conversation_id: id,
        ...(older ? {offset: windowState.next, cursor: windowState.cursor} : {})});
      if (version !== selection || id !== selected) return;
      const incoming = response.turn ?? null;
      const latest = newerTurn(turn, incoming);
      const stale = latest !== incoming;
      turn = latest;
      // A newer completion may have arrived while this history read was pending.
      // Preserve its visible page and fetch fresh content once this read releases.
      if (stale && !older) {
        refreshHistory = followsLatest();
        sayTurn(); paint();
        return;
      }
      historyLoaded = response.conversation?.state !== 'creating';
      refreshHistory = false;
      readingOlder = older && !response.page?.reset;
      windowState.apply(response.page ?? {messages: []}, older);
      if (response.page?.reset && !sending) say('历史已变化，已重新读取最新消息');
      else sayTurn();
      paint(readingOlder);
      updateButtons();
    } finally {
      if (historyLoading === version) historyLoading = -1;
      if (refreshHistory) schedule();
    }
  }

  async function create() {
    if (creatingRequest) return creatingRequest;
    creatingRequest = createOnce().finally(() => { creatingRequest = null; });
    return creatingRequest;
  }

  async function createOnce() {
    createOperation ||= crypto.randomUUID();
    const response = await client.call(queue.enabled ? 'prepare' : 'create', {operation_id: createOperation});
    if (!queue.enabled && response.conversation?.state !== 'ready') throw new Error('会话正在创建，请稍后刷新核对');
    createOperation = '';
    await select(response.conversation.id);
  }

  async function submit() {
    if (editor.commandSubmit?.()) return;
    const snapshot = editor.snapshot();
    if (!snapshot || !snapshot.text.trim() || sending || (!queue.enabled && (nodeBusy || unsettled(turn) || outbox.pending(selected))) || draftDecision) return;
    const operation = crypto.randomUUID();
    if (!editor.stageSubmission(operation, snapshot)) return;
    sendingOperation = operation;
    sending = true;
    let dispatched = false;
    if (!outbox.begin(operation, selected, snapshot.text)) {
      editor.restoreSubmission(operation);
      sendingOperation = ''; sending = false;
      say('请先处理已有待发消息，当前草稿已保留');
      updateButtons(); return;
    }
    readingOlder = false;
    updateButtons();
    setExpanded(true);
    paint();
    scroll.scrollTop = scroll.scrollHeight;
    sayTurn();
    try {
      if (!ready) await connect();
      if (creatingRequest) await creatingRequest;
      if (!selected) await create();
      const id = selected, version = selection;
      outbox.bind(operation, id);
      pending = {operation_id: operation, conversation_id: id};
      dispatched = true;
      outbox.dispatched(operation);
      const response = await client.call(queue.enabled ? 'enqueue' : 'submit', {conversation_id: id, operation_id: operation,
        text: snapshot.text, draft_revision: snapshot.revision});
      outbox.observe(response.turn);
      if (selected === id && selection === version) {
        turn = newerTurn(turn, response.turn);
        sayTurn();
        paint();
        if (!queue.enabled) await history();
      }
    } catch (error) {
      outbox.observe(error.response?.turn);
      if (outbox.fail(operation, dispatched, error) === 'rejected') editor.restoreSubmission(operation);
      if (error.response?.turn?.conversation_id === selected) turn = newerTurn(turn, error.response.turn);
      say(error.message);
      paint();
    } finally { sendingOperation = ''; sending = false; updateButtons(); schedule(); }
  }

  function schedule() {
    clearTimeout(timer);
    timer = null;
    if (visible && selected && (queue.waiting || expanded && (refreshHistory || nodeBusy) || unsettled(turn) || outbox.pending(selected) || pending?.conversation_id === selected)) {
      timer = setTimeout(poll, 500);
    }
  }

  async function poll() {
    if (polling || !visible) return;
    polling = true;
    const id = selected, version = selection;
    try {
      if (queue.enabled) {
        const creating = queue.creating(id);
        const before = queue.state.active?.operation_id;
        await queue.refresh();
        if (id !== selected || version !== selection) return;
        if (queue.consumeHistoryChange(id)) historyLoaded = false;
        if (creating && !queue.creating(id) || !historyLoaded && !queue.creating(id) || before && before !== queue.state.active?.operation_id) {
          historyLoaded = false;
          if (expanded && followsLatest()) void safely(() => history());
        }
        if (!queue.state.active || queue.state.active.conversation_id !== id) {
          if (!queue.waiting && unsettled(turn)) {
            if (expanded && followsLatest()) void safely(() => history());
            else { historyLoaded = false; turn = null; }
          }
          if (!queue.waiting) pending = null;
          sayTurn(); paint(); return;
        }
        turn = queue.state.active;
      }
      const identity = outbox.pending(id) ?? (pending?.conversation_id === id ? pending : null);
      const response = await client.call('reconcile', {conversation_id: id,
        ...(queue.enabled ? {operation_id:queue.state.active.operation_id} : identity ? {operation_id: identity.operation_id} : {})});
      if (id !== selected || version !== selection) return;
      const previous = turn;
      turn = newerTurn(turn, response.turn);
      sayTurn();
      const follow = expanded && followsLatest();
      if (response.live && follow) { windowState.live(response.live); paint(); }
      if (refreshHistory || turn && (turn.assistant_message_id !== previous?.assistant_message_id || !unsettled(turn) && unsettled(previous))) {
        if (!expanded) { historyLoaded = false; refreshHistory = true; }
        else if (follow) await history();
        else sayTurn('点击“回到最新”查看回复');
      }
      if (turn && !unsettled(turn)) pending = null;
    } catch (error) { say(error.message); }
    finally { polling = false; updateButtons(); schedule(); }
  }

  async function showList(more = false) {
    setExpanded(true);
    if (!more) listOffset = 0;
    const response = await client.call('list', {offset: listOffset});
    const entries = response.conversations ?? [];
    list.replaceChildren();
    for (const entry of entries) {
      const button = document.createElement('button');
      button.type = 'button';
      button.textContent = new Date(entry.updated_at).toLocaleString() + ' · ' + entry.id.slice(0, 8) +
        (entry.id === response.active_conversation ? queue.state?.active?.delivery === 'unknown' ? ' · 送达待确认' : ' · 正在回复' : '');
      button.addEventListener('click', () => { if (entry.id !== selected) requestSwitch(() => select(entry.id)); });
      list.append(button);
    }
    listOffset += entries.length;
    if (entries.length === 50) {
      const moreButton = document.createElement('button');
      moreButton.id = 'chat-list-more'; moreButton.type = 'button'; moreButton.textContent = '更多会话';
      moreButton.addEventListener('click', () => safely(() => showList(true)));
      list.append(moreButton);
    }
    if (!list.children.length) list.textContent = '暂无历史会话';
    list.hidden = false;
  }

  async function expandMessage(messageID, restart = false) {
    const id = selected, version = selection;
    const budget = windowState.makeRoom(messageID, 1024 * 1024);
    const encoder = new TextEncoder();
    const start = restart ? 0 : windowState.messages.find(message => message.id === messageID)?.fragmentNext ?? 0;
    let text = '', offset = start, more = true, bytes = 0;
    say('正在读取完整消息…');
    try {
      while (more && version === selection) {
        const response = await client.call('message', {conversation_id: id, message_id: messageID, offset});
        if (version !== selection) return;
        const fragment = response.fragment;
        const size = encoder.encode(fragment.text).length;
        if (bytes + size > budget) break;
        text += fragment.text;
        bytes += size;
        more = fragment.has_more;
        if (more && fragment.next_offset <= offset) throw new Error('消息分片未前进，请刷新');
        offset = fragment.next_offset;
      }
      windowState.replace(messageID, text, more, start, more ? offset : 0);
      paint();
      if (more) say('已达到当前窗口文本上限，可继续读取后续片段；原始消息保留在运行时');
      else sayTurn();
    } catch (error) { say(error.message); }
  }

  function copy(text) {
    const payload = {text};
    if (new TextEncoder().encode(text).length > 64 * 1024 || new TextEncoder().encode(JSON.stringify(payload)).length > 120 * 1024) {
      say('内容超过按钮复制上限，请选中文字后按 Ctrl+C');
      return;
    }
    editor.post('copy', payload);
  }

  async function safely(action) { try { await action(); } catch (error) { say(error.message); } }
  function requestSwitch(action) {
    if (sending) { say('消息正在发送，请稍后切换会话'); return; }
    const snapshot = editor.snapshot();
    if (!snapshot) { say('请先完成当前输入法组合，再切换会话'); return; }
    if (!snapshot.text) { void safely(action); return; }
    draftDecision = {action, revision: snapshot.revision};
    query('chat-draft-decision').hidden = false;
    query('chat-draft-cancel').focus();
    updateButtons();
  }
  function dismissDecision() {
    draftDecision = null;
    query('chat-draft-decision').hidden = true;
    updateButtons();
  }
  listen(query('chat-draft-cancel'), 'click', dismissDecision);
  listen(query('chat-draft-discard'), 'click', () => {
    const decision = draftDecision, snapshot = editor.snapshot();
    dismissDecision();
    if (!decision) return;
    if (!snapshot || snapshot.revision !== decision.revision || !editor.clearIfRevision(decision.revision)) {
      say('草稿已变化，请重新选择要切换的会话'); return;
    }
    void safely(decision.action);
  });
  listen(sendButton, 'click', submit);
  listen(query('chat-expand'), 'click', () => setExpanded(!expanded));
  listen(query('chat-history'), 'click', () => safely(() => ready ? showList() : connect().then(() => showList())));
  listen(query('chat-new'), 'click', () => requestSwitch(async () => { setExpanded(true); if (!ready) await connect(); await create(); }));
  listen(query('chat-refresh'), 'click', () => { connection.refresh(); return safely(() => ready && selected ? history().then(schedule) : connect()); });
  listen(query('chat-latest'), 'click', () => safely(async () => { await history(); scroll.scrollTop = scroll.scrollHeight; schedule(); }));
  listen(olderButton, 'click', () => safely(() => history(true)));
  listen(stopButton, 'click', () => safely(async () => {
    const active = queue.enabled ? queue.state.active : turn;
    if (stoppingOperation || active?.conversation_id !== selected || active?.delivery !== 'accepted' || !unsettled(active)) return;
    const id = selected, version = selection;
    stoppingOperation = active.operation_id;
    say('正在停止回复…'); updateButtons();
    try {
      const response = await client.call('cancel', {conversation_id: id, operation_id: active.operation_id});
      if (id === selected && version === selection) turn = newerTurn(turn, response.turn);
    } finally {
      stoppingOperation = ''; sayTurn(); updateButtons(); schedule();
    }
  }));
  editor.onSubmit(submit);
  const unsubscribe = bridge.subscribe(message => {
    if (message.kind === 'focus' || message.kind === 'visibility' && message.visible) {
      visible = true;
      statusView.visible(true);
      connection.visible(expanded && ready);
      if (!ready) void connect(); else { paint(); schedule(); }
    } else if (message.kind === 'visibility' && !message.visible) {
      visible = false;
      statusView.visible(false);
      connection.visible(false);
      clearTimeout(timer);
      timer = null;
    }
  });
  updateButtons();
  function dispose() { clearTimeout(timer); connection.dispose(); statusView.dispose(); render.dispose(); unsubscribe(); client.dispose(); cleanup.forEach(remove => remove()); }
  listen(window, 'pagehide', dispose);
  return {dispose};
}
