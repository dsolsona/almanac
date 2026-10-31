import { afterEach, expect, it, vi } from 'vitest';
import { TracerProvider, SimpleSpanProcessor, type ReadableSpan } from '@opentelemetry/sdk-trace';
import { trace } from '@opentelemetry/api';
import { resourceFromAttributes } from '@opentelemetry/resources';
import { BrowserTraceOutbox } from '../src/telemetry/outbox.ts';
import { installTelemetry, telemetrySession, journeyContentReady, traceJSON, flushTelemetry } from '../src/telemetry/client.ts';

const exported = vi.hoisted(() => ({ bodies: [] as string[], failures: [] as unknown[] }));
vi.mock('@opentelemetry/exporter-trace-otlp-http', async () => {
  const { JsonTraceSerializer } = await import('@opentelemetry/otlp-transformer');
  return { OTLPTraceExporter: class {
    export(spans: ReadableSpan[], callback: (result: { code: number }) => void) {
      try {
        const body = JsonTraceSerializer.serializeRequest(spans);
        exported.bodies.push(new TextDecoder().decode(body));
        callback({ code: 0 });
      } catch (error) { exported.failures.push(error); throw error; }
    }
    async shutdown() {}
  } };
});

let stop: (() => Promise<void>) | undefined;
async function expectReadyJourney(action: () => void) {
  let ready = false;
  const listener = (event: Event) => { if ((event as CustomEvent).detail?.result === 'ready') ready = true; };
  window.addEventListener('almanac:journey', listener);
  try {
    action();
    await vi.waitFor(() => expect(ready).toBe(true));
  } finally { window.removeEventListener('almanac:journey', listener); }
}
afterEach(async () => {
  await stop?.(); stop = undefined;
  exported.bodies.length = 0; exported.failures.length = 0;
  vi.unstubAllGlobals();
  trace.disable();
  sessionStorage.clear();
  history.replaceState({}, '', '/');
});

it('does not replay another user\'s pending telemetry or expired batches', async () => {
  const key = 'almanac.telemetry.pending.v1', now = 100_000;
  sessionStorage.setItem(key, JSON.stringify({ owner: 'owner-a', batches: [{ body: '{}', expires: now + 1000, count: 1 }] }));
  const changed = new BrowserTraceOutbox(sessionStorage, () => now);
  changed.authenticate('owner-b');
  const send = vi.fn().mockResolvedValue(true);
  await changed.replay(send);
  expect(send).not.toHaveBeenCalled();
  expect(sessionStorage.getItem(key)).toBeNull();
  sessionStorage.setItem(key, JSON.stringify({ owner: 'owner-a', batches: [{ body: '{}', expires: now - 1, count: 2 }] }));
  const expired = new BrowserTraceOutbox(sessionStorage, () => now);
  expired.authenticate('owner-a');
  await expired.replay(send);
  expired.persist();
  expect(send).not.toHaveBeenCalled();
  expect(expired.dropped).toBe(2);
  expect(sessionStorage.getItem(key)).toBeNull();
});

it('bounds retained SDK spans, batch size and total session storage', async () => {
  const outbox = new BrowserTraceOutbox(sessionStorage);
  outbox.authenticate('owner-a');
  const provider = new TracerProvider({ resource: resourceFromAttributes({ 'service.name': 'almanac-web' }), spanProcessors: [new SimpleSpanProcessor({ exporter: {
    export(spans, callback) { for (const span of spans) outbox.record(span); callback({ code: 0 }); }, async shutdown() {},
  } })] });
  const attributes = Object.fromEntries(Array.from({ length: 16 }, (_, index) => [`bounded_${index}`, 'x'.repeat(128)]));
  for (let index = 0; index < 130; index++) provider.getTracer('fixture').startSpan('ui.sli.list', { attributes }).end();
  outbox.persist();
  const encoded = sessionStorage.getItem('almanac.telemetry.pending.v1')!;
  const stored = JSON.parse(encoded) as { batches: { body: string; count: number }[] };
  expect(new TextEncoder().encode(encoded).byteLength).toBeLessThanOrEqual(128 * 1024);
  expect(stored.batches.reduce((sum, batch) => sum + batch.count, 0)).toBeLessThanOrEqual(128);
  expect(stored.batches.every(batch => new TextEncoder().encode(batch.body).byteLength <= 48 * 1024 && batch.count <= 64)).toBe(true);
  expect(outbox.dropped).toBeGreaterThan(2);
  await provider.shutdown();
});

