import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { randomBytes } from 'node:crypto';

const root = resolve(import.meta.dirname, '../..');
mkdirSync(join(root, '.local'), { recursive: true });
const artifacts = mkdtempSync(join(root, '.local/providers-check-'));
const project = `almanac-providers-${process.pid}-${randomBytes(5).toString('hex')}`;
const env = { ...process.env, COMPOSE_PROJECT_NAME: project,
  ALMANAC_METRICS_PORT: '0', ALMANAC_TRACES_PORT: '0' };
delete env.COMPOSE_FILE;
let activeChild;
let cancellation;
let cleaning = false;
let cleanupPromise;
let cleanupFailed = false;
let failedStage = 'startup';
let lastCommand;

function assertActive() {
  if (cancellation) throw Object.assign(new Error('cancelled'), { code: 'CANCELLED' });
}

function run(executable, args, { timeout = 30_000, allowCancel = false, input } = {}) {
  if (!allowCancel) assertActive();
  return new Promise((resolveCommand, rejectCommand) => {
    const child = spawn(executable, args, { cwd: root, env, detached: process.platform !== 'win32',
      stdio: ['pipe', 'pipe', 'pipe'] });
    activeChild = child;
    let stdout = '';
    let stderr = '';
    let timedOut = false;
    const stop = (signal) => {
      try {
        if (process.platform === 'win32') child.kill(signal);
        else process.kill(-child.pid, signal);
      } catch { /* The process may have exited already. */ }
    };
    const timer = setTimeout(() => {
      timedOut = true;
      stop('SIGTERM');
      const killTimer = setTimeout(() => stop('SIGKILL'), 3000);
      killTimer.unref();
    }, timeout);
    child.stdout.on('data', (chunk) => { stdout = (stdout + chunk.toString()).slice(-64 * 1024); });
    child.stderr.on('data', (chunk) => { stderr = (stderr + chunk.toString()).slice(-64 * 1024); });
    child.stdin.on('error', () => {});
    child.once('error', (error) => {
      clearTimeout(timer);
      if (activeChild === child) activeChild = undefined;
      lastCommand = { executable, args, error: error.code, stdout, stderr };
      rejectCommand(error);
    });
    child.once('close', (code, signal) => {
      clearTimeout(timer);
      if (activeChild === child) activeChild = undefined;
      if (cancellation && !allowCancel) return rejectCommand(Object.assign(new Error('cancelled'), { code: 'CANCELLED' }));
      if (timedOut || code !== 0) {
        const error = Object.assign(new Error(`${executable} failed`), { status: code, signal,
          code: timedOut ? 'TIMEOUT' : undefined });
        lastCommand = { executable, args, status: code, signal, timeout: timedOut, stdout, stderr };
        return rejectCommand(error);
      }
      resolveCommand(stdout);
    });
    child.stdin.end(input);
  });
}

function compose(args, options) {
  return run('docker', ['compose', '-f', 'compose.yaml', '-p', project, ...args], options);
}

async function request(url, options = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 5000);
  try {
    const response = await fetch(url, { ...options, signal: controller.signal });
    const text = await response.text();
    if (!response.ok) throw new Error(`HTTP ${response.status} from ${new URL(url).pathname}: ${text.slice(0, 1000)}`);
    return text;
  } finally { clearTimeout(timer); }
}

function assignedPort(text) {
  const match = text.trim().match(/^127\.0\.0\.1:([0-9]+)$/);
  assert.ok(match, `Could not parse Docker assigned loopback port: ${text.trim()}`);
  const port = Number(match[1]);
  assert.ok(port > 0 && port <= 65535);
  return port;
}

async function waitFor(label, fn, timeout = 45_000) {
  const until = Date.now() + timeout;
  let lastError;
  while (Date.now() < until) {
    assertActive();
    try { return await fn(); } catch (error) { lastError = error; }
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 500));
  }
  throw new Error(`${label} did not become ready within ${timeout}ms: ${lastError?.message ?? 'no response'}`);
}

function cleanup() {
  if (cleanupPromise) return cleanupPromise;
  cleaning = true;
  cleanupPromise = (async () => {
    if (activeChild) {
      const child = activeChild;
      try { if (process.platform === 'win32') child.kill('SIGTERM'); else process.kill(-child.pid, 'SIGTERM'); } catch { /* Already exited. */ }
      let closed = false;
      await Promise.race([new Promise((resolveClose) => child.once('close', () => { closed = true; resolveClose(); })),
        new Promise((resolveTimeout) => setTimeout(resolveTimeout, 5000))]);
      if (!closed) {
        try { if (process.platform === 'win32') child.kill('SIGKILL'); else process.kill(-child.pid, 'SIGKILL'); } catch { /* Already exited. */ }
        await Promise.race([new Promise((resolveClose) => child.once('close', resolveClose)),
          new Promise((resolveTimeout) => setTimeout(resolveTimeout, 2000))]);
      }
    }
    try {
      await compose(['down', '--volumes', '--remove-orphans', '--timeout', '20'], { timeout: 35_000, allowCancel: true });
      writeFileSync(join(artifacts, 'cleanup.json'), JSON.stringify({ project, containersNetworksAndVolumes: 'removed' }, null, 2), { mode: 0o600 });
    } catch (error) {
      cleanupFailed = true;
      writeFileSync(join(artifacts, 'cleanup.json'), JSON.stringify({ project, cleanup: 'failed', error: error.message }, null, 2), { mode: 0o600 });
      console.error(`Could not clean owned Compose project ${project}; inspect it before rerunning.`);
    }
  })();
  return cleanupPromise;
}

