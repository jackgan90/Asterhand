// Only native lifecycle fields are used. Message text and model output cannot
// declare connectivity or clear a failed request. No extra provider request.
export class ConnectionState {
  failed = false;
  event(frame) {
    const event = frame.payload;
    if (frame.event !== 'chat' || !event?.runId || !event.sessionKey) return;
    if (event.state === 'error' || event.state === 'final' && event.message?.stopReason === 'error') this.failed = true;
    else if (event.state === 'final') this.failed = false;
  }
  snapshot(gateway) {
    if (gateway.reconnecting) return {state: 'connecting'};
    return {state: !this.failed && gateway.authenticated && gateway.socket?.readyState === 1 ? 'connected' : 'unavailable'};
  }
}
