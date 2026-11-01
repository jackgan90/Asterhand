import {domFixture} from './web-fixture.mjs';

// Explicit geometry for event/lifecycle tests only. Native WebView2 tests
// establish the actual relationship between text, wrapping and scrollHeight.
export function composerFixture() {
  const dom = domFixture(), frames = new Map(), observers = [];
  let frameID = 0, measuredRows = 1;
  const input = dom.select('#input'), main = dom.select('main');
  const styles = new Map();
  const base = {display:'block', paddingTop:'0', paddingBottom:'0', borderTopWidth:'0', borderBottomWidth:'0',
    marginTop:'0', marginBottom:'0', lineHeight:'28.05px'};
  const decorate = node => {
    node.style = {removeProperty(name) { delete this[name.replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())]; }};
    node.remove = () => { dom.document.body.children = dom.document.body.children.filter(child => child !== node); };
    node.getBoundingClientRect = () => ({width:640, height:0});
    return node;
  };
  const createElement = dom.document.createElement;
  dom.document.createElement = tag => {
    const node = decorate(createElement(tag));
    if (tag === 'textarea') Object.defineProperty(node, 'scrollHeight', {
      get: () => Math.round(16 + (node.value ? measuredRows : 1) * 28.05),
    });
    return node;
  };
  for (const node of dom.elements.values()) decorate(node);
  dom.document.body = dom.document.createElement('body');
  dom.document.fonts = {ready:Promise.resolve()};
  input.wrap = 'soft'; input.clientHeight = 45; input.scrollTop = 0;
  styles.set(input, {...base, paddingTop:'8px', paddingBottom:'8px', marginTop:'3px'});
  styles.set(main, {...base, borderTopWidth:'1px', borderBottomWidth:'1px'});
  const children = ['header', '#chat-panel', '#chat-draft-decision', '#input', '.chat-actions', 'footer'].map(dom.select);
  main.children = children;
  for (const node of children) {
    if (!node.style) decorate(node);
    if (node !== input) node.getBoundingClientRect = () => ({width:640, height:node.hidden ? 0 : 30});
  }
  dom.select('#chat-panel').hidden = true;
  dom.select('#chat-draft-decision').hidden = true;
  class Observer {
    constructor(callback) { this.callback = callback; this.targets = []; observers.push(this); }
    observe(target) { this.targets.push(target); }
    disconnect() { this.targets = []; }
  }
  const view = Object.assign(new EventTarget(), {
    innerHeight:180, ResizeObserver:Observer, MutationObserver:Observer,
    getComputedStyle: node => ({...base, ...styles.get(node)}),
    requestAnimationFrame(callback) { frames.set(++frameID, callback); return frameID; },
    cancelAnimationFrame(id) { frames.delete(id); },
  });
  return {...dom, view, input, frames, observers, setRows(value) { measuredRows = value; },
    flushFrames() { for (const [id, callback] of [...frames]) { frames.delete(id); callback(); } }};
}
