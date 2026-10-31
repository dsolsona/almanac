import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { resolve, sep, join } from 'node:path';
import { chromium } from '@playwright/test';

// This executable belongs to the controlled performance fixture. It is not
// invoked by the ordinary browser catalogue or the two-minute journey gate.
let browser;
let output;
let stage = 'fixture';
const report = {
  schema_version: 1,
  environment: { cpu_slowdown: 4, download_bits_per_second: 10_000_000, latency_ms: 100, viewport: { width: 1440, height: 900 } },
  cold_loads: [], interactions: [], failures: [],
};

function distribution(samples, key) {
  const ordered = samples.map(sample => sample[key]).filter(Number.isFinite).sort((a, b) => a - b);
  const rank = percentile => ordered[Math.ceil(percentile * ordered.length) - 1] ?? null;
  return { count: ordered.length, p50_ms: rank(.50), p75_ms: rank(.75), p95_ms: rank(.95), p99_ms: rank(.99), max_ms: ordered.at(-1) ?? null };
}

// Runs before the app. It observes real fetches/DOM changes without supplying or
// altering API responses. Only timings and route paths leave the browser.
function installMeasurements() {
  const state = window.__almanacPerformance = { fetches: [], primary_ms: null, interaction: null };
  const originalFetch = window.fetch;
  window.fetch = async function (...args) {
    const input = args[0];
    const url = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href);
    const started = performance.now();
    const response = await originalFetch.apply(this, args);
    if (url.origin !== location.origin || !url.pathname.startsWith('/api/')) return response;
    const sample = { path: url.pathname, started_ms: started, headers_ms: performance.now(), status: response.status };
    state.fetches.push(sample);
    const json = response.json.bind(response);
    response.json = async () => {
      sample.parse_started_ms = performance.now();
      try { return await json(); }
      finally { sample.parsed_ms = performance.now(); }
    };
    return response;
  };
  const visible = element => !!element && element.getBoundingClientRect().width > 0 && element.getBoundingClientRect().height > 0;
  const ready = () => {
    if (location.pathname === '/slis') return visible(document.querySelector('.sli-list-table tbody tr'));
    if (location.pathname === '/slos') return visible(document.querySelector('.app-slo-row'));
    return visible(document.querySelector('.app-timeline-plot .timeline-chart__plot svg')) && document.querySelectorAll('.app-lane-label').length === 4 && !document.querySelector('.slo-error');
  };
  let primaryScheduled = false;
  const afterPaint = callback => requestAnimationFrame(() => requestAnimationFrame(callback));
  new MutationObserver(mutations => {
    if (state.primary_ms === null && !primaryScheduled && ready()) {
      primaryScheduled = true;
      afterPaint(() => { if (ready()) state.primary_ms = performance.now(); else primaryScheduled = false; });
    }
    const interaction = state.interaction;
    if (!interaction?.started_ms) return;
    if (interaction.feedback_ms === null && interaction.button.getAttribute('aria-pressed') === 'true' && !interaction.feedback_scheduled) {
      interaction.feedback_scheduled = true;
      afterPaint(() => { interaction.feedback_ms = performance.now() - interaction.started_ms; });
    }
    if (mutations.some(mutation => mutation.target instanceof Element && (mutation.target.closest('.timeline-chart__plot') || mutation.target.matches('.app-timeline-plot')))) {
      interaction.plot_mutated = true;
    }
    const refreshing = [...document.querySelectorAll('.app-timeline [role="status"]')].some(node => node.textContent === 'Refreshing operational evaluation');
    if (!interaction.update_scheduled && interaction.plot_mutated && interaction.button.getAttribute('aria-pressed') === 'true' && !refreshing && ready()) {
      interaction.update_scheduled = true;
      afterPaint(() => {
        const parsed = state.fetches.filter(sample => sample.path.endsWith('/operational') && sample.status === 200 && sample.parsed_ms >= interaction.started_ms).at(-1);
        interaction.finished_ms = performance.now();
        interaction.chart_ms = interaction.finished_ms - (parsed?.parsed_ms ?? interaction.started_ms);
        interaction.network_backed = !!parsed;
      });
    }
  }).observe(document, { subtree: true, childList: true, attributes: true, characterData: true });
}

