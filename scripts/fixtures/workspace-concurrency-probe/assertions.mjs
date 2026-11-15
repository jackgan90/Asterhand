function toolResult(messages, id) {
  const matches = (messages ?? []).filter(message => message.role === 'tool' && message.tool_call_id === id);
  if (matches.length !== 1) return null;
  try {
    const result = JSON.parse(matches[0].content);
    return result && typeof result === 'object' && !result.error && !result.isError ? result : null;
  } catch { return null; }
}

export function memoryResults(messages) {
  const search = toolResult(messages, 'memorycall0');
  const get = toolResult(messages, 'memorycall1');
  return {
    search: Array.isArray(search?.results) && search.results.some(result =>
      result.path === 'memory/facts.md' && typeof result.snippet === 'string' && result.snippet.includes('celesta')),
    get: get?.status === 'ok' && get.path === 'memory/facts.md' && typeof get.text === 'string' && get.text.includes('celesta'),
  };
}

export function ownedExecutionIdentity(trace) {
  const events = trace.filter(event => event.scenario === 'batch' && event.origin === 'owned_execute' &&
    event.path === 'owned.txt' && ['io_enter', 'io_exit'].includes(event.event));
  return events.length === 2 && events[0].event === 'io_enter' && events[1].event === 'io_exit' &&
    events[0].sequence < events[1].sequence && events.every(event =>
      event.ambient?.id === 'batchcall3' && event.ambient.source === 'owned_execute');
}
