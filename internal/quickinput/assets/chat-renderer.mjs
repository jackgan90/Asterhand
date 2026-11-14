import {messageText} from './chat-model.mjs';
import {markupTokens} from './chat-markup.mjs';
import {mathRenderer, MAX_WINDOW_MATH_NODES} from './chat-math.mjs';
import {formatDateTime, t} from './i18n.mjs';
import {toolActivity} from './chat-activities.mjs';

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

function fragmentControls(parent, state, id, expand, transient) {
  let notice, restart, more;
  if (state.fragmentStart) {
    notice = element('p', undefined, 'chat-author');
    restart = element('button');
    restart.type = 'button';
    restart.addEventListener('click', () => expand(id, true));
    parent.append(notice, restart);
  }
  if (state.truncated && !transient) {
    more = element('button'); more.type = 'button';
    more.addEventListener('click', () => expand(id));
    parent.append(more);
  }
  const relocalize = () => {
    if (notice) notice.textContent = t('quickinput.render.fragment_notice');
    if (restart) restart.textContent = t('quickinput.render.read_from_start');
    if (more) more.textContent = t(state.fragmentNext ? 'quickinput.render.read_next_fragment' : 'quickinput.render.read_full_message');
  };
  relocalize();
  return relocalize;
}

function messageBody(message, copy, expand, math) {
  const body = element('div', undefined, 'chat-message-body');
  const tools = [], fragments = [];
  const structured = message.blocks?.some(block => block.type === 'tool' || block.id);
  if (!structured) body.append(markdown(messageText(message), copy, math));
  else for (const block of message.blocks) {
    if (block.type === 'tool' && block.tool) {
      const node = toolActivity(block.tool);
      tools.push(node); body.append(node);
    } else if (block.type === 'text') {
      const section = element('div', undefined, 'chat-message-text');
      section.append(markdown(block.text ?? '', copy, math));
      fragments.push(fragmentControls(section, block, block.id ?? message.id, expand, message.transient));
      body.append(section);
    }
  }
  return {body, tools, fragments, structured};
}

function bodyIdentity(message) {
  return JSON.stringify((message.blocks ?? []).map(block => block.type === 'tool' ?
    {type: block.type, id: block.tool?.id, tool: block.tool?.tool} : block));
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
      const key = message.ui_id ?? message.id;
      const deliveryIdentity = message.delivery_status_key || message.delivery_status || '';
      const signature = message.id + ':' + deliveryIdentity + ':' + message.role + ':' + message.timestamp + ':' + message.truncated + ':' + message.transient + ':' + message.fragmentStart + ':' + bodyIdentity(message);
      let entry = cache.get(key);
      if (!entry) {
        entry = {node: element('article', undefined, 'chat-message'), mathNodes: 0};
        entry.node.dataset.messageId = key;
        cache.set(key, entry);
      }
      if (entry.signature !== signature) {
        entry.signature = signature;
        entry.node.classList.toggle('chat-user', message.role === 'user');
        mathNodes -= entry.mathNodes;
        const formulaBudget = math.message(MAX_WINDOW_MATH_NODES - mathNodes);
        const {body, tools, fragments, structured} = messageBody(message, copy, expand, formulaBudget);
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
        const toolOnly = tools.length > 0 && !message.blocks.some(block => block.type === 'text' && (block.text || block.truncated));
        entry.node.classList.toggle('chat-tool-only', toolOnly);
        entry.node.replaceChildren(...(toolOnly ? [body] : [author, body]));
        let deliveryEl = null;
        const deliveryText = message.delivery_status_key ? t(message.delivery_status_key) : (message.delivery_status || '');
        if (deliveryText) {
          deliveryEl = element('p', deliveryText, 'chat-delivery-status');
          entry.node.append(deliveryEl);
        }
        if (!structured) fragments.push(fragmentControls(entry.node, message, message.id, expand, message.transient));
        entry.meta = {
          role: message.role,
          transient: Boolean(message.transient),
          timestamp: message.timestamp || '',
          deliveryStatusKey: message.delivery_status_key || '',
          deliveryStatusRaw: message.delivery_status || '',
          authorLabel,
          timeEl,
          body,
          deliveryEl,
          fragments,
          tools,
        };
      }
      const tools = (message.blocks ?? []).filter(block => block.type === 'tool' && block.tool);
      entry.meta.tools.forEach((node, toolIndex) => node.update(tools[toolIndex].tool));
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
      m.fragments.forEach(relocalize => relocalize());
      m.tools.forEach(node => node.relocalize());
      relocalizeBodyControls(m.body);
    }
  };
  render.dispose = () => { cache.clear(); math.clear(); mathNodes = 0; };
  return render;
}
