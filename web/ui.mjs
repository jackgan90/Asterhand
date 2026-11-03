import {formatDateTime, localizeError, t} from "./i18n.mjs";

export {localizeError};
export const $ = (selector, root = document) => root.querySelector(selector);
export function node(tag, text, className) {
  const element = document.createElement(tag);
  if (text != null) element.textContent = text;
  if (className) element.className = className;
  return element;
}
export const date = value => value ? formatDateTime(value) : t("common.unconfirmed");
export function dialog(id) { if (!$(id).open) $(id).showModal(); }

// The page follows the quick-input panel theme saved on this node.
const themeKey = "asterhand-theme";
export function applyTheme(id) {
  const root = globalThis.document?.documentElement;
  if (!root?.dataset || !id) return;
  root.dataset.theme = id === "frost" ? "frost" : "star";
  try { localStorage.setItem(themeKey, root.dataset.theme); } catch { /* storage unavailable */ }
}
export function restoreTheme() {
  try { applyTheme(localStorage.getItem(themeKey)); } catch { /* storage unavailable */ }
}

// Settings panes are CSS-switched; every field stays in its original form.
export function settingsTabs(root, listen) {
  const tabs = [...document.querySelectorAll("[data-tab-target]")];
  const select = name => {
    root.dataset.tab = name;
    for (const tab of tabs) tab.setAttribute("aria-selected", String(tab.dataset.tabTarget === name));
  };
  for (const tab of tabs) listen(tab, "click", () => select(tab.dataset.tabTarget));
  // A hidden invalid control cannot receive focus, so reveal its pane first.
  listen($("#settings-form"), "invalid", event => {
    const pane = event.target.closest?.("[data-pane]");
    if (pane) select(pane.dataset.pane);
    const disclosure = event.target.closest?.("details");
    if (disclosure) disclosure.open = true;
  }, true);
  select(root.dataset.tab || "general");
}
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
