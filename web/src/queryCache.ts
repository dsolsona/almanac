type Entry = {
  controller: AbortController;
  promise: Promise<unknown>;
  users: Set<symbol>;
  settled: boolean;
  expires: number;
  accessed: number;
  epoch: number;
};
const entries = new Map<string, Entry>();
let epoch = 0;
const cancelled = () => new DOMException('The read was cancelled.', 'AbortError');

export function clearQueryCache(abortPending = true): void {
  epoch++;
  if (abortPending) for (const entry of entries.values()) entry.controller.abort();
  entries.clear();
}

/** Share validated reads, while each caller retains its own cancellation. */
export function sharedRead<T>(key: string, load: (signal: AbortSignal) => Promise<T>, signal?: AbortSignal | null, ttlMs = 30_000): Promise<T> {
  if (signal?.aborted) return Promise.reject(cancelled());
  const now = Date.now();
  for (const [storedKey, entry] of entries) {
    if (entry.settled && now - entry.accessed >= 300_000) entries.delete(storedKey);
  }
  let entry = entries.get(key);
  if (entry?.settled && entry.expires <= now) { entries.delete(key); entry = undefined; }
  if (!entry) {
    if (entries.size >= 100) {
      const oldest = [...entries].filter(([, value]) => value.settled).sort((a, b) => a[1].accessed - b[1].accessed)[0];
      if (oldest) entries.delete(oldest[0]);
      else return Promise.reject(new Error('Too many reads are pending. Try again shortly.'));
    }
    const controller = new AbortController();
    const created: Entry = { controller, users: new Set(), settled: false, expires: 0, accessed: now, epoch, promise: Promise.resolve() };
    let pending: Promise<T>;
    try { pending = load(controller.signal); }
    catch (error) { pending = Promise.reject(error); }
    created.promise = pending.then(value => {
      if (created.epoch !== epoch || controller.signal.aborted) throw cancelled();
      created.settled = true;
      created.expires = Date.now() + Math.min(30_000, Math.max(0, ttlMs));
      return value;
    }, error => { if (entries.get(key) === created) entries.delete(key); throw error; });
    entries.set(key, created);
    entry = created;
  }
  entry.accessed = now;
  const current = entry, token = Symbol();
  current.users.add(token);
  return new Promise<T>((resolve, reject) => {
    const release = () => {
      signal?.removeEventListener('abort', abort);
      current.controller.signal.removeEventListener('abort', abort);
      current.users.delete(token);
      if (!current.settled && current.users.size === 0) {
        current.controller.abort();
        if (entries.get(key) === current) entries.delete(key);
      }
    };
    const abort = () => { release(); reject(cancelled()); };
    signal?.addEventListener('abort', abort, { once: true });
    current.controller.signal.addEventListener('abort', abort, { once: true });
    if(signal?.aborted || current.controller.signal.aborted)abort();
    current.promise.then(value => {
      release();
      if (signal?.aborted || current.epoch !== epoch) reject(cancelled());
      else resolve(value as T);
    }, error => { release(); reject(error); });
  });
}

export function waitForReadRetry(signal: AbortSignal | null | undefined, milliseconds: number): Promise<void> {
  if (signal?.aborted) return Promise.reject(cancelled());
  return new Promise((resolve, reject) => {
    const abort = () => { clearTimeout(timer); signal?.removeEventListener('abort', abort); reject(cancelled()); };
    const timer = setTimeout(() => { signal?.removeEventListener('abort', abort); resolve(); }, milliseconds);
    signal?.addEventListener('abort', abort, { once: true });
  });
}
