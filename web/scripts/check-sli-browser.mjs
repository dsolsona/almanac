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
  await expect(page.getByRole('heading', { name: 'Create SLI', exact: true })).toBeFocused();
  await page.getByLabel('SLI name', { exact: true }).fill('request-success');
  await page.getByLabel('Display name', { exact: true }).fill('Request success');
  await page.getByLabel('Datasource', { exact: true }).selectOption(fixture.datasource_id);
  await page.getByLabel('Good count query', { exact: true }).fill('vector(99)');
  await page.getByLabel('Total count query', { exact: true }).fill('vector(100)');

  stage = 'submit SLI from the browser form';
  const createPromise = page.waitForResponse((response) => response.request().method() === 'POST'
    && new URL(response.url()).pathname === '/api/v1/slis');
  const creationStarted = performance.now();
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
  const createdAt = Date.parse(created.created_at);
  effectiveTimeEvidence = { valid: Number.isFinite(effectiveAt) && Number.isFinite(createdAt), validity_before_creation: effectiveAt < createdAt };
  assert.ok(Number.isFinite(createdAt) && effectiveAt < createdAt);
  stage = 'SLI create response ETag';
  assert.equal(createResponse.headers()['etag'], `"sli:${created.id}:${created.version}"`);

  stage = 'read initial activation in the UI';
  await expect(page.getByRole('heading', { name: 'Request success', exact: true })).toBeFocused();
  stage = 'fragment history preserves detail';
  await page.getByRole('link', { name: 'Skip to content' }).focus();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(/#main$/);
  await page.goBack();
  await expect(page.getByRole('button', { name: 'Refresh', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Refresh', exact: true }).focus();
  await page.getByRole('button', { name: 'Refresh', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Request success', exact: true })).not.toBeFocused();
  await expect(page.locator('.sli-state').filter({ hasText: 'Activation' }).locator('strong')).toHaveText(/scheduled|preparing|active/);
  const screenshotDir = fixture.artifacts;
  await mkdir(screenshotDir, { recursive: true, mode: 0o700 });
  const captureSLILayout = async (name, width, height, editor = false) => {
    await page.setViewportSize({ width, height });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, `${name} overflows the viewport`);
    if (editor) {
      assert.equal(await page.locator('.query-editor').evaluateAll((editors) => editors.length === 2 && editors.every((item) => {
        const label = item.querySelector('.query-editor__label')?.getBoundingClientRect();
        const surface = item.querySelector('.query-editor__surface')?.getBoundingClientRect();
        const bounds = item.getBoundingClientRect();
        return label && surface && label.bottom <= surface.top + 1 && surface.bottom <= bounds.bottom + 1;
      })), true, `${name} query editor labels and inputs overlap`);
    } else {
      const bounds = await page.getByRole('region', { name: 'Service level indicator table' }).boundingBox();
      assert.ok(bounds && bounds.x >= 0 && bounds.x + bounds.width <= width + 1, `${name} SLI table escapes the viewport`);
    }
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: join(screenshotDir, `${name}.png`), fullPage: true, animations: 'disabled' });
  };
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
  const elapsedToObservation = performance.now() - creationStarted;
  const observedDetail = await page.evaluate(async (id) => {
    const response = await fetch(`/api/v1/slis/${id}`, { credentials: 'same-origin', cache: 'no-store' });
    if (!response.ok) throw new Error('SLI detail unavailable');
    return response.json();
  }, created.id);
  const firstObservation = observedDetail.latest_observation;
  assert.equal(firstObservation.outcome, 'observed');
  assert.equal(firstObservation.good_count, '99');
  assert.equal(firstObservation.total_count, '100');
  assert.equal(Date.parse(firstObservation.interval_end) - Date.parse(firstObservation.interval_start), created.polling_interval_seconds * 1000);
  assert.ok(Date.parse(firstObservation.interval_start) >= effectiveAt);
  assert.ok(Date.parse(firstObservation.recorded_at) >= Date.parse(firstObservation.interval_end) + created.settling_delay_seconds * 1000);
  const historyRow = page.getByRole('row').filter({ hasText: 'Observed' });
  await expect(historyRow).toContainText('99');
  await expect(historyRow).toContainText('100');
  await page.screenshot({ path: join(screenshotDir, 'sli-detail-light-observed.png'), fullPage: true });

  stage = 'seed linked SLO summaries through the API';
  const linkedSLOs = await page.evaluate(async (sliID) => {
    const contextResponse = await fetch('/api/v1/auth/context', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: '{}' });
    if (!contextResponse.ok) throw new Error('authenticated context unavailable');
    const context = await contextResponse.json();
    const headers = { 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token };
    const serviceResponse = await fetch('/api/v1/services', { method: 'POST', credentials: 'same-origin', headers: { ...headers, 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ name: 'sli-reference-journey', display_name: 'SLI reference journey' }) });
    if (serviceResponse.status !== 201) throw new Error('reference service creation failed');
    const service = await serviceResponse.json();
    const created = [];
    for (let index = 0; index < 6; index++) {
      const response = await fetch('/api/v1/slos', { method: 'POST', credentials: 'same-origin', headers: { ...headers, 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ name: `reference-slo-${index + 1}`, display_name: `Reference SLO ${index + 1}`, service_id: service.id, sli_id: sliID, target: '0.99', rolling_window_seconds: 60 }) });
      if (response.status !== 201) throw new Error('reference SLO creation failed');
      created.push(await response.json());
    }
    return created.map((item) => ({ id: item.id, display_name: item.display_name }));
  }, created.id);
  assert.equal(linkedSLOs.length, 6);

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

  stage = 'navigate related records and breadcrumbs';
  const detailURL = page.url();
  const agentLink = page.locator(`a[href="/agents?agent=${fixture.agent_id}"]`);
  await expect(agentLink).toBeVisible();
  await agentLink.focus();
  await page.keyboard.press('Enter');
  await expect(page).toHaveURL(new RegExp(`/agents\\?agent=${fixture.agent_id}`));
  await expect(page.getByRole('link', { name: 'Agents', exact: true }).first()).toBeVisible();
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Agents' }).click();
  await expect(page).toHaveURL(new RegExp('/agents$'));
  await page.goBack();
  await expect(page).toHaveURL(new RegExp(`/agents\\?agent=${fixture.agent_id}`));
  await page.goBack();
  await expect(page).toHaveURL(detailURL);
  await page.locator(`a[href="/datasources/${fixture.datasource_id}"]`).click();
  await expect(page).toHaveURL(new RegExp(`/datasources/${fixture.datasource_id}$`));
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Datasources' }).click();
  await expect(page).toHaveURL(new RegExp('/datasources$'));
  await page.goBack();
  await page.goBack();
  await expect(page).toHaveURL(detailURL);
  await page.goForward();
  await expect(page).toHaveURL(new RegExp(`/datasources/${fixture.datasource_id}$`));
  const detailSources = page.waitForResponse((response) => new URL(response.url()).pathname === '/api/v1/datasources');
  await page.goBack();
  await expect(page).toHaveURL(detailURL);
  await detailSources;
  stage = 'recover datasource list failure';
  await page.route('**/api/v1/datasources?*', async (route) => {
    await route.fulfill({ status: 503, contentType: 'application/problem+json', body: JSON.stringify({ code: 'datasource_unavailable' }) });
  }, { times: 1 });
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'SLIs' }).click();
  await expect(page).toHaveURL(new RegExp('/slis$'));
  const recoverySLI = page.getByRole('link', { name: 'Request success', exact: true });
  await expect(recoverySLI).toBeVisible();
  await expect(recoverySLI).toHaveAttribute('href', `/slis/${created.id}`);
  await expect(page.getByRole('heading', { name: 'SLIs', exact: true })).toBeFocused();
  await expect(page.getByRole('alert')).toContainText('Datasources could not be loaded');
  await page.getByRole('button', { name: 'Try again', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Add SLI', exact: true })).toBeVisible();
  await page.goBack();
  await expect(page).toHaveURL(detailURL);

  stage = 'search and filter the server-backed SLI table';
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'SLIs' }).click();
  await expect(page).toHaveURL(new RegExp('/slis$'));
  const listRow = page.getByRole('row').filter({ hasText: 'Request success' });
  await expect(listRow).toBeVisible();
  await expect(listRow.getByRole('img', { name: /Recent good-to-total measurements/ })).toBeVisible();
  await expect(listRow).toContainText('5 of 6');
  await expect(listRow.getByRole('link', { name: 'Reference SLO 1' })).toHaveAttribute('href', new RegExp('^/slos/[0-9a-f-]{36}$'));
  await listRow.getByText(/^Exact values ·/).click();
  const recentTable = listRow.getByRole('table', { name: `Exact counts for recent ${created.polling_interval_seconds}-second intervals` });
  const observedStart = recentTable.locator('tbody > tr > td:first-child').filter({ hasText: new Date(firstObservation.interval_start).toISOString() });
  await expect(observedStart).toHaveCount(1);
  const observedSample = observedStart.locator('..');
  await expect(observedSample.getByRole('cell', { name: 'Observed', exact: true })).toBeVisible();
  await expect(observedSample.getByRole('cell', { name: '99', exact: true })).toBeVisible();
  await expect(observedSample.getByRole('cell', { name: '100', exact: true })).toBeVisible();
  const searchInput = page.getByRole('textbox', { name: 'Search SLIs', exact: true });
  await searchInput.fill('request-success');
  await expect(page).toHaveURL(/q=request-success/);
  await expect(page.getByRole('row').filter({ hasText: 'Request success' })).toBeVisible();
  const { filteredPage, savedDatasourceName } = await page.evaluate(async (datasourceID) => {
    const response = await fetch(`/api/v1/slis?limit=50&q=request-success&datasource_id=${encodeURIComponent(datasourceID)}`, { credentials: 'same-origin', cache: 'no-store' });
    if (!response.ok) throw new Error('filtered SLI page unavailable');
    const datasourceResponse = await fetch('/api/v1/datasources?limit=200', { credentials: 'same-origin', cache: 'no-store' });
    if (!datasourceResponse.ok) throw new Error('saved datasource page unavailable');
    const datasourcePage = await datasourceResponse.json();
    const datasource = datasourcePage.items.find((item) => item.id === datasourceID);
    if (!datasource) throw new Error('saved datasource missing from its page');
    return { filteredPage: await response.json(), savedDatasourceName: datasource.display_name };
  }, fixture.datasource_id);
  assert.equal(filteredPage.items.length, 1);
  assert.equal(filteredPage.items[0].datasource_display_name, savedDatasourceName);
  assert.ok(filteredPage.items[0].recent_measurements.some((sample) => sample.state === 'observed' && sample.good_count === '99' && sample.total_count === '100'));
  assert.equal(filteredPage.items[0].linked_slos.total_count, 6);
  assert.equal(filteredPage.items[0].linked_slos.items.length, 5);
  await page.getByLabel('Datasource', { exact: true }).selectOption(fixture.datasource_id);
  await expect(page).toHaveURL(new RegExp(`datasource_id=${fixture.datasource_id}`));
  const listURL = page.url();
  await page.reload();
  await expect(page).toHaveURL(listURL);
  await expect(page.getByRole('row').filter({ hasText: 'Request success' })).toBeVisible();
  await page.getByRole('row').filter({ hasText: 'Request success' }).getByRole('link', { name: 'Reference SLO 1' }).click();
  await expect(page).toHaveURL(new RegExp(`/slos/${linkedSLOs[0].id}$`));
  await page.goBack();
  await expect(page).toHaveURL(listURL);
  await expect(page.getByRole('row').filter({ hasText: 'Request success' })).toBeVisible();

  stage = 'capture populated editor and list layout matrix';
  await page.getByRole('button', { name: 'Add SLI', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Create SLI', exact: true })).toBeFocused();
  await page.getByLabel('Display name', { exact: true }).fill('Reference editor preview');
  await page.getByLabel('Datasource', { exact: true }).selectOption(fixture.datasource_id);
  await page.getByLabel('Good count query', { exact: true }).fill('sum(\n  vector(99)\n)');
  await page.getByLabel('Total count query', { exact: true }).fill('sum(\n  vector(100)\n)');
  await expect(page.getByLabel('Good count query', { exact: true })).toBeVisible();
  await expect(page.getByLabel('Total count query', { exact: true })).toBeVisible();
  assert.deepEqual(await page.locator('.query-editor__line-numbers').evaluateAll((gutters) => gutters.map((gutter) => gutter.textContent)), ['123', '123']);
  for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width: 1440, height: 900 });
    if (theme === 'dark' && await page.getByRole('button', { name: 'Switch to dark theme', exact: true }).count()) {
      await page.getByRole('button', { name: 'Switch to dark theme', exact: true }).click();
    }
    if (theme === 'light' && await page.getByRole('button', { name: 'Switch to light theme', exact: true }).count()) {
      await page.getByRole('button', { name: 'Switch to light theme', exact: true }).click();
    }
    for (const [viewport, width, height] of [['desktop', 1440, 900], ['intermediate', 1024, 900], ['mobile', 360, 844]]) {
      await captureSLILayout(`sli-editor-${theme}-${viewport}`, width, height, true);
    }
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'SLIs', exact: true }).click();
  await expect(page.getByRole('link', { name: 'Request success', exact: true })).toHaveAttribute('href', `/slis/${created.id}`);
  await expect(page.getByRole('row').filter({ hasText: '5 of 6' })).toBeVisible();
  for (const theme of ['light', 'dark']) {
    await page.setViewportSize({ width: 1440, height: 900 });
    if (theme === 'dark' && await page.getByRole('button', { name: 'Switch to dark theme', exact: true }).count()) {
      await page.getByRole('button', { name: 'Switch to dark theme', exact: true }).click();
    }
    if (theme === 'light' && await page.getByRole('button', { name: 'Switch to light theme', exact: true }).count()) {
      await page.getByRole('button', { name: 'Switch to light theme', exact: true }).click();
    }
    for (const [viewport, width, height] of [['desktop', 1440, 900], ['intermediate', 1024, 900], ['mobile', 360, 844]]) {
      await expect(page.getByRole('link', { name: 'Request success', exact: true })).toBeVisible();
      await captureSLILayout(`sli-list-${theme}-${viewport}`, width, height);
    }
  }



  await writeFile(join(screenshotDir, 'sli-journey.json'), JSON.stringify({
    scenario: 'browser-create-agent-collect-readback', sli_id: created.id, measurement_revision: created.measurement_revision,
    activation_state: activationState, outcome: 'observed', good_count: '99', total_count: '100',
    datasource_id: fixture.datasource_id, agent_id: fixture.agent_id, create_status: createResponse.status(), elapsed_to_observation_ms: Math.round(elapsedToObservation),
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
