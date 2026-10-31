import type { ReadableSpan } from '@opentelemetry/sdk-trace';
import { JsonTraceSerializer } from '@opentelemetry/otlp-transformer';

const key = 'almanac.telemetry.pending.v1';
const ttl = 5 * 60_000;
const byteLimit = 128 * 1024;
const batchLimit = 48 * 1024;
type Pending = { body: string; expires: number; count: number };
type Record = { span: ReadableSpan; expires: number };
const bytes = (value: string) => new TextEncoder().encode(value).byteLength;

// Only owned, fixed-attribute spans enter this buffer. Credentials never do.
// Retention belongs to the browser tab, is bounded in time and bytes, and does
// not turn page navigation into a wait for telemetry transport.
export class BrowserTraceOutbox {
  private records = new Map<string, Record>();
  private previous: Pending[] = [];
  private owner = '';
  private restoredOwner = '';
  private replaying: Promise<void> | undefined;
  dropped = 0;
  failures = 0;
  constructor(private readonly storage: Storage | undefined, private readonly now = Date.now) { if (!storage) this.failures++; }

  record(span: ReadableSpan) {
    if (this.records.has(span.spanContext().spanId)) return;
    while (this.previous.length && this.previous.reduce((sum, batch) => sum + batch.count, this.records.size) >= 128) this.dropped += this.previous.shift()!.count;
    if (this.records.size >= 128) {
      const oldest = this.records.keys().next().value;
      if (oldest) { this.records.delete(oldest); this.dropped++; }
    }
    this.records.set(span.spanContext().spanId, { span, expires: this.now() + ttl });
  }

  acknowledge(spans: ReadableSpan[]) {
    for (const span of spans) this.records.delete(span.spanContext().spanId);
    this.persist();
  }

  clear() {
    this.records.clear(); this.previous = []; this.owner = ''; this.restoredOwner = '';
    try { this.storage?.removeItem(key); } catch { this.failures++; }
  }

  authenticate(owner: string) {
    if (this.owner && owner !== this.owner) this.clear();
    this.owner = owner;
    if (!owner || this.restoredOwner === owner) return;
    this.restoredOwner = owner;
    try {
      const encoded = this.storage?.getItem(key);
      if (!encoded) return;
      if (bytes(encoded) > byteLimit) { this.clear(); this.owner = owner; return; }
      const stored = JSON.parse(encoded) as { owner?: unknown; batches?: unknown };
      if (stored.owner !== owner || !Array.isArray(stored.batches) || stored.batches.length > 128) { this.clear(); this.owner = owner; return; }
      this.previous = stored.batches.filter((value): value is Pending => {
        if (!value || typeof value !== 'object') return false;
        const entry = value as Partial<Pending>;
        if (typeof entry.expires === 'number' && entry.expires <= this.now() && typeof entry.count === 'number' && Number.isInteger(entry.count) && entry.count > 0 && entry.count <= 64) { this.dropped += entry.count; return false; }
        if (!(typeof entry.body === 'string' && bytes(entry.body) <= batchLimit && typeof entry.expires === 'number' && entry.expires > this.now() && entry.expires <= this.now() + ttl && typeof entry.count === 'number' && Number.isInteger(entry.count) && entry.count > 0 && entry.count <= 64)) return false;
        try {
          const request = JSON.parse(entry.body);
          const resources = request?.resourceSpans;
          const scopes = Array.isArray(resources) && resources.length === 1 ? resources[0]?.scopeSpans : undefined;
          const spans = Array.isArray(scopes) && scopes.length === 1 ? scopes[0]?.spans : undefined;
          if (Array.isArray(spans) && spans.length === entry.count) return true;
        } catch { /* Invalid tab storage is never replayed. */ }
        this.dropped += entry.count;
        return false;
      });
      while (this.previous.reduce((sum, batch) => sum + batch.count, this.records.size) > 128 && this.previous.length) this.dropped += this.previous.shift()!.count;
    } catch { this.failures++; this.previous = []; }
  }

  persist() {
    if (!this.owner || !this.storage) return;
    try {
      const now = this.now();
      this.previous = this.previous.filter(value => { if (value.expires > now) return true; this.dropped += value.count; return false; });
      for (const [id, value] of this.records) if (value.expires <= now) { this.records.delete(id); this.dropped++; }
      const values = [...this.records.values()];
      const current: { pending: Pending; ids: string[] }[] = [];
      const encode = (records: Record[]) => {
        const body = new TextDecoder().decode(JsonTraceSerializer.serializeRequest(records.map(value => value.span)));
        if (bytes(body) > batchLimit) {
          if (records.length === 1) { this.records.delete(records[0]!.span.spanContext().spanId); this.dropped++; return; }
          const middle = Math.floor(records.length / 2);
          encode(records.slice(0, middle)); encode(records.slice(middle)); return;
        }
        current.push({ pending: { body, expires: Math.min(...records.map(value => value.expires)), count: records.length }, ids: records.map(value => value.span.spanContext().spanId) });
      };
      for (let index = 0; index < values.length; index += 32) encode(values.slice(index, index + 32));
      const encodeStored = () => JSON.stringify({ owner: this.owner, batches: [...this.previous, ...current.map(value => value.pending)] });
      let encoded = encodeStored();
      const spanCount = () => this.previous.reduce((sum, value) => sum + value.count, 0) + current.reduce((sum, value) => sum + value.ids.length, 0);
      while ((bytes(encoded) > byteLimit || spanCount() > 128) && (this.previous.length || current.length)) {
        if (this.previous.length) this.dropped += this.previous.shift()!.count;
        else {
          const removed = current.shift()!;
          for (const id of removed.ids) this.records.delete(id);
          this.dropped += removed.ids.length;
        }
        encoded = encodeStored();
      }
      if (!this.previous.length && !current.length) this.storage.removeItem(key);
      else this.storage.setItem(key, encoded);
    } catch { this.failures++; }
  }

  replay(send: (body: string) => Promise<boolean>) {
    if (this.replaying) return this.replaying;
    const owner = this.owner;
    this.replaying = (async () => {
      for (const value of [...this.previous]) {
        if (owner !== this.owner || !owner) return;
        if (value.expires <= this.now()) { this.previous = this.previous.filter(item => item !== value); this.dropped += value.count; continue; }
        try {
          if (!await send(value.body)) { this.failures++; return; }
          this.previous = this.previous.filter(item => item !== value);
          this.persist();
        } catch { this.failures++; return; }
      }
    })().finally(() => { this.replaying = undefined; });
    return this.replaying;
  }
}