it('shares the 128-span retention limit between restored and newly completed spans', async () => {
  let outbox = new BrowserTraceOutbox(sessionStorage);
  outbox.authenticate('owner-a');
  const provider = new TracerProvider({ spanProcessors: [new SimpleSpanProcessor({ exporter: {
    export(spans, callback) { for (const span of spans) outbox.record(span); callback({ code: 0 }); }, async shutdown() {},
  } })] });
  for (let index = 0; index < 128; index++) provider.getTracer('fixture').startSpan('ui.sli.list').end();
  outbox.persist();
  outbox = new BrowserTraceOutbox(sessionStorage);
  outbox.authenticate('owner-a');
  for (let index = 0; index < 128; index++) provider.getTracer('fixture').startSpan('ui.sli.list').end();
  outbox.persist();
  const stored = JSON.parse(sessionStorage.getItem('almanac.telemetry.pending.v1')!) as { batches: { count: number }[] };
  expect(stored.batches.reduce((sum, batch) => sum + batch.count, 0)).toBeLessThanOrEqual(128);
  expect(outbox.dropped).toBe(128);
  await provider.shutdown();
});

it('exports through its owned provider when an existing global provider is registered', async () => {
  const foreign = new TracerProvider();
  trace.setGlobalTracerProvider(foreign);
  history.replaceState({}, '', '/slis');
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => setTimeout(() => callback(performance.now()), 0));
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('{}', { headers: { 'X-Almanac-Tracing': '1' } })));
  stop = installTelemetry();
  telemetrySession({ csrf_token: 'fixture-csrf', session: {} });
  await expectReadyJourney(() => journeyContentReady('sli.list'));
  await flushTelemetry();
  expect(exported.failures).toEqual([]);
  expect(exported.bodies.some(body => body.includes('ui.sli.list'))).toBe(true);
  await foreign.shutdown();
});

it('keeps the rendered journey open until document fonts are ready', async () => {
  const original = Object.getOwnPropertyDescriptor(document, 'fonts');
  let ready!: () => void;
  const fonts = new Promise<void>(resolve => { ready = resolve; });
  Object.defineProperty(document, 'fonts', { configurable: true, value: { ready: fonts } });
  try {
    history.replaceState({}, '', '/slis');
    vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => setTimeout(() => callback(performance.now()), 0));
    vi.stubGlobal('fetch', vi.fn());
    stop = installTelemetry();
    telemetrySession({ csrf_token: 'fixture-csrf', session: {} });
    journeyContentReady('sli.list');
    await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())));
    await flushTelemetry();
    expect(exported.bodies.some(body => body.includes('ui.sli.list'))).toBe(false);
    await expectReadyJourney(ready);
    await flushTelemetry();
    expect(exported.bodies.some(body => body.includes('ui.sli.list'))).toBe(true);
  } finally {
    ready();
    if (original) Object.defineProperty(document, 'fonts', original);
    else Reflect.deleteProperty(document, 'fonts');
  }
});

it('exports a completed list root and its fetch/parse spans after a response without capability metadata', async () => {
  history.replaceState({}, '', '/slis');
  vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => setTimeout(() => callback(performance.now()), 0));
  vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(new Response('{}', { status: 200, headers: { 'X-Almanac-Tracing': '1' } }))
    .mockResolvedValueOnce(new Response('{}', { status: 503 })));
  stop = installTelemetry();
  telemetrySession({ csrf_token: 'fixture-csrf', session: {} });
  const response = await fetch('/api/v1/slis');
  traceJSON(response, await response.text());
  const failed = await fetch('/api/v1/datasources');
  traceJSON(failed, await failed.text());
  await expectReadyJourney(() => journeyContentReady('sli.list'));
  await flushTelemetry();
  expect(exported.failures).toEqual([]);
  const spans = exported.bodies.flatMap(body => JSON.parse(body).resourceSpans.flatMap((resource: { scopeSpans: { spans: { name: string; traceId: string }[] }[] }) => resource.scopeSpans.flatMap(scope => scope.spans)));
  expect(spans.map(span => span.name)).toContain('ui.sli.list');
  expect(spans.filter(span => span.name === 'http.GET /api/v1/slis')).toHaveLength(1);
  expect(spans.filter(span => span.name === 'ui.json.parse')).toHaveLength(2);
  expect(new Set(spans.map(span => span.traceId)).size).toBe(1);
});
