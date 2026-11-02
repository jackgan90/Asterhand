// Small DOM/clock fixture for user-operation tests; real browser checks are separate.
export class Element extends EventTarget {
  constructor(tag = "div", document) {
    super();
    this.tagName = tag.toUpperCase();
    this.document = document;
    this.children = [];
    this.dataset = {};
    this.attributes = {};
    this.value = "";
    this.checked = false;
    this.hidden = false;
    this.elements = {};
    this.style = {};
    this.classList = {add:(...names)=>{this.className=[this.className||'',...names].join(' ').trim();}};
  }
  get options() { return this.children; }
  append(...elements) {
    for(const element of elements){element.parentElement=this;this.children.push(element);}
    if (this.tagName === "SELECT" && !this.value) this.value = elements[0]?.value || "";
  }
  replaceChildren(...elements) { this.children = []; if (this.tagName === "SELECT") this.value = ""; this.append(...elements); }
  remove(){if(this.parentElement)this.parentElement.children=this.parentElement.children.filter(child=>child!==this);this.parentElement=null;}
  querySelectorAll(selector){const tags=selector.split(',').map(tag=>tag.trim().toUpperCase());return descendants(this).filter(el=>tags.includes(el.tagName));}
  setPointerCapture() {}
  setAttribute(key, value) { this.attributes[key] = value; }
  getAttribute(key) { return this.attributes[key] ?? null; }
  hasAttribute(key) { return Object.hasOwn(this.attributes, key); }
  setSelectionRange(start, end) { this.selectionStart = start; this.selectionEnd = end; }
  focus() { this.document.activeElement = this; this.dispatchEvent(new Event("focus")); this.onfocus?.(); }
  blur() { this.document.activeElement = null; this.dispatchEvent(new Event("blur")); this.onblur?.(); }
  showModal() { this.open = true; }
  close() { this.open = false; this.dispatchEvent(new Event("close")); }
  reset() { for (const field of Object.values(this.elements)) field.value = ""; }
  fire(type) {
    const event = new Event(type, {cancelable:true});
    this.dispatchEvent(event);
    this[`on${type}`]?.(event);
  }
}

export function domFixture() {
  const elements = new Map();
  const document = new EventTarget();
  document.compatMode = 'CSS1Compat';
  const select = id => {
    if (!elements.has(id)) elements.set(id, new Element("div", document));
    return elements.get(id);
  };
  // Optional feature panels are absent in the base fixture. Dedicated tests
  // explicitly create them with select() before mounting their controllers.
  const optional = new Set(['#local-commands', '#command-panel', '#command-list']);
  document.querySelector = selector => optional.has(selector) && !elements.has(selector) ? null : selector === ".question input:focus" ? document.activeElement : select(selector);
  document.querySelectorAll = () => [];
  document.createElement = tag => new Element(tag, document);
  document.createElementNS = (namespaceURI,tag) => Object.assign(new Element(tag,document),{namespaceURI});
  document.createDocumentFragment = () => new Element('fragment',document);
  document.createTextNode = text => ({textContent:text});
  document.getElementById = id => document.querySelector("#"+id);
  document.documentElement = {dataset:{}};
  for (const id of ["#target", "#project", "#desktop-key", "#locale-select", "#settings-locale"]) select(id).tagName = "SELECT";
  for (const [id, names] of Object.entries({
    "#desktop-form":["ctrl","alt","shift","key","enabled","theme"],
    "#settings-form":["name","endpoint","peer_listen","agent_runtime","provider","model","credential_ref","root","key","delete_key"],
    "#login-form":["token"], "#pair-form":["code"],
  })) {
    for (const name of names) select(id).elements[name] = name === "key" && id === "#desktop-form" ? select("#desktop-key") : document.createElement(["agent_runtime","provider","model"].includes(name) ? "select" : "input");
  }
  return {document, elements, select};
}

export function clockFixture() {
  let next = 0;
  const timers = new Map();
  return {
    timers,
    setTimeout(fn, delay) { const id = ++next; timers.set(id, {fn, delay}); return id; },
    clearTimeout(id) { timers.delete(id); },
    setInterval(fn, delay) { const id = ++next; timers.set(id, {fn, delay, interval:true}); return id; },
    clearInterval(id) { timers.delete(id); },
    async fire(delay) {
      for (const [id, timer] of [...timers]) if (timer.delay === delay) {
        if (!timer.interval) timers.delete(id);
        await timer.fn();
      }
    },
  };
}

export const tick = () => new Promise(resolve => setImmediate(resolve));
export const deferred = () => {
  let resolve, reject;
  const promise = new Promise((a,b) => { resolve = a; reject = b; });
  return {promise, resolve, reject};
};
export function descendants(root) { return root.children.flatMap(child => [child, ...descendants(child)]); }
