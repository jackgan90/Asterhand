// A session is an immutable identity with a revocable active flag. Reads and
// callbacks check it after awaits; stopping subscriptions never cancels a job.
export function appLifecycle({client, initialize, refreshPeers, refreshTasks, connection, stopped, failed, clock = globalThis}) {
  let current, source, peerTimer;
  function stop() {
    if (current) current.active = false;
    source?.close();
    source = undefined;
    clock.clearInterval(peerTimer);
    peerTimer = undefined;
    stopped();
  }
  async function run(session, fn) {
    if (!session.active) return;
    try { await fn(session); }
    catch (error) { if (session.active) failed(error); }
  }
  async function start(token) {
    stop();
    const session = current = {active:true};
    await run(session, async () => {
      await initialize(session, token);
      if (!session.active) return;
      source = client.stream();
      source.addEventListener("refresh", () => run(session, refreshTasks));
      source.onerror = () => { if (session.active) connection(false); };
      source.onopen = () => { if (session.active) connection(true); };
      peerTimer = clock.setInterval(() => run(session, refreshPeers), 20000);
    });
  }
  return {start, stop, session:() => current};
}
