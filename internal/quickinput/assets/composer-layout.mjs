// A local experience choice: 0 fits the text, 1 leaves one spare line.
export const compactExtraRows = 0;
const maxRows = 10;
const expandedHeight = 760;
const pixels = value => Number.parseFloat(value) || 0;
const verticalEdges = style => pixels(style.paddingTop) + pixels(style.paddingBottom) +
  pixels(style.borderTopWidth) + pixels(style.borderBottomWidth);
const verticalMargins = style => pixels(style.marginTop) + pixels(style.marginBottom);

export function compactRows(text, measuredRows, extraRows = compactExtraRows) {
  return text === '' ? 1 : Math.min(maxRows, Math.max(1, measuredRows) + extraRows);
}

function measurementInput(input) {
  const mirror = document.createElement('textarea');
  mirror.className = 'input-measure';
  mirror.tabIndex = -1;
  mirror.readOnly = true;
  mirror.setAttribute('aria-hidden', 'true');
  document.body.append(mirror);
  return mirror;
}

function measureText(input, mirror, style) {
  for (const property of ['fontFamily', 'fontSize', 'fontWeight', 'fontStyle', 'lineHeight',
    'letterSpacing', 'wordSpacing', 'tabSize', 'textIndent', 'textTransform', 'direction',
    'whiteSpace', 'overflowWrap', 'wordBreak', 'paddingTop', 'paddingBottom',
    'paddingLeft', 'paddingRight', 'borderTopWidth', 'borderBottomWidth',
    'borderLeftWidth', 'borderRightWidth']) mirror.style[property] = style[property];
  mirror.style.width = `${input.getBoundingClientRect().width}px`;
  mirror.wrap = input.wrap;
  mirror.value = input.value;
  const line = pixels(style.lineHeight);
  const padding = pixels(style.paddingTop) + pixels(style.paddingBottom);
  // scrollHeight is integer-rounded; rounding to the nearest line avoids an
  // extra row at fractional line heights (the current font uses 28.05px).
  return {line, rows: Math.max(1, Math.round((mirror.scrollHeight - padding) / line))};
}

function auxiliaryHeight(main, input, desired = true) {
  const style = window.getComputedStyle(main);
  let height = verticalEdges(style);
  for (const child of main.children) {
    if (child === input || child.hidden) continue;
    const childStyle = window.getComputedStyle(child);
    if (childStyle.display !== 'none') {
      const commandHeight = desired && child.id === 'command-panel' ? Number(child.dataset.desiredHeight) : 0;
      height += (commandHeight || child.getBoundingClientRect().height) + verticalMargins(childStyle);
    }
  }
  return height;
}

function fitCommandList(main, input, editorHeight, expanded, settled) {
  const panel = document.getElementById('command-panel'), list = document.getElementById('command-list');
  if (!panel || panel.hidden || !list) return;
  panel.dataset.layoutSettled = String(settled);
  // The old compact viewport is not a work-area limit. Keep the intended
  // list extent until the current native allocation reaches the renderer.
  if (!settled) { list.style.maxHeight = '448px'; return; }
  let fixed = verticalEdges(window.getComputedStyle(main)) + editorHeight + verticalMargins(window.getComputedStyle(input));
  for (const child of main.children) {
    if (child === input || child.hidden) continue;
    const style = window.getComputedStyle(child);
    if (style.display === 'none') continue;
    if (child === panel) {
      fixed += panel.querySelector('.command-toolbar').getBoundingClientRect().height + verticalEdges(style) + verticalMargins(style);
    } else if (expanded && child.id === 'chat-panel') fixed += 140 + verticalMargins(style);
    else fixed += child.getBoundingClientRect().height + verticalMargins(style);
  }
  list.style.maxHeight = `${Math.max(40, Math.min(448, window.innerHeight - fixed))}px`;
}

export class ComposerLayout {
  constructor(input, resize, {extraRows = compactExtraRows} = {}) {
    this.input = input;
    this.main = document.querySelector('main');
    this.resize = resize;
    this.extraRows = extraRows;
    this.mirror = measurementInput(input);
    this.visible = false;
    this.expanded = false;
    this.disposed = false;
    this.frame = null;
    this.lastHeight = 0;
    this.resizeID = 0;
    this.appliedHeight = null;
    this.awaitingAllocation = false;
    this.confirmedLimit = false;
    this.followEnd = false;
    this.holdingTop = false;
    this.onResize = () => this.schedule();
    this.onScroll = () => {
      if (this.holdingTop && this.input.scrollTop !== 0) this.input.scrollTop = 0;
    };
    this.sizeObserver = new window.ResizeObserver(() => this.schedule());
    this.controlsObserver = new window.MutationObserver(() => this.schedule());
    document.fonts.ready.then(() => this.schedule());
  }

  observe() {
    window.addEventListener('resize', this.onResize);
    this.input.addEventListener('scroll', this.onScroll);
    this.sizeObserver.observe(this.main);
    for (const child of this.main.children) if (child !== this.input) this.sizeObserver.observe(child);
    this.controlsObserver.observe(this.main, {subtree: true, attributes: true, attributeFilter: ['hidden']});
  }

