type PauseAcknowledgement = {requestedAt: string; acknowledgedAt: string};

function wait(milliseconds: number, signal: AbortSignal): Promise<void> {
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {signal.removeEventListener('abort', abort); resolve();}, milliseconds);
    const abort = () => {clearTimeout(timer); reject(signal.reason);};
    signal.addEventListener('abort', abort, {once: true});
  });
}

export async function pauseSourceBeforeRollover(rollover: number, signal: AbortSignal, request: () => void, acknowledged: (requestedAt: string) => boolean): Promise<PauseAcknowledgement> {
  // Leave a full minute for the existing 30-second acknowledgement budget.
  // A separate timer also runs while the scenario awaits handoff or restart.
  const delay = rollover - 60_000 - Date.now();
  if (delay > 0) await wait(delay, signal);
  signal.throwIfAborted();
  const requested = Date.now();
  if (requested >= rollover) throw new Error('source_pause_missed_rollover');
  const requestedAt = new Date(requested).toISOString();
  request();
  const deadline = Math.min(requested + 30_000, rollover);
  for (;;) {
    signal.throwIfAborted();
    if (Date.now() >= rollover) throw new Error('source_pause_missed_rollover');
    if (Date.now() >= deadline) throw new Error('source_pause_acknowledgement_timeout');
    const confirmed = acknowledged(requestedAt);
    const finished = Date.now();
    // The log read itself can block past either deadline. A late observation
    // cannot certify that the worker was quiescent before the boundary.
    if (finished >= rollover) throw new Error('source_pause_missed_rollover');
    if (finished >= deadline) throw new Error('source_pause_acknowledgement_timeout');
    if (confirmed) return {requestedAt, acknowledgedAt: new Date(finished).toISOString()};
    await wait(Math.min(1000, deadline - finished), signal);
  }
}
