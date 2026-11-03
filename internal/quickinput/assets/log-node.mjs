import {t} from './i18n.mjs';

const states = {
  waiting: 'common.graph.waiting', ready: 'common.graph.ready',
  starting: 'common.graph.starting', running: 'common.command.running',
  stopping: 'common.command.stopping', draining: 'common.graph.draining',
  succeeded: 'common.command.succeeded', failed: 'common.command.failed',
  interrupted: 'common.command.interrupted', skipped: 'common.graph.skipped',
  unknown: 'common.unconfirmed',
};

export function nodeState(node) {
  const state = node?.phase === 'finished' ? node.result : node?.phase;
  return Object.hasOwn(states, state) ? state : 'unknown';
}

export const nodeLabel = node => t(states[nodeState(node)]);
export const nodeName = node => node?.kind === 'start' ? t('common.graph.start') :
  node?.kind === 'end' ? t('common.graph.end') : node?.name || t('common.graph.unnamed');
export const nodeActive = node => node?.kind === 'script' && ['starting', 'running', 'stopping', 'draining'].includes(node.phase);

// Choices describe a view of the current run; they never alter execution.
export function logSelection() {
  let mode = 'all';
  const selected = new Set();
  return {
    get mode() { return mode; },
    has(id) { return mode === 'selected' && selected.has(id); },
    choose(next) { mode = next; selected.clear(); },
    toggle(id, checked) {
      if (mode !== 'selected') selected.clear();
      mode = 'selected';
      if (checked) selected.add(id); else selected.delete(id);
    },
    nodes(nodes) {
      return nodes.filter(node => node.kind === 'script' && (mode === 'active' ? nodeActive(node) : selected.has(node.node_id)));
    },
  };
}