async function measuredPage(context) {
  await context.addInitScript(installMeasurements);
  const page = await context.newPage();
  const cdp = await context.newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.setCacheDisabled', { cacheDisabled: true });
  await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 100, downloadThroughput: 1_250_000, uploadThroughput: 1_250_000 });
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 });
  return page;
}

async function browserTimings(page) {
  return page.evaluate(() => ({
    primary_ms: window.__almanacPerformance.primary_ms,
    fetches: window.__almanacPerformance.fetches,
    resources: performance.getEntriesByType('resource').map(entry => ({
      path: new URL(entry.name).pathname, initiator: entry.initiatorType,
      started_ms: entry.startTime, response_started_ms: entry.responseStart, response_ended_ms: entry.responseEnd,
      transfer_bytes: entry.transferSize, encoded_bytes: entry.encodedBodySize, decoded_bytes: entry.decodedBodySize,
    })),
    navigation: performance.getEntriesByType('navigation').map(entry => ({
      response_started_ms: entry.responseStart, response_ended_ms: entry.responseEnd,
      dom_interactive_ms: entry.domInteractive, dom_content_loaded_ms: entry.domContentLoadedEventEnd,
    })),
  }));
}

try {
  let input = '';
  for await (const chunk of process.stdin) {
    input += chunk;
    assert.ok(Buffer.byteLength(input) <= 16 * 1024);
  }
  const fixture = JSON.parse(input);
  input = '';
  const origin = new URL(fixture.url);
  assert.equal(origin.protocol, 'https:');
  assert.equal(origin.hostname, 'localhost');
  assert.equal(origin.pathname, '/');
  assert.equal(origin.search, '');
  assert.equal(origin.hash, '');
  assert.equal(origin.username + origin.password, '');
  assert.match(fixture.spki, /^[A-Za-z0-9+/]{43}=$/);
  assert.match(fixture.slo_id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  assert.match(fixture.candidate_binary_sha256, /^[0-9a-f]{64}$/);
  assert.ok(typeof fixture.login === 'string' && typeof fixture.password === 'string' && fixture.login && fixture.password);
  output = resolve(fixture.artifacts);
  assert.ok(output.startsWith(resolve('..', '.local') + sep));
  await mkdir(output, { recursive: true, mode: 0o700 });
  report.candidate_binary_sha256 = fixture.candidate_binary_sha256;
  browser = await chromium.launch({ headless: true, args: [`--ignore-certificate-errors-spki-list=${fixture.spki}`] });
  report.environment.browser = browser.version();
  report.environment.cache = 'fresh browser context and disabled HTTP cache; backend preparation measured separately';
  stage = 'authenticate';
  const loginContext = await browser.newContext({ viewport: report.environment.viewport });
  const loginPage = await loginContext.newPage();
  await loginPage.goto(origin.href);
  await loginPage.getByLabel('Login', { exact: true }).fill(fixture.login);
  await loginPage.getByLabel('Password', { exact: true }).fill(fixture.password);
  await loginPage.getByLabel('Password', { exact: true }).press('Enter');
  await loginPage.getByRole('heading', { name: 'Agents', exact: true }).waitFor();
  const storageState = await loginContext.storageState();
  fixture.password = '';
  await loginContext.close();

  for (const [view, path, budget] of [['sli-list', '/slis', 1000], ['slo-list', '/slos', 1000], ['slo-detail', `/slos/${fixture.slo_id}`, 2500]]) {
    for (let index = 0; index < 20; index++) {
      stage = `${view}-cold-${index}`;
      const context = await browser.newContext({ storageState, viewport: report.environment.viewport, colorScheme: 'light' });
      const page = await measuredPage(context);
      const sample = { view, index, budget_ms: budget };
      try {
        await page.goto(new URL(path, origin).href, { waitUntil: 'domcontentloaded', timeout: 10_000 });
        await page.waitForFunction(() => Number.isFinite(window.__almanacPerformance?.primary_ms), { }, { timeout: 10_000 });
        Object.assign(sample, await browserTimings(page));
      } catch {
        sample.failure = 'primary_content_unavailable';
        sample.primary_ms = await page.evaluate(() => performance.now()).catch(() => 10_000);
        report.failures.push(stage);
      } finally {
        report.cold_loads.push(sample);
        await context.close();
        await writeFile(join(output, 'browser-performance.json'), JSON.stringify(report) + '\n', { mode: 0o600 });
      }
    }
  }
  stage = 'interaction-setup';
  const context = await browser.newContext({ storageState, viewport: report.environment.viewport, colorScheme: 'light' });
  const page = await measuredPage(context);
  await page.goto(new URL(`/slos/${fixture.slo_id}`, origin).href);
  await page.waitForFunction(() => Number.isFinite(window.__almanacPerformance?.primary_ms), { }, { timeout: 10_000 });
  for (let index = 0; index < 50; index++) {
    stage = `interaction-${index}`;
    const group = index % 2 === 0 ? 'Objective history period' : 'Burn window';
    const label = index % 2 === 0 ? ['90 d', '1 y', '7 d'][Math.floor(index / 2) % 3] : Math.floor(index / 2) % 2 === 0 ? '6 h' : '1 h';
    const sample = { index, group, label };
    try {
      await page.evaluate(({ group, label }) => {
        const container = [...document.querySelectorAll('[role="group"]')].find(node => node.getAttribute('aria-label') === group);
        const button = [...container.querySelectorAll('button')].find(node => node.textContent.trim() === label);
        if (!button || button.getAttribute('aria-pressed') === 'true') throw new Error('interaction is not a change');
        const interaction = window.__almanacPerformance.interaction = { button, feedback_ms: null, chart_ms: null };
        button.addEventListener('click', () => { interaction.started_ms = performance.now(); }, { once: true, capture: true });
      }, { group, label });
      await page.getByRole('group', { name: group, exact: true }).getByRole('button', { name: label, exact: true }).click();
      await page.waitForFunction(() => {
        const value = window.__almanacPerformance.interaction;
        return value.feedback_ms !== null && value.chart_ms !== null;
      }, { }, { timeout: 15_000 });
      Object.assign(sample, await page.evaluate(() => {
        const value = window.__almanacPerformance.interaction;
        return { feedback_ms: value.feedback_ms, chart_ms: value.chart_ms, total_ms: value.finished_ms - value.started_ms, network_backed: value.network_backed };
      }));
    } catch {
      sample.failure = 'interaction_or_chart_unavailable';
      sample.feedback_ms = sample.chart_ms = 15_000;
      report.failures.push(stage);
    }
    report.interactions.push(sample);
    await writeFile(join(output, 'browser-performance.json'), JSON.stringify(report) + '\n', { mode: 0o600 });
  }
  report.interaction_resources = await browserTimings(page);
  await context.close();
  report.distributions = Object.fromEntries(['sli-list', 'slo-list', 'slo-detail'].map(view => [view, distribution(report.cold_loads.filter(sample => sample.view === view), 'primary_ms')]));
  report.distributions.feedback = distribution(report.interactions, 'feedback_ms');
  report.distributions.chart = distribution(report.interactions, 'chart_ms');
  for (const [view, budget] of [['sli-list', 1000], ['slo-list', 1000], ['slo-detail', 2500]]) {
    if (report.distributions[view].count !== 20 || report.distributions[view].p75_ms > budget) report.failures.push(`${view}-p75-budget`);
  }
  for (const name of ['feedback', 'chart']) {
    if (report.distributions[name].count !== 50 || report.distributions[name].p95_ms > 200) report.failures.push(`${name}-p95-budget`);
  }
  report.result = report.failures.length ? 'failed' : 'passed';
  await writeFile(join(output, 'browser-performance.json'), JSON.stringify(report) + '\n', { mode: 0o600 });
  if (report.result !== 'passed') process.exitCode = 1;
} catch {
  // Playwright errors can contain entered values. Keep credentials and raw error
  // messages out of both the artifact and terminal output.
  report.failures.push(stage);
  report.result = 'failed';
  if (output) await writeFile(join(output, 'browser-performance.json'), JSON.stringify(report) + '\n', { mode: 0o600 }).catch(() => {});
  process.exitCode = 1;
} finally {
  await browser?.close();
}
