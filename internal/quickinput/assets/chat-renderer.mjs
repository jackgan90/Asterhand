import {messageText} from './chat-model.mjs';

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}

// All content is text or created DOM nodes. Raw HTML, media, external navigation,
// style attributes, and embedded widgets are never interpreted.
function inline(parent, text) {
  const pattern = /(`[^`\n]+`|\*\*[^*\n]+\*\*|\*[^*\n]+\*)/g;
  let start = 0;
  for (const match of text.matchAll(pattern)) {
    parent.append(document.createTextNode(text.slice(start, match.index)));
    const value = match[0];
    const tag = value.startsWith('`') ? 'code' : value.startsWith('**') ? 'strong' : 'em';
    const count = tag === 'strong' ? 2 : 1;
    parent.append(element(tag, value.slice(count, -count)));
    start = match.index + value.length;
  }
  parent.append(document.createTextNode(text.slice(start)));
}

export function markdown(text, copy) {
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
  const lines = text.split('\n');
  let paragraph = [];
  const flush = () => {
    if (!paragraph.length) return;
    const p = element('p');
    inline(p, paragraph.join('\n'));
    fragment.append(p);
    paragraph = [];
  };
  for (let index = 0; index < lines.length; index++) {
    const line = lines[index];
    const fence = /^\s*```([\w+-]*)\s*$/.exec(line);
    if (fence) {
      flush();
      const content = [];
      while (++index < lines.length && !/^\s*```\s*$/.test(lines[index])) content.push(lines[index]);
      const code = content.join('\n');
      const wrapper = element('div', undefined, 'chat-code');
      const bar = element('div', undefined, 'chat-code-bar');
      const button = element('button', '复制代码');
      button.type = 'button';
      button.addEventListener('click', () => copy(code));
      bar.append(element('span', fence[1] || '代码'), button);
      const pre = element('pre');
      pre.append(element('code', code));
      wrapper.append(bar, pre);
      fragment.append(wrapper);
      continue;
    }
    const heading = /^(#{1,6})\s+(.+)$/.exec(line);
    const quote = /^>\s?(.*)$/.exec(line);
    const bullet = /^(?:[-*+]\s+|\d+\.\s+)(.*)$/.exec(line);
    if (heading || quote || bullet) {
      flush();
      const node = element(heading ? 'h' + Math.min(heading[1].length + 1, 6) : quote ? 'blockquote' : 'p', undefined, bullet ? 'chat-bullet' : undefined);
      inline(node, heading ? heading[2] : quote ? quote[1] : '• ' + bullet[1]);
      fragment.append(node);
    } else if (!line.trim()) flush();
    else paragraph.push(line);
  }
  flush();
  return fragment;
}

export function messageRenderer(container, {copy, expand}) {
  const cache = new Map();
  return messages => {
    const identities = new Set(messages.map(message => message.ui_id ?? message.id));
    for (const [id, entry] of cache) {
      if (!identities.has(id)) { entry.node.remove(); cache.delete(id); }
    }
    messages.forEach((message, index) => {
      const text = messageText(message);
      const key = message.ui_id ?? message.id;
      const signature = message.id + ':' + message.delivery_status + ':' + message.role + ':' + message.timestamp + ':' + message.truncated + ':' + message.transient + ':' + message.fragmentStart + ':' + text;
      let entry = cache.get(key);
      if (!entry) {
        entry = {node: element('article', undefined, 'chat-message')};
        entry.node.dataset.messageId = key;
        cache.set(key, entry);
      }
      if (entry.signature !== signature) {
        entry.signature = signature;
        entry.node.classList.toggle('chat-user', message.role === 'user');
        const body = element('div', undefined, 'chat-message-body');
        body.append(markdown(text, copy));
        const label = message.role === 'user' ? '你' : message.transient ? '助手 · 生成中' : '助手';
        const author = element('div', label, 'chat-author');
        if (message.timestamp && Number.isFinite(Date.parse(message.timestamp))) {
          const time = element('time', ' · ' + new Date(message.timestamp).toLocaleString());
          time.dateTime = message.timestamp; author.append(time);
        }
        entry.node.replaceChildren(author, body);
        if (message.delivery_status) entry.node.append(element('p', message.delivery_status, 'chat-delivery-status'));
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
}
