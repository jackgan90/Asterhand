// Bounded observation of the optional plugin through the existing private Gateway.
// Never reuse an earlier success after a failed or mismatched current query.
export class PromptMonitor {
  constructor(application, instance, readStatus) {
    this.application = application ?? {mode: 'unsupported', state: 'degraded', reason: 'unsupported'};
    this.instance = instance;
    this.readStatus = readStatus;
    this.runs = new Map();
  }

  begin(run, session) {
    if (this.runs.has(run)) return;
    if (this.runs.size >= 16) this.runs.delete(this.runs.keys().next().value);
    this.runs.set(run, session);
  }

  async status(run, session, terminal = false) {
    if (!this.runs.has(run) || this.runs.get(run) !== session) return null;
    let application = {...this.application};
    try {
      const record = await this.readStatus({instance: this.instance, run, session});
      const observed = record?.application;
      if (record?.instance === this.instance && record.run === run && record.session === session &&
          observed?.revision === this.application.revision && observed.mode === this.application.mode &&
          ['applied', 'degraded'].includes(observed.state)) {
        application = {...this.application, state: observed.state,
          ...(observed.state === 'degraded' ? {reason: 'unconfirmed'} : {})};
      }
    } catch { /* Optional status lookup never affects model execution. */ }
    if (terminal && application.state === 'pending') application = {...application, state: 'degraded', reason: 'unconfirmed'};
    return application;
  }
}
