import { ROOT_CONTEXT, SpanKind, SpanStatusCode, trace, createNoopMeter, type Context, type Span, type MeterProvider, type ObservableCallback } from '@opentelemetry/api';
import { W3CTraceContextPropagator } from '@opentelemetry/core';
import { resourceFromAttributes } from '@opentelemetry/resources';
import { BatchSpanProcessor, ParentBasedSampler, TraceIdRatioBasedSampler, TracerProvider, type SpanExporter, type SpanProcessor } from '@opentelemetry/sdk-trace';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-http';
import { BrowserTraceOutbox } from './outbox.ts';

type Journey = { span: Span; context: Context; key: string; route: string; started: number; content: boolean; chart: boolean; ended: boolean; paintQueued: boolean; paint?: Span; timer: ReturnType<typeof setTimeout> };
type Read = { context: Context; body: Span; timer: ReturnType<typeof setTimeout> };
const responses = new WeakMap<Response, Read>();
const activeReads = new Set<Read>();
const propagator = new W3CTraceContextPropagator();
let current: Journey | undefined;
let provider: TracerProvider | undefined;
let csrf = '';
let authenticated = false;
let awaitingAuthentication = false;
let enabled = true;
let installed = false;
let exportFailures = 0;
let droppedBatches = 0;
let droppedSpans = 0;
let queueSpans = 0;
let queueCapacity = 0;
let observeQueue: ObservableCallback | undefined;
let outbox: BrowserTraceOutbox | undefined;
let replayPending: (() => void) | undefined;
// Observe only the SDK's fixed queue-full counter. No metric labels, arbitrary
// attributes or periodic collection are retained in the browser.
const selfMetrics: MeterProvider = { getMeter() {
  const meter = createNoopMeter();
  const observed = Object.create(meter) as typeof meter;
  observed.createCounter = (name) => {
    return { add(count, attributes) { if (name === 'otel.sdk.processor.span.processed' && attributes?.['error.type'] === 'queue_full') droppedSpans += count; } };
  };
  observed.createUpDownCounter = name => ({ add(value) { if (name === 'otel.sdk.processor.span.queue.capacity') queueCapacity += value; } });
  observed.createObservableUpDownCounter = name => {
    const metric = Object.create(meter.createObservableUpDownCounter(name)) as ReturnType<typeof meter.createObservableUpDownCounter>;
    metric.addCallback = callback => { if (name === 'otel.sdk.processor.span.queue.size') observeQueue = callback; };
    metric.removeCallback = callback => { if (observeQueue === callback) observeQueue = undefined; };
    return metric;
  };
  return observed;
} };
// This driver manually owns propagation and export. Use the same provider for
// creation and flushing; a global provider may belong to another SDK instance.
const tracer = () => provider?.getTracer('almanac-web') ?? trace.getTracer('almanac-web');
const journeyContext = () => current && !current.ended ? current.context : ROOT_CONTEXT;
// Keep every span on the document's monotonic timeline. Mixing SDK wall-clock
// timestamps with navigation timestamps lets a system-clock change poison an
// otherwise valid batch, including unrelated completed journeys.
const traceTime = () => performance.timeOrigin + performance.now();

function exportEvent(spans: readonly { spanContext(): { traceId: string } }[], result: string, error?: unknown) {
  const errorName = error instanceof Error && ['Error', 'TypeError', 'RangeError', 'SyntaxError'].includes(error.name) ? error.name : undefined;
  const frame = error instanceof Error ? error.stack?.match(/\/assets\/([A-Za-z0-9_-]+\.js):(\d{1,6}):(\d{1,8})/) : undefined;
  window.dispatchEvent(new CustomEvent('almanac:telemetry-delivery', { detail: { trace_ids: [...new Set(spans.map(span => span.spanContext().traceId))], result, ...telemetryStats(), authenticated, enabled, error_name: errorName, error_asset: frame?.[1], error_line: frame ? Number(frame[2]) : undefined, error_column: frame ? Number(frame[3]) : undefined } }));
}

