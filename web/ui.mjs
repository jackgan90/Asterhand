export const $ = (selector, root = document) => root.querySelector(selector);
export function node(tag, text, className) {
  const element = document.createElement(tag);
  if (text != null) element.textContent = text;
  if (className) element.className = className;
  return element;
}
export const date = value => value ? new Date(value).toLocaleString() : "未确认";
export function dialog(id) { if (!$(id).open) $(id).showModal(); }
export function noticeView(element, clock = globalThis) {
  let timer;
  function stop() {
    clock.clearTimeout(timer);
    timer = undefined;
    element.hidden = true;
  }
  function show(text) {
    stop();
    element.textContent = text;
    element.hidden = false;
    timer = clock.setTimeout(() => { element.hidden = true; }, 7000);
  }
  return {show, stop};
}
