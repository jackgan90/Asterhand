import {historyCursor, historyPage} from './projection.mjs';

// OpenClaw offset pages do not carry deltaCursor. Compare two latest-page
// snapshots around an older read, rather than treating that missing field as a
// rewrite. A concurrent append/rewrite still invalidates offset pagination.
export async function readHistoryPage(read, project, offset, cursor) {
  const latest = await read({offset: 0});
  const revision = historyCursor(latest);
  const reset = async history => ({...historyPage(await project(history), historyCursor(history)), reset: true});
  if (!offset) return historyPage(await project(latest), revision);
  if (cursor !== revision) return reset(latest);
  const older = await read({offset});
  if (older.windowReset) return reset(await read({offset: 0}));
  const projected = await project(older);
  const after = await read({offset: 0});
  if (historyCursor(after) !== revision) return reset(after);
  return historyPage(projected, revision);
}
