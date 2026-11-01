import {messageText} from './chat-model.mjs';
import {markupTokens} from './chat-markup.mjs';
import {mathRenderer, MAX_WINDOW_MATH_NODES} from './chat-math.mjs';
import {formatDateTime, t} from './i18n.mjs';

const bodyRoleKeys = {
  'code-copy': 'quickinput.render.copy_code',
  'code-lang-default': 'quickinput.render.code_default',
  'copy-text': 'quickinput.render.copy_text',
  'copy-source': 'quickinput.render.copy_source',
};

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}

function authorRoleLabel(role, transient) {
  if (role === 'user') return t('quickinput.render.role_user');
  return transient ? t('quickinput.render.role_assistant_streaming') : t('quickinput.render.role_assistant');
}

function relocalizeBodyControls(root) {
  if (!root?.children) return;
  for (const child of root.children) {
    const role = child.getAttribute?.('data-i18n-role');
    if (role && bodyRoleKeys[role]) child.textContent = t(bodyRoleKeys[role]);
    relocalizeBodyControls(child);
  }
}

// Message HTML is never interpreted. Only the local math engine may create
// mathematical DOM/style nodes; its input and output have independent budgets.
function inline(parent, text) {
  const pattern = /(\*\*[^*\n]+\*\*|\*[^*\n]+\*)/g;
  let start = 0;
  for (const match of text.matchAll(pattern)) {
    parent.append(document.createTextNode(text.slice(start, match.index)));
    const value = match[0];
    const tag = value.startsWith('**') ? 'strong' : 'em';
    const count = tag === 'strong' ? 2 : 1;
    parent.append(element(tag, value.slice(count, -count)));
    start = match.index + value.length;
  }
  parent.append(document.createTextNode(text.slice(start)));
}

function codeBlock(token, copy) {
  const wrapper = element('div', undefined, 'chat-code');
  const bar = element('div', undefined, 'chat-code-bar');
  const button = element('button', t('quickinput.render.copy_code'));
  button.type = 'button';
  button.setAttribute('data-i18n-role', 'code-copy');
  button.addEventListener('click', () => copy(token.text));
  const lang = element('span', token.language || t('quickinput.render.code_default'));
  if (!token.language) lang.setAttribute('data-i18n-role', 'code-lang-default');
  bar.append(lang, button);
  const pre = element('pre');
  pre.append(element('code', token.text));
  wrapper.append(bar, pre);
  return wrapper;
}