export function telemetrySession(value: { csrf_token: string; session: unknown } | null) {
  csrf = value?.session ? value.csrf_token : '';
  authenticated = !!value?.session;
  if (value?.session === null) outbox?.clear();
  if (value?.session && typeof value.session === 'object' && 'user_id' in value.session && typeof value.session.user_id === 'string') {
    outbox?.authenticate(value.session.user_id);
    replayPending?.();
  }
  if (!value) return;
  const restricted = !!value.session && typeof value.session === 'object' && 'restricted' in value.session && value.session.restricted === true;
  if (value.session === null || restricted) {
    awaitingAuthentication = true;
    if (current && !current.ended) endJourney(current, 'cancelled');
  } else if (awaitingAuthentication) {
    // Human sign-in/password entry is outside the authenticated page journey.
    awaitingAuthentication = false;
    if (current && !current.ended) endJourney(current, 'cancelled');
    beginJourney();
  }
}

export function routeName(path: string): string {
  if (path === '/slos') return 'slo.list';
  if (/^\/slos\/[0-9a-f-]{36}$/.test(path)) return 'slo.detail';
  if (path === '/slis') return 'sli.list';
  if (/^\/slis\/[0-9a-f-]{36}$/.test(path)) return 'sli.detail';
  return 'management';
}