  stopObserving() {
    window.removeEventListener('resize', this.onResize);
    this.input.removeEventListener('scroll', this.onScroll);
    this.holdingTop = false;
    this.sizeObserver.disconnect();
    this.controlsObserver.disconnect();
    if (this.frame !== null) window.cancelAnimationFrame(this.frame);
    this.frame = null;
  }

  setVisible(value) {
    if (this.disposed || this.visible === value) return;
    this.visible = value;
    if (value) { this.observe(); this.flush(); }
    else this.stopObserving();
  }

  setExpanded(value) {
    this.expanded = value;
    this.flush();
  }

  applied(id, height) {
    if (this.disposed || id !== String(this.resizeID) || !Number.isFinite(height) || height <= 0) return;
    this.appliedHeight = height;
    this.schedule();
  }

  requestHeight(height) {
    if (height === this.lastHeight) return;
    this.lastHeight = height;
    this.appliedHeight = null;
    this.awaitingAllocation = true;
    this.resize(height, String(++this.resizeID));
  }

  fitCommands(editorHeight) {
    // The current request's reply and matching renderer viewport must both
    // arrive. An older shrink can still be in flight even when content fits.
    const applied = this.appliedHeight !== null && Math.abs(window.innerHeight - this.appliedHeight) <= 1;
    if (applied) this.awaitingAllocation = false;
    fitCommandList(this.main, this.input, editorHeight, this.expanded, !this.awaitingAllocation);
    return applied;
  }

  schedule(editing = false) {
    if (this.disposed) return;
    this.followEnd ||= editing;
    if (!this.visible || this.frame !== null) return;
    this.frame = window.requestAnimationFrame(() => { this.frame = null; this.flush(); });
  }

  flush() {
    if (this.disposed) return;
    const input = this.input;
    let height = expandedHeight;
    let followAfterResize = false;
    if (this.expanded) {
      this.holdingTop = false;
      input.style.removeProperty('height');
      input.style.removeProperty('max-height');
      input.style.removeProperty('overflow-y');
      this.requestHeight(height);
      this.fitCommands(104);
    } else {
      const style = window.getComputedStyle(input);
      const {line, rows} = measureText(input, this.mirror, style);
      const desiredChrome = auxiliaryHeight(this.main, input) + verticalMargins(style);
      const editorHeight = Math.ceil(compactRows(input.value, rows, this.extraRows) * line + verticalEdges(style));
      // Desired height must not depend on the (possibly work-area-clamped)
      // viewport. Only the actual editor allocation is constrained by it.
      height = Math.ceil(desiredChrome + editorHeight);
      this.requestHeight(height);
      const applied = this.fitCommands(editorHeight);
      const chrome = auxiliaryHeight(this.main, input, false) + verticalMargins(style);
      // A newly visible command list occupies space before the window grows.
      // Retain the editor's existing allocation so its focus underline cannot
      // jump upward; a matching native allocation still applies real limits.
      const commandPending = this.awaitingAllocation && document.getElementById('command-panel')?.hidden === false;
      const retainedHeight = commandPending ? input.getBoundingClientRect().height : 0;
      const available = Math.max(1, window.innerHeight - chrome, retainedHeight);
      const top = input.scrollTop;
      input.style.height = `${editorHeight}px`;
      input.style.maxHeight = `${available}px`;
      const contentHeight = Math.ceil(rows * line + verticalEdges(style));
      const overflows = contentHeight > available + 1;
      // A new request does not undo an already confirmed limit. Keep its
      // scrollbar until content fits or a matching allocation clears the cap.
      if (!overflows) this.confirmedLimit = false;
      else if (applied) this.confirmedLimit = editorHeight > available + 1;
      input.style.overflowY = rows > maxRows || this.confirmedLimit && overflows ? 'auto' : 'hidden';
      // Keep the browser's current scroll position. Native editing already
      // accounts for full-word wrapping; a truncated prefix cannot locate its
      // caret reliably and must not override a correctly scrolled viewport.
      const editingAtEnd = this.followEnd && document.activeElement === input &&
        input.selectionStart === input.value.length && input.selectionEnd === input.value.length;
      // Do not reveal a clipped preceding row while the native window is still
      // growing. Retain end-follow intent for the allocated viewport, including
      // the case where the host later confirms that growth is constrained.
      this.holdingTop = this.awaitingAllocation && !applied && !this.confirmedLimit && editorHeight > available + 1 &&
        (rows <= maxRows || editingAtEnd);
      input.scrollTop = this.holdingTop ? 0 : editingAtEnd ? input.scrollHeight : top;
      followAfterResize = this.awaitingAllocation && editingAtEnd;
    }
    this.followEnd = followAfterResize;
  }

  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    this.stopObserving();
    this.mirror.value = '';
    this.mirror.remove();
  }
}