function formattedBlocks(tokens, fragment, copy, math) {
  let paragraph = [], line = [];
  function append(parent, parts) {
    for (const token of parts) {
      if (token.kind === 'text') inline(parent, token.text);
      else if (token.kind === 'math') parent.append(math.render(token));
      else parent.append(element(token.kind === 'code' ? 'code' : 'span', token.text,
        token.kind === 'source' ? 'chat-math-source' : undefined));
    }
  }
  function flushParagraph() {
    if (!paragraph.length) return;
    const p = element('p');
    append(p, paragraph); fragment.append(p); paragraph = [];
  }
  function flushLine() {
    if (line.every(token => token.kind === 'text' && !token.text.trim())) flushParagraph();
    else {
      const first = line[0]?.kind === 'text' ? line[0].text : '';
      const heading = /^(#{1,6})\s+/.exec(first);
      const quote = /^>\s?/.exec(first);
      const bullet = /^(?:[-*+]\s+|\d+\.\s+)/.exec(first);
      const prefix = heading || quote || bullet;
      if (prefix) {
        flushParagraph();
        const node = element(heading ? 'h' + Math.min(heading[1].length + 1, 6) : quote ? 'blockquote' : 'p',
          undefined, bullet ? 'chat-bullet' : undefined);
        line[0] = {kind: 'text', text: (bullet ? '• ' : '') + first.slice(prefix[0].length)};
        append(node, line); fragment.append(node);
      } else {
        if (paragraph.length) paragraph.push({kind: 'text', text: '\n'});
        paragraph.push(...line);
      }
    }
    line = [];
  }
  for (const token of tokens) {
    if (token.kind === 'code-block' || token.display) {
      flushLine(); flushParagraph();
      fragment.append(token.kind === 'code-block' ? codeBlock(token, copy) : token.kind === 'math' ?
        math.render(token) : element('div', token.text, 'chat-math-source'));
    } else if (token.kind === 'text') {
      const lines = token.text.split('\n');
      lines.forEach((text, index) => {
        if (text) line.push({kind: 'text', text});
        if (index < lines.length - 1) flushLine();
      });
    } else line.push(token);
  }
  flushLine(); flushParagraph();
}

export function markdown(text, copy, math = mathRenderer().message()) {
  const fragment = document.createDocumentFragment();
  // Dense markup and very long bodies remain selectable/copyable text without
  // turning a bounded byte payload into hundreds of thousands of DOM nodes.
  let markers = 0;
  for (const unused of text.matchAll(/[\n`*]/g)) { if (++markers > 256) break; }
  if (text.length > 32768 || markers > 256) {
    const pre = element('pre', text, 'chat-plain-text');
    const button = element('button', t('quickinput.render.copy_text'));
    button.type = 'button';
    button.setAttribute('data-i18n-role', 'copy-text');
    button.addEventListener('click', () => copy(text));
    fragment.append(pre, button);
    return fragment;
  }
  const tokens = markupTokens(text);
  formattedBlocks(tokens, fragment, copy, math);
  if (tokens.some(token => token.kind === 'math' || token.kind === 'source')) {
    const button = element('button', t('quickinput.render.copy_source'), 'chat-copy-source');
    button.type = 'button';
    button.setAttribute('data-i18n-role', 'copy-source');
    button.addEventListener('click', () => copy(text));
    fragment.append(button);
  }
  return fragment;
}

export function messageRenderer(container, {copy, expand}) {
  const cache = new Map();
  const math = mathRenderer();
  let mathNodes = 0;
  const render = messages => {
    math.beginRender();
    const identities = new Set(messages.map(message => message.ui_id ?? message.id));
    for (const [id, entry] of cache) {
      if (!identities.has(id)) { mathNodes -= entry.mathNodes; entry.node.remove(); cache.delete(id); }
    }
    messages.forEach((message, index) => {
      const text = messageText(message);
      const key = message.ui_id ?? message.id;
      const deliveryIdentity = message.delivery_status_key || message.delivery_status || '';
      const signature = message.id + ':' + deliveryIdentity + ':' + message.role + ':' + message.timestamp + ':' + message.truncated + ':' + message.transient + ':' + message.fragmentStart + ':' + text;
      let entry = cache.get(key);
      if (!entry) {
        entry = {node: element('article', undefined, 'chat-message'), mathNodes: 0};
        entry.node.dataset.messageId = key;
        cache.set(key, entry);
      }
      if (entry.signature !== signature) {
        entry.signature = signature;
        entry.node.classList.toggle('chat-user', message.role === 'user');
        const body = element('div', undefined, 'chat-message-body');
        mathNodes -= entry.mathNodes;
        const formulaBudget = math.message(MAX_WINDOW_MATH_NODES - mathNodes);
        body.append(markdown(text, copy, formulaBudget));
        entry.mathNodes = formulaBudget.nodes;
        mathNodes += entry.mathNodes;
        const author = element('div', undefined, 'chat-author');
        const authorLabel = document.createTextNode(authorRoleLabel(message.role, message.transient));
        author.append(authorLabel);
        let timeEl = null;
        if (message.timestamp && Number.isFinite(Date.parse(message.timestamp))) {
          timeEl = element('time', ' · ' + formatDateTime(message.timestamp));
          timeEl.dateTime = message.timestamp;
          author.append(timeEl);
        }
        entry.node.replaceChildren(author, body);
        let deliveryEl = null, fragmentNoticeEl = null, restartBtn = null, moreBtn = null;
        const deliveryText = message.delivery_status_key ? t(message.delivery_status_key) : (message.delivery_status || '');
        if (deliveryText) {
          deliveryEl = element('p', deliveryText, 'chat-delivery-status');
          entry.node.append(deliveryEl);
        }
        if (message.fragmentStart) {
          fragmentNoticeEl = element('p', t('quickinput.render.fragment_notice'), 'chat-author');
          restartBtn = element('button', t('quickinput.render.read_from_start'));
          restartBtn.type = 'button';
          restartBtn.addEventListener('click', () => expand(message.id, true));
          entry.node.append(fragmentNoticeEl, restartBtn);
        }
        if (message.truncated && !message.transient) {
          moreBtn = element('button', t(message.fragmentNext ? 'quickinput.render.read_next_fragment' : 'quickinput.render.read_full_message'));
          moreBtn.type = 'button';
          moreBtn.addEventListener('click', () => expand(message.id));
          entry.node.append(moreBtn);
        }
        entry.meta = {
          role: message.role,
          transient: Boolean(message.transient),
          timestamp: message.timestamp || '',
          deliveryStatusKey: message.delivery_status_key || '',
          deliveryStatusRaw: message.delivery_status || '',
          fragmentNext: Boolean(message.fragmentNext),
          authorLabel,
          timeEl,
          body,
          deliveryEl,
          fragmentNoticeEl,
          restartBtn,
          moreBtn,
        };
      }
      const current = container.children[index];
      if (current !== entry.node) container.insertBefore(entry.node, current ?? null);
    });
  };
  render.relocalize = () => {
    for (const entry of cache.values()) {
      const m = entry.meta;
      if (!m) continue;
      m.authorLabel.textContent = authorRoleLabel(m.role, m.transient);
      if (m.timeEl) m.timeEl.textContent = ' · ' + formatDateTime(m.timestamp);
      if (m.deliveryEl) m.deliveryEl.textContent = m.deliveryStatusKey ? t(m.deliveryStatusKey) : m.deliveryStatusRaw;
      if (m.fragmentNoticeEl) m.fragmentNoticeEl.textContent = t('quickinput.render.fragment_notice');
      if (m.restartBtn) m.restartBtn.textContent = t('quickinput.render.read_from_start');
      if (m.moreBtn) m.moreBtn.textContent = t(m.fragmentNext ? 'quickinput.render.read_next_fragment' : 'quickinput.render.read_full_message');
      relocalizeBodyControls(m.body);
    }
  };
  render.dispose = () => { cache.clear(); math.clear(); mathNodes = 0; };
  return render;
}
