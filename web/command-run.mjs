
const results = {succeeded:'common.command.succeeded', failed:'common.command.failed', interrupted:'common.command.interrupted'};
const reasons = {
  start_failed:'common.command.start_failed', exit_error:'common.command.exit_error', user_stop:'common.command.user_stop',
  node_shutdown:'common.command.node_shutdown', unexpected_shutdown:'common.command.unexpected_shutdown', timeout:'common.command.timeout_reason',
  log_io_error:'common.command.log_io_error', capture_error:'common.command.capture_error', execution_error:'common.command.execution_error',
  log_drain_timeout:'common.command.log_drain_timeout',
  log_limit:'common.command.log_limit',
  node_failed:'common.graph.node_failed',graph_aborted:'common.graph.aborted',
};
export const runBusy = run => run?.active_count !== undefined ? run.active_count > 0 : Boolean(run?.phase && run.phase !== 'finished');
export const runStoppable = run => run?.active_count !== undefined ? run.active_count > 0 : runBusy(run) && run.phase !== 'stopping' && run.phase !== 'draining';
export function runKey(run) {
  if (!run?.phase) return '';
  if (run.phase === 'stopping') return run.reason==='node_failed'?'common.graph.failed_stopping':'common.command.stopping';
  if (run.phase !== 'finished') return 'common.command.running';
  return results[run.result] || 'common.unconfirmed';
}
export const reasonKey = run => reasons[run?.reason] || '';
// Visual tone shared by status icons: running, succeeded, failed, interrupted or unknown.
export function runTone(run) {
  if (!run?.phase && !(run?.active_count > 0)) return '';
  if (runBusy(run)) return 'running';
  if (run.failed_count > 0) return 'failed';
  if (run.interrupted_count > 0) return 'interrupted';
  return results[run.result] ? run.result : 'unknown';
}
export const canLaunch = command => Boolean(command) && (!runBusy(command) || command.allow_concurrent === true) && (command.active_count ?? 0) < 8;
export function summaryParts(run,t) {
  if(!run)return [];
  const parts=[];
  if(run.active_count>0)parts.push(t('common.command.active_count',{count:run.active_count}));
  else if(run.phase)parts.push(t(runKey(run)));
  if(run.failed_count>0)parts.push(t('common.command.failed_count',{count:run.failed_count}));
  if(run.interrupted_count>0)parts.push(t('common.command.interrupted_count',{count:run.interrupted_count}));
  return parts;
}
export function timeoutSeconds(value, unit) {
  if (String(value).trim() === '') return null;
  const seconds = Number(value) * Number(unit);
  if (!Number.isSafeInteger(seconds) || seconds < 1 || seconds > 9223372036) {
    throw Object.assign(new Error('err.localcmd.invalid_timeout'), {messageKey:'err.localcmd.invalid_timeout'});
  }
  return seconds;
}
