// Minimal DOM for the actual KaTeX DOM renderer and its budgets. Layout, fonts,
// CSS animation and native interactions are checked in real WebView2 tests.
export class RenderNode {
  constructor(tag = 'span', text = '') {
    this.tagName = tag; this.nodeType = tag === '#text' ? 3 : 1;
    this._text = text; this.children = []; this.style = {}; this.attributes = {};
    this.dataset = {}; this.className = ''; this.listeners = new Map();
    this.classList = {toggle: (name, value) => {
      const classes = new Set(this.className.split(' ').filter(Boolean));
      if (value) classes.add(name); else classes.delete(name);
      this.className = [...classes].join(' ');
    }};
  }
  get textContent() { return this._text + this.children.map(node => node.textContent).join(''); }
  set textContent(value) { this._text = String(value); this.children = []; }
  get outerHTML() { return JSON.stringify({tag: this.tagName, text: this._text, style: this.style, attributes: this.attributes}) + this.children.map(node => node.outerHTML).join(''); }
  append(...nodes) {
    for (const node of nodes) {
      if (node.tagName === '#fragment') this.append(...[...node.children]);
      else { node.remove(); node.parent = this; this.children.push(node); }
    }
  }
  appendChild(node) { this.append(node); return node; }
  replaceChildren(...nodes) { this.children = []; this._text = ''; this.append(...nodes); }
  remove() { if (this.parent) { this.parent.children = this.parent.children.filter(node => node !== this); this.parent = null; } }
  insertBefore(node, next) {
    node.remove(); node.parent = this;
    const at = next ? this.children.indexOf(next) : this.children.length;
    this.children.splice(at, 0, node);
  }
  setAttribute(key, value) { this.attributes[key] = String(value); }
  getAttribute(key) { return this.attributes[key] ?? null; }
  addEventListener(key, listener) { this.listeners.set(key, listener); }
  querySelectorAll() { return this.children.flatMap(node => [...(node.nodeType === 1 ? [node] : []), ...node.querySelectorAll()]); }
  cloneNode(deep) {
    const node = new RenderNode(this.tagName, this._text);
    node.style = {...this.style}; node.attributes = {...this.attributes}; node.className = this.className;
    if (deep) node.append(...this.children.map(child => child.cloneNode(true)));
    return node;
  }
}

export function rendererDocument(t) {
  const previous = globalThis.document;
  globalThis.document = {compatMode: 'CSS1Compat', createElement: tag => new RenderNode(tag),
    createElementNS: (ns, tag) => new RenderNode(tag), createTextNode: text => new RenderNode('#text', text),
    createDocumentFragment: () => new RenderNode('#fragment')};
  t.after(() => { if (previous === undefined) delete globalThis.document; else globalThis.document = previous; });
}

export const withClass = (root, name) => [root, ...root.querySelectorAll('*')].filter(node => node.className.split(' ').includes(name));