function onSignal(signal) {
  if (cancellation) return;
  cancellation = signal;
  if (!cleaning && activeChild) {
    try { if (process.platform === 'win32') activeChild.kill('SIGTERM'); else process.kill(-activeChild.pid, 'SIGTERM'); } catch { /* Already exited. */ }
  }
  void cleanup();
}
process.on('SIGINT', () => onSignal('SIGINT'));
process.on('SIGTERM', () => onSignal('SIGTERM'));

try {
  failedStage = 'pull pinned provider images';
  await compose(['pull', 'victoriametrics', 'victoriatraces', 'provider-ready'], { timeout: 300_000 });
  failedStage = 'start isolated provider services';
  // Keep downloads separate. The metrics health window can take 5 + 20*(3+3)
  // seconds including probe timeouts; leave bounded Docker orchestration headroom.
  await compose(['up', '-d', '--pull', 'never', 'provider-ready'], { timeout: 150_000 });
  await waitFor('provider-ready completion', async () => {
    const rows = (await compose(['ps', '-a', '--format', 'json'])).trim().split('\n').filter(Boolean).map((line) => JSON.parse(line));
    const ready = rows.find((row) => row.Service === 'provider-ready');
    assert.ok(ready, 'provider-ready container was not listed by Compose');
    if (ready.State === 'running' || ready.State === 'created') throw new Error(`provider-ready is ${ready.State}`);
    assert.equal(ready.State, 'exited', JSON.stringify(ready));
    assert.equal(Number(ready.ExitCode), 0, JSON.stringify(ready));
    return ready;
  }, 50_000);
  let metricsPort = assignedPort(await compose(['port', 'victoriametrics', '8428']));
  let tracesPort = assignedPort(await compose(['port', 'victoriatraces', '10428']));
  const beforeRestartPorts = { metrics: metricsPort, traces: tracesPort };
  let metricsURL = `http://127.0.0.1:${metricsPort}`;
  let tracesURL = `http://127.0.0.1:${tracesPort}`;

  failedStage = 'provider readiness';
  await waitFor('VictoriaMetrics readiness', () => request(`${metricsURL}/-/ready`));
  await waitFor('VictoriaTraces Tempo health', () => request(`${tracesURL}/select/tempo/api/echo`));

  const now = Date.now();
  const sampleTimestamp = Math.floor(now / 1000) * 1000;
  const metricLabel = `f2_${process.pid}_${randomBytes(5).toString('hex')}`;
  const metricName = 'almanac_f2_fixture_value';
  const expectedValue = 73.25;
  failedStage = 'write and query Prometheus fixture';
  const metricLine = `${metricName}{fixture="${metricLabel}"} ${expectedValue} ${sampleTimestamp}\n`;
  await request(`${metricsURL}/api/v1/import/prometheus`, { method: 'POST', headers: { 'content-type': 'text/plain' }, body: metricLine });
  const metricQueryParams = new URLSearchParams({ query: `${metricName}{fixture="${metricLabel}"}`,
    start: String((sampleTimestamp - 30_000) / 1000), end: String((sampleTimestamp + 30_000) / 1000), step: '1s' });
  async function queryMetric() {
    const result = JSON.parse(await request(`${metricsURL}/api/v1/query_range?${metricQueryParams}`));
    assert.equal(result.status, 'success', JSON.stringify(result));
    assert.ok(Array.isArray(result.data?.result), JSON.stringify(result));
    const series = result.data.result.find((item) => item.metric?.fixture === metricLabel);
    assert.ok(series, 'query returned no series with the unique fixture label');
    const sample = series.values?.find(([timestamp, value]) => Math.abs(Number(timestamp) * 1000 - sampleTimestamp) <= 1000 && Number(value) === expectedValue);
    assert.ok(sample, `expected ${expectedValue} at ${sampleTimestamp}, got ${JSON.stringify(series.values)}`);
    return { series: series.metric, sample: [Number(sample[0]), Number(sample[1])] };
  }
  const metricResult = await waitFor('Prometheus query result', queryMetric);

  const serviceName = `almanac-f2-${process.pid}-${randomBytes(4).toString('hex')}`;
  const operation = 'almanac_fixture_operation';
  const spanEnd = Math.floor(Date.now() / 1000) * 1_000_000_000;
  const otlp = { resourceSpans: [{ resource: { attributes: [{ key: 'service.name', value: { stringValue: serviceName } }] },
    scopeSpans: [{ scope: { name: 'almanac.provider.fixture' }, spans: Array.from({ length: 5 }, (_, index) => ({
      traceId: (index + 1).toString(16).padStart(32, '0'), spanId: (index + 1).toString(16).padStart(16, '0'),
      name: operation, kind: 2, startTimeUnixNano: String(spanEnd - (index + 1) * 1_000_000_000),
      endTimeUnixNano: String(spanEnd - (index + 1) * 1_000_000_000 + 100_000_000),
      attributes: [{ key: 'almanac.fixture', value: { stringValue: serviceName } }], status: { code: 1 },
    })) }] }] };
  failedStage = 'write and query TraceQL metrics fixture';
  await request(`${tracesURL}/insert/opentelemetry/v1/traces`, {
    method: 'POST', headers: { 'content-type': 'application/json' }, body: JSON.stringify(otlp),
  });
  const traceQueryParams = new URLSearchParams({ q: `{ resource.service.name = "${serviceName}" } | rate() by (name)`,
    start: String(spanEnd / 1_000_000_000 - 10), end: String(spanEnd / 1_000_000_000 + 2), step: '1s' });
  async function queryTraces() {
    const result = JSON.parse(await request(`${tracesURL}/select/tempo/api/metrics/query_range?${traceQueryParams}`));
    assert.equal(result.status, 'COMPLETE', JSON.stringify(result));
    assert.ok(Array.isArray(result.series), JSON.stringify(result));
    const series = result.series.find((item) => item.labels?.some((label) => label.key === 'name' && label.value?.stringValue === operation));
    assert.ok(series, `TraceQL metrics query returned no ${operation} series: ${JSON.stringify(result)}`);
    const samples = series.samples ?? [];
    assert.ok(samples.length > 0, `TraceQL metrics series had no samples: ${JSON.stringify(series)}`);
    const values = samples.map((sample) => Number(sample.value));
    assert.ok(values.every((value) => Number.isFinite(value) && value >= 0), `TraceQL metrics values were invalid: ${JSON.stringify(series)}`);
    const derivedSpanCount = values.reduce((sum, value) => sum + value, 0) * 1;
    assert.ok(Math.abs(derivedSpanCount - 5) < 0.05,
      `expected rate × 1s samples to represent exactly five spans, got ${derivedSpanCount}: ${JSON.stringify(series)}`);
    return { labels: series.labels, derivedSpanCount, samples: samples.map((sample) => ({ timestampMs: Number(sample.timestampMs), value: Number(sample.value) })) };
  }
  const traceResult = await waitFor('TraceQL metrics result', queryTraces);

  failedStage = 'stop and restart providers, then verify persisted fixtures';
  await compose(['stop', 'victoriametrics', 'victoriatraces'], { timeout: 30_000 });
  await compose(['start', 'victoriametrics', 'victoriatraces'], { timeout: 30_000 });
  await compose(['run', '--rm', '--no-deps', 'provider-ready'], { timeout: 55_000 });
  metricsPort = assignedPort(await compose(['port', 'victoriametrics', '8428']));
  tracesPort = assignedPort(await compose(['port', 'victoriatraces', '10428']));
  metricsURL = `http://127.0.0.1:${metricsPort}`;
  tracesURL = `http://127.0.0.1:${tracesPort}`;
  const persistedMetricResult = await waitFor('persisted Prometheus query result', queryMetric);
  const persistedTraceResult = await waitFor('persisted TraceQL metrics result', queryTraces);

  writeFileSync(join(artifacts, 'acceptance.json'), JSON.stringify({ project, result: 'PASS',
    services: ['victoriametrics', 'victoriatraces'],
    assignedLoopbackPorts: { beforeRestart: beforeRestartPorts, afterRestart: { metrics: metricsPort, traces: tracesPort } },
    metrics: { name: metricName, uniqueLabel: metricLabel, expectedValue, timestampMs: sampleTimestamp, result: metricResult },
    traces: { serviceName, operation, ingestedSpanCount: 5, result: traceResult },
    persistenceAfterStopStart: { metrics: persistedMetricResult, traces: persistedTraceResult },
    cleanup: 'owned resources removed in finally',
  }, null, 2), { mode: 0o600 });
} catch (error) {
  if (!cancellation) {
    const failure = { project, stage: failedStage, error: error.message, command: lastCommand };
    try { failure.services = await compose(['ps', '-a', '--format', 'json'], { timeout: 10_000 }); } catch { failure.services = 'unavailable'; }
    try { failure.logs = await compose(['logs', '--no-color', '--tail', '80'], { timeout: 10_000 }); } catch { failure.logs = 'unavailable'; }
    writeFileSync(join(artifacts, 'failure.json'), JSON.stringify(failure, null, 2), { mode: 0o600 });
    console.error(`FAIL local provider fixture at ${failedStage}; evidence: ${artifacts}`);
  }
} finally {
  await cleanup();
}

if (cancellation) process.exitCode = cancellation === 'SIGINT' ? 130 : 143;
else if (cleanupFailed || (await import('node:fs')).existsSync(join(artifacts, 'failure.json'))) process.exitCode = 1;
else console.log(`PASS local provider fixtures; evidence: ${artifacts}`);
