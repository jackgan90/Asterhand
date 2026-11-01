import {messageText} from './chat-model.mjs';
import {markupTokens} from './chat-markup.mjs';
import {mathRenderer, MAX_WINDOW_MATH_NODES} from './chat-math.mjs';

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
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
  const button = element('button', '复制代码');
  button.type = 'button';
  button.addEventListener('click', () => copy(token.text));
  bar.append(element('span', token.language || '代码'), button);
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
    const button = element('button', '复制文本');
    button.type = 'button'; button.addEventListener('click', () => copy(text));
    fragment.append(pre, button);
    return fragment;
  }
  const tokens = markupTokens(text);
  formattedBlocks(tokens, fragment, copy, math);
  if (tokens.some(token => token.kind === 'math' || token.kind === 'source')) {
    const button = element('button', '复制原文', 'chat-copy-source');
    button.type = 'button'; button.addEventListener('click', () => copy(text));
    fragment.append(button);
  }
  return fragment;
}

export function messageRenderer(container, {copy, expand, cancelQueued}) {
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
      const signature = message.id + ':' + message.queue_operation + ':' + message.queue_restore + ':' + message.delivery_status + ':' + message.role + ':' + message.timestamp + ':' + message.truncated + ':' + message.transient + ':' + message.fragmentStart + ':' + text;
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
        const label = message.role === 'user' ? '你' : message.transient ? '助手 · 生成中' : '助手';
        const author = element('div', label, 'chat-author');
        if (message.timestamp && Number.isFinite(Date.parse(message.timestamp))) {
          const time = element('time', ' · ' + new Date(message.timestamp).toLocaleString());
          time.dateTime = message.timestamp; author.append(time);
        }
        entry.node.replaceChildren(author, body);
        if (message.delivery_status) entry.node.append(element('p', message.delivery_status, 'chat-delivery-status'));
        if (message.queue_operation && cancelQueued) {
          const actions = message.queue_restore ? [['取回编辑', true], ['取消发送', false]] : [['移出队列', false]];
          for (const [label, restore] of actions) {
            const button = element('button', label, 'chat-queue-action');
            button.type = 'button';
            button.addEventListener('click', () => cancelQueued(message.queue_operation, message.queue_conversation, restore));
            entry.node.append(button);
          }
        }
        if (message.fragmentStart) {
          const restart = element('button', '从头读取');
          restart.type = 'button';
          restart.addEventListener('click', () => expand(message.id, true));
          entry.node.append(element('p', '当前显示后续片段；更早正文仍可重新读取。', 'chat-author'), restart);
        }
        if (message.truncated && !message.transient) {
          const button = element('button', message.fragmentNext ? '读取后续片段' : '读取完整消息');
          button.type = 'button';
          button.addEventListener('click', () => expand(message.id));
          entry.node.append(button);
        }
      }
      const current = container.children[index];
      if (current !== entry.node) container.insertBefore(entry.node, current ?? null);
    });
  };
  render.dispose = () => { cache.clear(); math.clear(); mathNodes = 0; };
  return render;
}
