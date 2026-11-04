import {node} from './ui.mjs';

// Context menu for the graph canvas. Items are rebuilt on every open, so their
// labels always use the current language.
export function graphMenu(host) {
  const menu = node('div', null, 'graph-menu');
  menu.hidden = true;
  menu.setAttribute('role', 'menu');
  host.append(menu);
  let open = false;
  function close() {
    if (!open) return;
    open = false;
    menu.hidden = true;
    menu.replaceChildren();
  }
  // items: [{label, action, disabled?, danger?}]; x/y are relative to host.
  function show(x, y, items) {
    menu.replaceChildren();
    for (const item of items) {
      const entry = node('button', item.label, 'graph-menu-item');
      entry.type = 'button';
      entry.setAttribute('role', 'menuitem');
      entry.disabled = Boolean(item.disabled);
      if (item.danger) entry.dataset.danger = 'true';
      entry.onclick = () => { close(); item.action(); };
      menu.append(entry);
    }
    if (!items.length) return;
    menu.style.left = `${Math.max(0, x)}px`;
    menu.style.top = `${Math.max(0, y)}px`;
    menu.hidden = false;
    open = true;
    menu.children[0]?.focus?.();
  }
  menu.addEventListener('keydown', event => {
    if (event.key !== 'Escape') return;
    event.preventDefault(); event.stopPropagation(); close();
  });
  globalThis.document?.addEventListener?.('pointerdown', event => { if (open && !menu.contains(event.target)) close(); });
  return {show, close, get open() { return open; }};
}
