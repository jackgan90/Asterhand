
const results = {succeeded:'common.command.succeeded', failed:'common.command.failed', interrupted:'common.command.interrupted'};
const reasons = {
  start_failed:'common.command.start_failed', exit_error:'common.command.exit_error', user_stop:'common.command.user_stop',
  node_shutdown:'common.command.node_shutdown', unexpected_shutdown:'common.command.unexpected_shutdown', timeout:'common.command.timeout_reason',
  log_io_error:'common.command.log_io_error', capture_error:'common.command.capture_error', execution_error:'common.command.execution_error',
  log_drain_timeout:'common.command.log_drain_timeout',
};
export const runBusy = run => Boolean(run?.phase && run.phase !== 'finished');
export const runStoppable = run => runBusy(run) && run.phase !== 'stopping' && run.phase !== 'draining';
export function runKey(run) {
  if (!run?.phase) return '';
  if (run.phase === 'stopping') return 'common.command.stopping';
  if (run.phase !== 'finished') return 'common.command.running';
  return results[run.result] || 'common.unconfirmed';
}
export const reasonKey = run => reasons[run?.reason] || '';
export const modeKey = command => command.execution_mode === 'managed' ? 'common.command.managed' : 'common.command.unmanaged';
export function timeoutSeconds(value, unit) {
  if (String(value).trim() === '') return null;
  const seconds = Number(value) * Number(unit);
  if (!Number.isSafeInteger(seconds) || seconds < 1 || seconds > 9223372036) {
    throw Object.assign(new Error('err.localcmd.invalid_timeout'), {messageKey:'err.localcmd.invalid_timeout'});
  }
  return seconds;
}
