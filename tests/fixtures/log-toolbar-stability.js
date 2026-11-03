window.__logToolbarStability = null;
(async () => {
  const {logPane} = await import('./log-pane.mjs');
  const stylesheet = document.createElement('link');
  stylesheet.rel = 'stylesheet';
  stylesheet.href = './command-log.css';
  await new Promise((resolve, reject) => {
    stylesheet.onload = resolve;
    stylesheet.onerror = reject;
    document.head.append(stylesheet);
  });
  const run = {run_id: 'toolbar-test', phase: 'running', command: {kind: 'graph'}, nodes: []};
  const chunk = text => ({node_id: '', stream: 'stdout', data: btoa(text)});
  const theme = document.documentElement.dataset.theme, locale = document.documentElement.lang;
  let calls = 0;
  const observed = [];
  const pane = logPane({commandID: 'toolbar-command', record: run, encoding: 'utf-8',
    read: async () => {
      const initial = calls++ === 0;
      if (!initial) {
        const button = pane.element.querySelector('#log-latest');
        observed.push({disabled: button.disabled, opacity: getComputedStyle(button).opacity});
        await new Promise(resolve => setTimeout(resolve, 40));
      }
      return {log: {run, chunks: initial ? [chunk('initial output')] : calls % 2 ? [chunk(' more output')] : [],
        start: 100, previous: 0, next: 200 + calls, end: true}};
    }});
  document.body.append(pane.element);
  await pane.refresh();
  const buttons = [...pane.element.querySelectorAll('button')], latest = buttons.at(-1);
  latest.focus();
  const mutations = [];
  const observer = new MutationObserver(records => mutations.push(...records));
  for (const button of buttons) observer.observe(button, {attributes: true, attributeFilter: ['disabled'],
    childList: true, subtree: true, characterData: true});
  for (let cycle = 0; cycle < 3; cycle++) {
    await new Promise(resolve => setTimeout(resolve, 500));
    pane.metadata({...run, elapsed_seconds: cycle});
    await pane.refresh();
  }
  await new Promise(resolve => setTimeout(resolve, 0));
  observer.disconnect();
  const focusRetained = document.activeElement === latest;
  const stable = mutations.length === 0 && observed.length === 3 &&
    observed.every(state => !state.disabled && state.opacity === '1') && focusRetained;
  window.__logToolbarStability = {ok: stable, theme, locale, mutations: mutations.length, observed, focusRetained};
  pane.dispose();
})().catch(error => { window.__logToolbarStability = {ok: false, error: String(error)}; });
true;