function apiRoute(path: string): string {
  if (!/^\/api\/(?:v1|agent\/v1)\//.test(path)) return 'unmatched';
  const parts = path.split('/').filter(Boolean);
  if (parts.length > 8) return 'unmatched';
  const allowed = new Set(['api', 'v1', 'agent', 'auth', 'context', 'login', 'logout', 'password', 'agents', 'providers', 'datasources', 'diagnostics', 'slis', 'slos', 'services', 'overview', 'operational', 'status', 'history', 'values', 'preparation', 'retry', 'evaluation', 'observations', 'configuration', 'connect', 'result']);
  return '/' + parts.map(part => /^[0-9a-f-]{36}$/.test(part) ? '{id}' : allowed.has(part) ? part : '{value}').join('/');
}

export function beginJourney(key = location.pathname + location.search, documentLoad = false, refresh = false) {
  if (!installed || !enabled) return;
  if (!refresh && current && !current.ended && current.key === key) return;
  if (current && !current.ended) endJourney(current, 'cancelled');
  const route = routeName(new URL(key, location.origin).pathname);
  const started = documentLoad ? 0 : performance.now();
  const span = tracer().startSpan('ui.' + route, { startTime: performance.timeOrigin + started, attributes: { 'almanac.journey': route, 'almanac.action': documentLoad ? 'document' : refresh ? 'refresh' : 'navigation' } }, ROOT_CONTEXT);
  const journey: Journey = { span, context: trace.setSpan(ROOT_CONTEXT, span), key, route, started, content: false, chart: false, ended: false, paintQueued: false, timer: setTimeout(() => endJourney(journey, 'timeout'), 30_000) };
  current = journey;
}

function endJourney(journey: Journey, result: 'ready' | 'cancelled' | 'timeout' | 'error') {
  if (journey.ended) return;
  journey.ended = true;
  clearTimeout(journey.timer);
  const completed = performance.now();
  if (journey.paint) {
    journey.paint.setStatus({ code: SpanStatusCode.ERROR, message: 'paint cancelled' });
    journey.paint.end(performance.timeOrigin + completed);
    journey.paint = undefined;
  }
  journey.span.setAttributes({ 'almanac.result': result, 'almanac.duration_ms': completed - journey.started });
  if (result === 'error' || result === 'timeout') journey.span.setStatus({ code: SpanStatusCode.ERROR, message: result });
  const recording = journey.span.isRecording();
  journey.span.end(performance.timeOrigin + completed);
  if (authenticated && enabled) outbox?.persist();
  window.dispatchEvent(new CustomEvent('almanac:journey', { detail: { traceId: journey.span.spanContext().traceId, journey: journey.route, result, recording, duration: completed - journey.started } }));
}

function completeAfterPaint(journey: Journey) {
  if (journey.paintQueued || !journey.content || (journey.route === 'slo.detail' && !journey.chart)) return;
  journey.paintQueued = true;
  if (enabled) journey.paint = tracer().startSpan('ui.fonts.wait', { startTime: traceTime() }, journey.context);
  void (document.fonts?.ready ?? Promise.resolve()).then(() => {
    journey.paint?.end(traceTime());
    journey.paint = undefined;
    if (current !== journey || journey.ended) return;
    requestAnimationFrame(() => requestAnimationFrame(() => {
      if (current === journey) endJourney(journey, 'ready');
    }));
  }, () => endJourney(journey, 'error'));
}

export function journeyContentReady(route: string, error = false) {
  const journey = current;
  if (!journey || journey.route !== route || journey.ended) return;
  if (error) { endJourney(journey, 'error'); return; }
  journey.content = true;
  completeAfterPaint(journey);
}

export function traceChartRender(render: () => void) {
  if (!installed || !enabled) { render(); return; }
  const journey = current;
  const span = tracer().startSpan('ui.chart.render', { startTime: traceTime() }, journeyContext());
  try { render(); } catch (error) { span.setStatus({ code: SpanStatusCode.ERROR, message: 'chart render failed' }); throw error; }
  finally { span.end(traceTime()); }
  if (journey && !journey.ended) {
    journey.chart = true;
    completeAfterPaint(journey);
  }
}

// Existing clients keep their bounded stream readers. Only their final parse is
// wrapped here; no response body, parsed value or exception text is retained.
export function traceJSON(response: Response, text: string): unknown {
  const read = responses.get(response);
  if (read) finishRead(read);
  responses.delete(response);
  if (!installed || !enabled) return JSON.parse(text) as unknown;
  const span = tracer().startSpan('ui.json.parse', { startTime: traceTime() }, read?.context ?? journeyContext());
  try { return JSON.parse(text) as unknown; }
  catch (error) { span.setStatus({ code: SpanStatusCode.ERROR, message: 'invalid JSON' }); throw error; }
  finally { span.end(traceTime()); }
}

function finishRead(read: Read, incomplete = false) {
  if (!activeReads.delete(read)) return;
  clearTimeout(read.timer);
  if (incomplete) read.body.setStatus({ code: SpanStatusCode.ERROR, message: 'response read incomplete' });
  read.body.end(traceTime());
}

export function telemetryStats() {
  void observeQueue?.({ observe(value) { queueSpans = value; } });
  return { exportFailures, droppedBatches, droppedSpans, queueSpans, queueCapacity, outboxDropped: outbox?.dropped ?? 0, outboxFailures: outbox?.failures ?? 0 };
}
export async function flushTelemetry() { await provider?.forceFlush({ timeoutMillis: 2_000 }); }

export function installTelemetry() {
  if (installed || typeof window === 'undefined') return;
  const originalFetch = globalThis.fetch;
  const originalPush = history.pushState, originalReplace = history.replaceState;
  const upstream = originalFetch.bind(globalThis);
  let storage: Storage | undefined;
  try { storage = window.sessionStorage; } catch { /* SDK export remains available without browser storage. */ }
  // Expiry uses captured wall time so suspended tabs do not extend retention.
  // Trace timestamps/durations remain on the document's monotonic timeline.
  outbox = new BrowserTraceOutbox(storage);
  replayPending = () => {
    if (!authenticated || !enabled || !csrf) return;
    void outbox?.replay(async body => {
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), 2_000);
      try {
        const response = await upstream('/api/v1/telemetry/traces', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf }, body, credentials: 'same-origin', mode: 'same-origin', redirect: 'error', keepalive: true, signal: controller.signal });
        void response.body?.cancel().catch(() => undefined);
        return response.status === 202;
      } finally { clearTimeout(timeout); }
    });
  };
  const removeListeners: (() => void)[] = [];
  const listen = (target: EventTarget, type: string, listener: EventListener, capture = false) => {
    target.addEventListener(type, listener, capture);
    removeListeners.push(() => target.removeEventListener(type, listener, capture));
  };
  // forceFlush exports up to queue/batch = 512/64 batches concurrently. Bound
  // transport concurrency to that same finite maximum instead of rejecting
  // sibling batches from the SDK's own flush operation.
  const exporter = new OTLPTraceExporter({ url: new URL('/api/v1/telemetry/traces', location.origin).href, headers: async () => ({ 'X-CSRF-Token': csrf }), concurrencyLimit: 8, timeoutMillis: 2_000 });
  const guarded: SpanExporter = {
    export(spans, callback) {
      if (!authenticated || !csrf || !enabled) { droppedBatches++; exportEvent(spans, !enabled ? 'disabled' : 'unauthenticated'); callback({ code: 0 }); return; }
      try {
        exporter.export(spans, result => { if (result.code !== 0) exportFailures++; else outbox?.acknowledge(spans); exportEvent(spans, result.code === 0 ? 'exported' : 'failed', result.error); callback(result); });
      } catch (error) {
        // Serialization may throw before the SDK invokes its result callback.
        // Always settle the processor, count the loss and retain only a fixed
        // diagnostic classification, never arbitrary exception text.
        exportFailures++;
        exportEvent(spans, 'serialization_failed', error);
        callback({ code: 1, error: new Error('Trace serialization failed') });
      }
    },
    shutdown: () => exporter.shutdown(),
  };
  const configuredRatio = Number(import.meta.env.VITE_OTEL_TRACES_SAMPLER_ARG ?? '1');
  const ratio = Number.isFinite(configuredRatio) && configuredRatio >= 0 && configuredRatio <= 1 ? configuredRatio : 0;
  queueSpans = 0;
  queueCapacity = 0;
  const batch = new BatchSpanProcessor({ exporter: guarded, selfObsMeterProvider: selfMetrics, maxQueueSize: 512, maxExportBatchSize: 64, scheduledDelayMillis: 1_000, exportTimeoutMillis: 2_000 });
  const observed: SpanProcessor = {
    onStart: (span, parent) => batch.onStart(span, parent),
    onEnd(span) {
      if (enabled) outbox?.record(span);
      const previousDrops = droppedSpans;
      batch.onEnd(span);
      if (/^ui\.(?:sli|slo)\.(?:list|detail)$/.test(span.name)) exportEvent([span], (span.spanContext().traceFlags & 1) === 0 ? 'unsampled' : droppedSpans > previousDrops ? 'queue_full' : 'queued');
    },
    forceFlush: async () => {
      await batch.forceFlush();
      // Automatic batches may already be exporting outside the processor queue.
      await exporter.forceFlush();
    },
    shutdown: () => batch.shutdown(),
  };
  provider = new TracerProvider({ resource: resourceFromAttributes({ 'service.name': 'almanac-web', 'service.version': new URL(import.meta.url).pathname.split('/').pop() ?? 'unknown', 'deployment.environment.name': import.meta.env.MODE }), sampler: new ParentBasedSampler({ root: new TraceIdRatioBasedSampler(ratio) }), spanLimits: { attributeCountLimit: 16, attributeValueLengthLimit: 128, eventCountLimit: 0, linkCountLimit: 0 }, spanProcessors: [observed] });
  installed = true;
  beginJourney(location.pathname + location.search, true);
  globalThis.fetch = async (input, init) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, location.href);
    if (url.origin !== location.origin || !url.pathname.startsWith('/api/') || url.pathname.startsWith('/api/v1/telemetry/')) return upstream(input, init);
    const observeCapability = (response: Response) => {
      const capability = response.headers.get('X-Almanac-Tracing');
      // Observe explicit changes while capture is off, too. Missing metadata
      // on proxy/error responses does not change the last backend decision.
      if (capability === '0' || capability === '1') {
        enabled = capability === '1';
        if (!enabled) outbox?.clear();
      }
      return response;
    };
    if (!enabled) return observeCapability(await upstream(input, init));
    const method = init?.method ?? (input instanceof Request ? input.method : 'GET');
    const parent = journeyContext();
    const span = tracer().startSpan('http.' + method + ' ' + apiRoute(url.pathname), { startTime: traceTime(), kind: SpanKind.CLIENT, attributes: { 'http.request.method': method, 'http.route': apiRoute(url.pathname) } }, parent);
    const ctx = trace.setSpan(parent, span);
    const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined));
    propagator.inject(ctx, headers, { set: (carrier, key, value) => carrier.set(key, value) });
    try {
      const response = observeCapability(await upstream(input, { ...init, headers }));
      span.setAttribute('http.response.status_code', response.status);
      if (response.status >= 500) span.setStatus({ code: SpanStatusCode.ERROR, message: 'server error' });
      if (enabled && response.body) {
        const body = tracer().startSpan('http.response.read', { startTime: traceTime() }, ctx);
        const read: Read = { context: ctx, body, timer: setTimeout(() => finishRead(read, true), 30_000) };
        if (activeReads.size >= 128) { const oldest = activeReads.values().next().value; if (oldest) finishRead(oldest, true); }
        activeReads.add(read);
        responses.set(response, read);
        // Malformed/cancelled bodies cannot retain an unfinished span forever.
      }
      return response;
    } catch (error) {
      span.setStatus({ code: SpanStatusCode.ERROR, message: 'request failed or cancelled' });
      throw error;
    } finally { span.end(traceTime()); }
  };
  const navigate = (value: string | URL | null | undefined) => { if (value != null) { const url = new URL(value, location.href); if (url.origin === location.origin) beginJourney(url.pathname + url.search); } };
  const push = history.pushState.bind(history), replace = history.replaceState.bind(history);
  history.pushState = (data, unused, url) => { navigate(url); push(data, unused, url); };
  history.replaceState = (data, unused, url) => { navigate(url); replace(data, unused, url); };
  listen(window, 'popstate', () => beginJourney());
  listen(document, 'click', event => { const anchor = event.target instanceof Element ? event.target.closest<HTMLAnchorElement>('a[data-app-route]') : null; if (anchor && !event.defaultPrevented) navigate(anchor.href); }, true);
  listen(window, 'pagehide', () => { if (current && !current.ended) endJourney(current, 'cancelled'); for (const read of activeReads) finishRead(read, true); if (authenticated && enabled) outbox?.persist(); });
  listen(window, 'almanac:telemetry-flush', () => {
    void flushTelemetry().then(() => window.dispatchEvent(new CustomEvent('almanac:telemetry-flushed', { detail: { ...telemetryStats(), result: 'ready' } })), () => window.dispatchEvent(new CustomEvent('almanac:telemetry-flushed', { detail: { ...telemetryStats(), result: 'error' } })));
  });
  return async () => {
    for (const remove of removeListeners) remove();
    globalThis.fetch = originalFetch;
    history.pushState = originalPush;
    history.replaceState = originalReplace;
    if (current && !current.ended) endJourney(current, 'cancelled');
    for (const read of activeReads) finishRead(read, true);
    await provider?.shutdown();
    outbox = undefined;
    replayPending = undefined;
    provider = undefined;
    current = undefined;
    authenticated = false;
    awaitingAuthentication = false;
    csrf = '';
    installed = false;
    enabled = true;
  };
}

if (import.meta.env.MODE !== 'test') installTelemetry();
