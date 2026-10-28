import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

let stage = 'fixture input';
let fixture;
let origin;
let browser;
let page;
let latestAPI = { status: 'none', code: 'none', requestID: 'none' };
let effectiveTimeEvidence = null;
const startedAt = Date.now();
const safeCodes = new Set(['agent_unavailable', 'conflict', 'forbidden', 'invalid_request', 'measurement_conflict', 'not_found', 'observation_unavailable', 'sli_unavailable', 'source_secret_unavailable', 'unauthenticated', 'unauthorized', 'unsupported_measurement']);

function safePart(value) {
  return typeof value === 'string' && /^[A-Za-z0-9_-]{1,96}$/.test(value) ? value : 'none';
}

async function writeProgress(state = {}) {
  if (!fixture?.artifacts) return;
  await writeFile(join(fixture.artifacts, 'sli-progress.json'), JSON.stringify({
    stage: safePart(stage.replaceAll(' ', '_')), elapsed_ms: Date.now() - startedAt,
    api_status: safePart(latestAPI.status), api_code: safePart(latestAPI.code), request_id: safePart(latestAPI.requestID),
    activation: safePart(state.activation), reachability: safePart(state.reachability), freshness: safePart(state.freshness),
  }) + '\n', { mode: 0o600 });
}

try {
  let input = '';
  for await (const chunk of process.stdin) {
    input += chunk;
    if (Buffer.byteLength(input) > 16 * 1024) throw new Error('bounded input exceeded');
  }
  fixture = JSON.parse(input);
  input = '';
  origin = new URL(fixture.url);
  assert.equal(origin.protocol, 'https:');
  assert.equal(origin.hostname, 'localhost');
  assert.match(fixture.spki, /^[A-Za-z0-9+/]{43}=$/);
  assert.match(fixture.datasource_id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  assert.match(fixture.agent_id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  assert.ok(typeof fixture.login === 'string' && fixture.login.length < 100);
  assert.ok(typeof fixture.password === 'string' && fixture.password.length >= 15 && fixture.password.length <= 128);
  assert.ok(typeof fixture.artifacts === 'string' && fixture.artifacts.length < 1024);
  await mkdir(fixture.artifacts, { recursive: true, mode: 0o700 });
  await writeProgress();

  stage = 'browser module import';
  const { chromium, expect } = await import('@playwright/test');
  stage = 'browser launch';
  browser = await chromium.launch({ headless: true, args: [`--ignore-certificate-errors-spki-list=${fixture.spki}`] });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
  context.setDefaultTimeout(10_000);
  page = await context.newPage();
  page.on('response', async (response) => {
    let url;
    try { url = new URL(response.url()); } catch { return; }
    if (url.origin !== origin.origin || !url.pathname.startsWith('/api/v1/')) return;
    const headers = response.headers();
    const requestID = headers['x-request-id'] ?? '';
    latestAPI = {
      status: String(response.status()),
      requestID: /^[0-9a-f]{32}$/.test(requestID) || /^[0-9a-f-]{36}$/.test(requestID) ? requestID : 'none',
      code: 'none',
    };
    if (response.status() >= 400) {
      try {
        const body = await response.json();
        if (body && typeof body.code === 'string' && safeCodes.has(body.code)) latestAPI.code = body.code;
      } catch { /* Report only bounded status evidence. */ }
    }
  });

  stage = 'administrator sign in';
  await page.goto(fixture.url);
  await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
  await page.getByLabel('Login', { exact: true }).fill(fixture.login);
  await page.getByLabel('Password', { exact: true }).fill(fixture.password);
  await page.getByLabel('Password', { exact: true }).press('Enter');
  fixture.password = '';
  await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();

  stage = 'reject mutation without CSRF';
  const deniedStatus = await page.evaluate(async (datasourceID) => {
    const result = await fetch('/api/v1/slis', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': 'missing-csrf-check' }, body: JSON.stringify({ name: 'must-not-create', display_name: 'Must not create', datasource_id: datasourceID, measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: 'vector(99)', total_query: 'vector(100)' }) });
    return result.status;
  }, fixture.datasource_id);
  assert.equal(deniedStatus, 403);

  stage = 'open create SLI flow';
  await page.goto(new URL('/slis/new', origin).href);
  await expect(page.getByRole('heading', { name: 'Create SLI', exact: true })).toBeVisible();
  await page.getByLabel('SLI name', { exact: true }).fill('request-success');
  await page.getByLabel('Display name', { exact: true }).fill('Request success');
  await page.getByLabel('Datasource', { exact: true }).selectOption(fixture.datasource_id);
  await page.getByLabel('Good count query', { exact: true }).fill('vector(99)');
  await page.getByLabel('Total count query', { exact: true }).fill('vector(100)');

  stage = 'submit SLI from the browser form';
  const createPromise = page.waitForResponse((response) => response.request().method() === 'POST'
    && new URL(response.url()).pathname === '/api/v1/slis');
  await page.getByRole('button', { name: 'Create SLI', exact: true }).click();
  stage = 'SLI create response status';
  const createResponse = await createPromise;
  assert.equal(createResponse.status(), 201);
  const createRequest = createResponse.request();
  stage = 'SLI create idempotency header';
  assert.match(createRequest.headers()['idempotency-key'], /^[\x21-\x7e]{1,128}$/);
  stage = 'SLI create CSRF header';
  assert.equal(createRequest.headers()['x-csrf-token']?.length > 0, true);
  stage = 'SLI create request JSON';
  const inputDefinition = createRequest.postDataJSON();
  stage = 'SLI create request fields';
  assert.deepEqual(Object.keys(inputDefinition).sort(), ['component_mode', 'datasource_id', 'display_name', 'good_query', 'measurement_kind', 'name', 'total_query'].sort());
  stage = 'SLI create request name';
  assert.equal(inputDefinition.name, 'request-success');
  stage = 'SLI create request display name';
  assert.equal(inputDefinition.display_name, 'Request success');
  stage = 'SLI create request datasource';
  assert.equal(inputDefinition.datasource_id, fixture.datasource_id);
  stage = 'SLI create request measurement kind';
  assert.equal(inputDefinition.measurement_kind, 'event_counts');
  stage = 'SLI create request component mode';
  assert.equal(inputDefinition.component_mode, 'interval_count');
  stage = 'SLI create request good query';
  assert.equal(inputDefinition.good_query, 'vector(99)');
  stage = 'SLI create request total query';
  assert.equal(inputDefinition.total_query, 'vector(100)');
  stage = 'SLI create response JSON';
  const created = await createResponse.json();
  stage = 'SLI create response ID';
  assert.match(created.id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  stage = 'SLI create response revision';
  assert.equal(created.measurement_revision, '1');
  stage = 'SLI create response measurement kind';
  assert.equal(created.measurement_kind, 'event_counts');
  stage = 'SLI create response component mode';
  assert.equal(created.component_mode, 'interval_count');
  stage = 'SLI create response effective time';
  const effectiveAt = new Date(created.effective_from).getTime();
  const comparedAt = Date.now();
  effectiveTimeEvidence = { valid: Number.isFinite(effectiveAt), after_check: effectiveAt > comparedAt,
    within_one_second: Number.isFinite(effectiveAt) && Math.abs(effectiveAt - comparedAt) <= 1000 };
  assert.ok(effectiveAt > comparedAt);
  stage = 'SLI create response ETag';
  assert.equal(createResponse.headers()['etag'], `"sli:${created.id}:${created.version}"`);

  stage = 'read scheduled activation in the UI';
  await expect(page.getByRole('heading', { name: 'Request success', exact: true })).toBeVisible();
  await expect(page.locator('.sli-state').filter({ hasText: 'Activation' }).locator('strong')).toHaveText(/scheduled|preparing/);
  const screenshotDir = fixture.artifacts;
  await mkdir(screenshotDir, { recursive: true, mode: 0o700 });
  await page.screenshot({ path: join(screenshotDir, 'sli-detail-light-desktop.png'), fullPage: true });

  stage = 'wait for agent ACK and first interval';
  await writeProgress();
  const deadline = Date.now() + 110_000;
  let observed = false;
  let activationState = created.activation_state;
  let lastProgress = 0;
  while (Date.now() < deadline) {
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    if (Date.now() - lastProgress >= 5000) {
      const state = {};
      for (const label of ['Activation', 'Agent reachability', 'Freshness']) {
        const value = await page.locator('.sli-state').filter({ hasText: label }).locator('strong').textContent();
        state[label === 'Activation' ? 'activation' : label === 'Freshness' ? 'freshness' : 'reachability'] = value?.trim().replaceAll(' ', '_');
      }
      await writeProgress(state);
      lastProgress = Date.now();
    }
    const counts = page.locator('.sli-counts strong');
    if (await counts.count() === 2 && await counts.nth(0).textContent() === '99' && await counts.nth(1).textContent() === '100') {
      observed = true;
      activationState = (await page.locator('.sli-state').filter({ hasText: 'Activation' }).textContent())?.replace(/^Activation\s*/, '').trim() ?? 'unknown';
      break;
    }
    await page.waitForTimeout(1000);
  }
  assert.equal(observed, true, `first 99/100 interval was not visible before deadline; latest_api_status=${latestAPI.status} latest_api_code=${latestAPI.code}`);
  assert.equal(activationState, 'active', `unexpected activation state ${safePart(activationState)}`);
  const historyRow = page.getByRole('row').filter({ hasText: 'Observed' });
  await expect(historyRow).toContainText('99');
  await expect(historyRow).toContainText('100');
  await page.screenshot({ path: join(screenshotDir, 'sli-detail-light-observed.png'), fullPage: true });

  stage = 'inspect dark and mobile layouts';
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole('heading', { name: 'Request success', exact: true })).toBeVisible();
  await page.screenshot({ path: join(screenshotDir, 'sli-detail-light-mobile.png'), fullPage: true });
  await page.keyboard.press('Tab');
  assert.equal(await page.evaluate(() => document.activeElement !== document.body), true);
  await page.setViewportSize({ width: 1440, height: 900 });
  const darkToggle = page.getByRole('button', { name: 'Switch to dark theme', exact: true });
  await expect(darkToggle).toBeVisible();
  await darkToggle.click();
  await expect(page.getByRole('button', { name: 'Switch to light theme', exact: true })).toBeVisible();
  await page.screenshot({ path: join(screenshotDir, 'sli-detail-dark-desktop.png'), fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole('heading', { name: 'Request success', exact: true })).toBeVisible();
  await page.screenshot({ path: join(screenshotDir, 'sli-detail-dark-mobile.png'), fullPage: true });
  await page.getByRole('button', { name: 'Menu', exact: true }).click();
  await expect(page.getByRole('dialog', { name: 'Navigation menu' })).toBeVisible();
  await page.getByRole('button', { name: 'Close navigation', exact: true }).click();

  await writeFile(join(screenshotDir, 'sli-journey.json'), JSON.stringify({
    scenario: 'browser-create-agent-collect-readback', sli_id: created.id, measurement_revision: created.measurement_revision,
    activation_state: activationState, outcome: 'observed', good_count: '99', total_count: '100',
    datasource_id: fixture.datasource_id, agent_id: fixture.agent_id, create_status: createResponse.status(),
  }, null, 2) + '\n', { mode: 0o600 });
  await context.close();
  await browser.close();
  console.log('PASS browser-sli-journey');
} catch (error) {
  const stageValue = safePart(stage.replaceAll(' ', '_'));
  const status = safePart(latestAPI.status);
  const requestID = safePart(latestAPI.requestID);
  const code = safeCodes.has(latestAPI.code) ? latestAPI.code : 'none';
  await page?.screenshot({ path: fixture?.artifacts ? join(fixture.artifacts, 'sli-failure.png') : undefined, fullPage: true }).catch(() => {});
  const failureClass = error instanceof Error && error.name === 'TimeoutError' ? 'timeout'
    : stage === 'browser module import' ? 'module'
      : stage === 'browser launch' ? 'launch' : 'other';
  const diagnosticClass = error?.code === 'ERR_ASSERTION' ? 'assertion'
    : error instanceof Error && error.name === 'TimeoutError' ? 'timeout'
      : error instanceof SyntaxError ? 'json_parse'
        : typeof error?.message === 'string' && /Response\.(?:body|json):/.test(error.message) ? 'response_body'
          : typeof error?.message === 'string' && /element is not attached|element was detached/i.test(error.message) ? 'element_detached' : 'other';
  const sourceLine = typeof error?.stack === 'string' ? error.stack.match(/check-sli-browser\.mjs:(\d{1,4}):\d+/)?.[1] : undefined;
  const safeType = (value) => value === null ? 'null' : ['string', 'number', 'boolean', 'undefined'].includes(typeof value) ? typeof value : Array.isArray(value) ? 'array' : 'object';
  if (fixture?.artifacts) {
    const detail = { stage: stageValue, category: diagnosticClass, source_line: sourceLine ? Number(sourceLine) : null,
      assertion_actual_type: diagnosticClass === 'assertion' ? safeType(error.actual) : null,
      assertion_expected_type: diagnosticClass === 'assertion' ? safeType(error.expected) : null,
      assertion_actual_boolean: diagnosticClass === 'assertion' && typeof error.actual === 'boolean' ? error.actual : null,
      assertion_expected_boolean: diagnosticClass === 'assertion' && typeof error.expected === 'boolean' ? error.expected : null,
      effective_time: effectiveTimeEvidence };
    await writeFile(join(fixture.artifacts, 'sli-failure-detail.json'), JSON.stringify(detail) + '\n', { mode: 0o600 }).catch(() => {});
  }
  console.log(`FAIL browser-sli-journey stage=${stageValue} api_status=${status} request_id=${requestID} api_code=${code} class=${failureClass}`);
  process.exitCode = 1;
  await browser?.close().catch(() => {});
}
