import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { chromium, expect } from '@playwright/test';
import { readResponseJSON } from './response-read-diagnostics.mjs';

let stage = 'fixture input';
let fixture;
let origin;
let browser;
let page;
let authenticated = false;
let latestAPI = { status: null, requestID: 'none', code: 'none' };
let latestAPIRead = Promise.resolve();
let latestDiagnostic = { state: 'none', errorCode: 'none', resultClass: 'none' };
let latestDiagnosticRead = Promise.resolve();
let activeDiagnosticID = '';
let cancelResponseState = 'none';
const safeCodes = new Set([
  'agent_unavailable', 'authentication_unavailable', 'backend_unavailable', 'capability_unavailable',
  'conflict', 'configuration_failed', 'configuration_pending', 'csrf_invalid', 'datasource_unavailable', 'diagnostic_unavailable', 'forbidden',
  'invalid_agent', 'invalid_request', 'method_not_allowed', 'not_found', 'origin_rejected',
  'rate_limited', 'secure_transport_required', 'session_present', 'source_secret_unavailable',
  'unauthenticated', 'unauthorized', 'unsupported', 'unsupported_media_type',
]);
try {
let input = '';
for await (const chunk of process.stdin) {
  input += chunk;
  if (Buffer.byteLength(input) > 48 * 1024) throw new Error('fixture input too large');
}
fixture = JSON.parse(input);
input = '';
origin = new URL(fixture.url);
assert.equal(origin.protocol, 'https:');
assert.equal(origin.hostname, 'localhost');
assert.match(fixture.spki, /^[A-Za-z0-9+/]{43}=$/);
assert.match(fixture.agent_id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
assert.match(fixture.datasource_id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
assert.equal(fixture.query, 'vector(1)');
assert.equal(typeof fixture.ca_pem, 'string');
assert.ok(fixture.ca_pem.length > 0 && fixture.ca_pem.length <= 16 * 1024);
assert.match(fixture.ca_pem, /^-----BEGIN CERTIFICATE-----/);

stage = 'browser launch';
  browser = await chromium.launch({ headless: true, args: [`--ignore-certificate-errors-spki-list=${fixture.spki}`] });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
  context.setDefaultTimeout(5000);
  page = await context.newPage();
  let pageErrors = 0;
  let apiSequence = 0;
  page.on('pageerror', () => { pageErrors += 1; });
  page.on('response', (response) => {
    let url;
    try { url = new URL(response.url()); } catch { return; }
    if (url.origin !== origin.origin || !url.pathname.startsWith('/api/v1/')) return;
    const sequence = ++apiSequence;
    const headers = response.headers();
    const rawRequestID = headers['x-request-id'] ?? '';
    const requestID = /^(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/.test(rawRequestID) ? rawRequestID : 'none';
    latestAPI = { status: response.status(), requestID, code: 'none' };
    const diagnosticPath = url.pathname.match(/^\/api\/v1\/diagnostics\/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/);
    if (response.request().method() === 'GET' && response.status() === 200 && diagnosticPath) {
      latestDiagnosticRead = response.json().then((body) => {
        const states = new Set(['pending', 'running', 'succeeded', 'failed', 'canceled', 'timed_out', 'unknown']);
        const resultClasses = new Set(['complete', 'empty', 'incomplete']);
        const errorCodes = new Set(['authentication', 'authentication_failed', 'deadline', 'diagnostic_failed', 'incomplete',
          'network', 'network_failed', 'query', 'query_failed', 'rate_limited', 'tls', 'tls_failed', 'timeout',
          'unsupported', 'assignment_revoked']);
        if (!body || body.id !== diagnosticPath[1] || (activeDiagnosticID && body.id !== activeDiagnosticID) || !states.has(body.state)) return;
        latestDiagnostic = {
          state: body.state,
          errorCode: errorCodes.has(body.error_code) ? body.error_code : 'none',
          resultClass: resultClasses.has(body.result_class) ? body.result_class : 'none',
        };
      }).catch(() => {});
    }
    if (response.status() < 400) return;
    latestAPIRead = response.json().then((body) => {
      if (sequence === apiSequence && body && typeof body === 'object' && safeCodes.has(body.code)) latestAPI.code = body.code;
    }).catch(() => {});
  });

  stage = 'sign-in page';
  await page.goto(fixture.url);
  await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
  await page.getByLabel('Login', { exact: true }).fill(fixture.login);
  await page.getByLabel('Password', { exact: true }).fill(fixture.password);
  const passwordForRedaction = fixture.password;
  await page.getByLabel('Password', { exact: true }).press('Enter');
  fixture.password = '';
  stage = 'authenticated administrator landing';
  await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
  authenticated = true;

  stage = 'saved datasource detail request';
  const detailResponsePromise = page.waitForResponse((response) => response.request().method() === 'GET'
    && new URL(response.url()).pathname === `/api/v1/datasources/${fixture.datasource_id}`);
  await page.goto(new URL(`/datasources/${fixture.datasource_id}`, origin).href);
  const detailResponse = await detailResponsePromise;
  assert.equal(detailResponse.status(), 200);
  const source = await detailResponse.json();
  assert.equal(source.id, fixture.datasource_id);
  assert.equal(source.agent_id, fixture.agent_id);
  assert.equal(source.provider, 'prometheus');
  assert.equal(source.configuration_format, 'prometheus-http-v1');
  assert.equal(typeof source.authentication.password_configured, 'boolean');
  assert.equal(typeof source.authentication.token_configured, 'boolean');
  assert.equal('password' in source.authentication, false);
  assert.equal('token' in source.authentication, false);
  assert.equal('password_secret' in source.authentication, false);
  assert.equal('token_secret' in source.authentication, false);
  stage = 'datasource redacted detail rendering';
  await expect(page.getByRole('heading', { name: source.display_name, exact: true })).toBeVisible();
  await expect(page.getByLabel('Prometheus endpoint', { exact: true })).toHaveValue(source.endpoint);
  assert.equal(page.url().includes(source.endpoint), false);

  stage = 'datasource create back to list';
  await page.getByRole('button', { name: 'Back to datasources', exact: true }).click();
  stage = 'datasource create list heading';
  await expect(page.getByRole('heading', { name: 'Datasources', exact: true })).toBeVisible();
  stage = 'datasource create open form';
  await page.getByRole('button', { name: 'Add datasource', exact: true }).click();
  stage = 'datasource create form heading';
  await expect(page.getByRole('heading', { name: 'Add datasource', exact: true })).toBeVisible();
  const createdName = `browser-${fixture.datasource_id.slice(0, 8)}`;
  const createdDisplayName = `Browser datasource ${fixture.datasource_id.slice(0, 8)}`;
  stage = 'datasource create name field';
  await page.getByLabel('Name', { exact: true }).fill(createdName);
  stage = 'datasource create display name field';
  await page.getByLabel('Display name', { exact: true }).fill(createdDisplayName);
  stage = 'datasource create endpoint field';
  await page.getByLabel('Prometheus endpoint', { exact: true }).fill(source.endpoint);
  stage = 'datasource create CA field';
  await page.getByLabel('Custom CA PEM', { exact: true }).fill(fixture.ca_pem);
  stage = 'datasource create agent field';
  await page.getByLabel('Assigned agent', { exact: true }).selectOption(fixture.agent_id);
  stage = 'datasource create response waiter';
  const createPromise = page.waitForResponse((response) => response.request().method() === 'POST'
    && new URL(response.url()).pathname === '/api/v1/datasources');
  stage = 'datasource create submit button';
  await page.getByRole('button', { name: 'Create datasource', exact: true }).click();
  stage = 'datasource create API response';
  const createResponse = await createPromise;
  assert.equal(createResponse.status(), 201);
  const createRequest = createResponse.request();
  assert.match(createRequest.headers()['idempotency-key'], /^[0-9a-f-]{36}$/);
  const createInput = createRequest.postDataJSON();
  assert.equal(createInput.name, createdName);
  assert.equal(createInput.agent_id, fixture.agent_id);
  assert.equal(createInput.tls.ca_pem, fixture.ca_pem);
  assert.deepEqual(createInput.authentication.password_secret, { action: 'clear' });
  assert.deepEqual(createInput.authentication.token_secret, { action: 'clear' });
  const created = await createResponse.json();
  const createdID = created.id;
  assert.match(createdID, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  assert.equal(created.name, createdName);
  assert.equal(created.display_name, createdDisplayName);
  assert.equal(created.version, '1');
  assert.equal('password' in created.authentication, false);
  assert.equal('token' in created.authentication, false);
  stage = 'new UI datasource in real list';
  await expect(page.getByText(createdDisplayName, { exact: true })).toBeVisible();

  stage = 'newly created datasource detail';
  const createdDetailPromise = page.waitForResponse((response) => response.request().method() === 'GET'
    && new URL(response.url()).pathname === `/api/v1/datasources/${createdID}`);
  await page.goto(new URL(`/datasources/${createdID}`, origin).href);
  const createdDetailResponse = await createdDetailPromise;
  assert.equal(createdDetailResponse.status(), 200);
  const createdDetail = await createdDetailResponse.json();
  assert.equal(createdDetail.id, createdID);
  assert.equal(createdDetail.version, '1');

  stage = 'UI datasource update request';
  const nextName = `${createdDisplayName} updated`;
  await page.getByLabel('Display name', { exact: true }).fill(nextName);
  const updatePromise = page.waitForResponse((response) => response.request().method() === 'PUT'
    && new URL(response.url()).pathname === `/api/v1/datasources/${createdID}`);
  await page.getByRole('button', { name: 'Save settings', exact: true }).click();
  const updateResponse = await updatePromise;
  assert.equal(updateResponse.status(), 200);
  const updateRequest = updateResponse.request();
  assert.equal(updateRequest.headers()['if-match'], createdDetailResponse.headers()['etag']);
  const write = updateRequest.postDataJSON();
  assert.deepEqual(write.authentication.password_secret, { action: 'clear' });
  assert.deepEqual(write.authentication.token_secret, { action: 'clear' });
  assert.equal(Object.hasOwn(write.authentication.password_secret, 'value'), false);
  assert.equal(Object.hasOwn(write.authentication.token_secret, 'value'), false);
  const saved = await readResponseJSON(updateResponse);
  assert.equal(saved.display_name, nextName);
  assert.equal(saved.version, String(BigInt(createdDetail.version) + 1n));
  stage = 'UI datasource update confirmation';
  await expect(page.getByRole('status').filter({ hasText: 'Datasource settings saved.' })).toBeVisible();
  await expect(page.getByLabel('Display name', { exact: true })).toHaveValue(nextName);

  stage = 'batch saved Basic and Bearer datasource fixtures';
  const authContext = await page.evaluate(async () => {
    const response = await fetch('/api/v1/auth/context', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}', credentials: 'same-origin', cache: 'no-store',
    });
    return { status: response.status, body: await response.json() };
  });
  assert.equal(authContext.status, 200);
  assert.equal(typeof authContext.body.csrf_token, 'string');
  const createSavedAuthSource = async (record) => page.evaluate(async ({ csrfToken, source }) => {
    const response = await fetch('/api/v1/datasources', {
      method: 'POST', credentials: 'same-origin', cache: 'no-store',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken, 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify(source),
    });
    return { status: response.status, body: await response.json(), etag: response.headers.get('ETag') };
  }, { csrfToken: authContext.body.csrf_token, source: record });
  const authSourcePrefix = `browser-auth-${fixture.datasource_id.slice(0, 8)}`;
  const basicCreate = await createSavedAuthSource({
    name: `${authSourcePrefix}-basic`, display_name: `Browser auth Basic ${fixture.datasource_id.slice(0, 8)}`,
    provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: `${fixture.auth_endpoint}/basic`,
    tls: { insecure_skip_verify: false, ca_pem: fixture.auth_ca_pem },
    authentication: { mode: 'basic', username: 'browser-user', password_secret: { action: 'replace', value: fixture.basic_password }, token_secret: { action: 'clear' } },
    polling_interval_seconds: 60, agent_id: fixture.agent_id,
  });
  assert.equal(basicCreate.status, 201);
  const basicSource = basicCreate.body;
  const basicID = basicSource.id;
  assert.equal(basicSource.authentication.password_configured, true);
  assert.equal(basicSource.authentication.token_configured, false);
  assert.equal('password' in basicSource.authentication, false);
  assert.equal('password_secret' in basicSource.authentication, false);
  const bearerCreate = await createSavedAuthSource({
    name: `${authSourcePrefix}-bearer`, display_name: `Browser auth Bearer ${fixture.datasource_id.slice(0, 8)}`,
    provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: `${fixture.auth_endpoint}/bearer`,
    tls: { insecure_skip_verify: false, ca_pem: fixture.auth_ca_pem },
    authentication: { mode: 'bearer', username: null, password_secret: { action: 'clear' }, token_secret: { action: 'replace', value: fixture.bearer_token } },
    polling_interval_seconds: 60, agent_id: fixture.agent_id,
  });
  assert.equal(bearerCreate.status, 201);
  const bearerSource = bearerCreate.body;
  const bearerID = bearerSource.id;
  assert.equal(bearerSource.authentication.password_configured, false);
  assert.equal(bearerSource.authentication.token_configured, true);
  assert.equal('token' in bearerSource.authentication, false);
  assert.equal('token_secret' in bearerSource.authentication, false);
  assert.equal(basicCreate.etag, `"datasource:${basicID}:1"`);
  assert.equal(bearerCreate.etag, `"datasource:${bearerID}:1"`);

  await page.reload();
  await expect(page.getByRole('heading', { name: nextName, exact: true })).toBeVisible();
  stage = 'UI reports datasource configuration ready';
  await expect(page.getByRole('status').filter({ hasText: 'The assigned agent has applied the datasource settings.' })).toBeVisible({ timeout: 30000 });
  await expect(page.getByRole('button', { name: 'Run preview', exact: true })).toBeEnabled();
  stage = 'public provider readiness contract';
  const publicReadiness = await page.evaluate(async (agentID) => {
    const response = await fetch(`/api/v1/providers?agent_id=${encodeURIComponent(agentID)}`, { credentials: 'same-origin', cache: 'no-store' });
    if (!response.ok) return null;
    const value = await response.json();
    return value;
  }, fixture.agent_id);
  assert.ok(publicReadiness && Array.isArray(publicReadiness.items));
  const prometheusReadiness = publicReadiness.items.find((item) => item.provider === 'prometheus' && item.configuration_format === 'prometheus-http-v1');
  assert.ok(prometheusReadiness);
  assert.equal(prometheusReadiness.availability, 'available');
  assert.match(prometheusReadiness.desired_configuration_revision, /^[1-9][0-9]*$/);
  assert.match(prometheusReadiness.applied_configuration_revision, /^[1-9][0-9]*$/);
  assert.equal(prometheusReadiness.desired_configuration_revision, prometheusReadiness.applied_configuration_revision);
  assert.deepEqual(prometheusReadiness.operations, ['connection_test', 'query_preview']);

  stage = 'real successful saved-source preview';
  const queryField = page.getByLabel('PromQL query', { exact: true });
  await queryField.fill(fixture.query);
  const previewPromise = page.waitForResponse((response) => response.request().method() === 'POST'
    && new URL(response.url()).pathname === `/api/v1/datasources/${createdID}/query-previews`);
  await page.getByRole('button', { name: 'Run preview', exact: true }).click();
  const previewResponse = await previewPromise;
  assert.equal(previewResponse.status(), 202);
  const previewRequest = previewResponse.request();
  assert.equal(previewRequest.postDataJSON().query, fixture.query);
  const previewReceipt = await previewResponse.json();
  assert.match(previewReceipt.id, /^[0-9a-f-]{36}$/);
  stage = 'successful diagnostic result';
  const resultPre = page.locator('.diagnostic-result pre').last();
  await expect.poll(async () => {
    const text = await resultPre.textContent();
    if (!text) return null;
    try { return JSON.parse(text).state; } catch { return null; }
  }, { timeout: 15000 }).toBe('succeeded');
  const result = JSON.parse(await resultPre.textContent());
  assert.equal(result.result_class, 'complete');
  assert.equal(result.series.length, 1);
  assert.deepEqual(result.series[0].labels, {});
  assert.equal(result.series[0].value, '1');
  assert.equal(typeof result.series[0].time, 'string');
  assert.equal(await page.locator('.diagnostic-result').count(), 1);

  stage = 'real failed saved-source preview';
  await queryField.fill('vector(');
  const invalidPreviewPromise = page.waitForResponse((response) => response.request().method() === 'POST'
    && new URL(response.url()).pathname === `/api/v1/datasources/${createdID}/query-previews`);
  await page.getByRole('button', { name: 'Run preview', exact: true }).click();
  const invalidPreview = await invalidPreviewPromise;
  assert.equal(invalidPreview.status(), 202);
  const invalidReceipt = await invalidPreview.json();
  assert.match(invalidReceipt.id, /^[0-9a-f-]{36}$/);
  stage = 'safe failed diagnostic result code';
  await expect.poll(async () => page.locator('.diagnostic-result').last().innerText(), { timeout: 15000 }).toContain('query_failed');
  stage = 'safe failed diagnostic result message';
  await expect(page.locator('.diagnostic-result').last()).toContainText('The datasource rejected the query.');
  assert.equal(await page.locator('.diagnostic-result').count(), 1);
  stage = 'public failed diagnostic payload redaction';
  const publicFailure = await page.evaluate(async (diagnosticID) => {
    const response = await fetch(`/api/v1/diagnostics/${diagnosticID}`, { credentials: 'same-origin', cache: 'no-store' });
    return { status: response.status, body: await response.json() };
  }, invalidReceipt.id);
  assert.equal(publicFailure.status, 200);
  assert.equal(publicFailure.body.state, 'failed');
  assert.equal(publicFailure.body.error_code, 'query_failed');
  assert.equal(publicFailure.body.message, 'The datasource rejected the query.');
  assert.equal(publicFailure.body.result_class, null);
  assert.equal(publicFailure.body.result, null);
  stage = 'diagnostic result secret redaction';
  assert.equal(await page.locator('body').innerText().then((text) => text.includes(passwordForRedaction)), false);

  const waitForDiagnostic = async (expectedState) => expect.poll(async () => {
    const state = await page.locator('.diagnostic-result').last().locator('.datasource-diagnostic-status').textContent().catch(() => null);
    return state?.trim().toLowerCase();
  }, { timeout: 15000 }).toBe(expectedState);
  const assertNoCredentialDrafts = async () => {
    assert.equal(page.url().includes(fixture.basic_password), false);
    assert.equal(page.url().includes(fixture.bearer_token), false);
    assert.deepEqual(await page.locator('input[type="password"]').evaluateAll((inputs) => inputs.map((input) => input.value)), ['']);
    assert.equal(await page.evaluate((values) => Array.from({ length: localStorage.length }, (_, index) => localStorage.getItem(localStorage.key(index) ?? '') ?? '').some((entry) => values.some((value) => entry.includes(value))), [fixture.basic_password, fixture.bearer_token]), false);
  };
  const openSavedDatasource = async (datasourceID) => {
    const detailPromise = page.waitForResponse((response) => response.request().method() === 'GET'
      && new URL(response.url()).pathname === `/api/v1/datasources/${datasourceID}`);
    await page.goto(new URL(`/datasources/${datasourceID}`, origin).href);
    const detail = await detailPromise;
    assert.equal(detail.status(), 200);
    const value = await detail.json();
    assert.equal(value.id, datasourceID);
    assert.equal(detail.headers()['etag'], `"datasource:${datasourceID}:${value.version}"`);
    for (const key of ['password', 'token', 'password_secret', 'token_secret']) {
      assert.equal(Object.hasOwn(value.authentication, key), false);
    }
    return detail;
  };
  const submitSavedConnectionTest = async (datasourceID, expectedVersion) => {
    const path = `/api/v1/datasources/${datasourceID}/connection-tests`;
    const responsePromise = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === path);
    await page.getByRole('button', { name: 'Test connection', exact: true }).click();
    const response = await responsePromise;
    assert.equal(response.status(), 202);
    assert.equal(response.request().headers()['if-match'], expectedVersion);
    assert.match(response.request().headers()['idempotency-key'], /^[\x21-\x7e]{1,128}$/);
    assert.deepEqual(response.request().postDataJSON(), {});
    const receipt = await response.json();
    assert.equal(receipt.operation, 'connection_test');
    await waitForDiagnostic('succeeded');
    return receipt;
  };
  stage = 'saved Basic datasource detail';
  const basicDetail = await openSavedDatasource(basicID);
  await expect(page.getByRole('status').filter({ hasText: 'The assigned agent has applied the datasource settings.' })).toBeVisible({ timeout: 5000 });
  assert.equal((await basicDetail.json()).authentication.password_configured, true);
  stage = 'saved Basic credential connection test';
  await submitSavedConnectionTest(basicID, basicDetail.headers()['etag']);
  await assertNoCredentialDrafts();

  stage = 'saved Bearer datasource detail';
  const bearerDetail = await openSavedDatasource(bearerID);
  await expect(page.getByRole('status').filter({ hasText: 'The assigned agent has applied the datasource settings.' })).toBeVisible({ timeout: 5000 });
  assert.equal((await bearerDetail.json()).authentication.token_configured, true);
  stage = 'saved Bearer credential connection test';
  await submitSavedConnectionTest(bearerID, bearerDetail.headers()['etag']);
  await assertNoCredentialDrafts();

  stage = 'running real preview cancellation';
  await page.getByLabel('PromQL query', { exact: true }).fill('block_until_cancel');
  stage = 'cancel preview submission';
  const slowPreviewPromise = page.waitForResponse((response) => response.request().method() === 'POST'
    && new URL(response.url()).pathname === `/api/v1/datasources/${bearerID}/query-previews`);
  await page.getByRole('button', { name: 'Run preview', exact: true }).click();
  const slowPreview = await slowPreviewPromise;
  assert.equal(slowPreview.status(), 202);
  const slowReceipt = await slowPreview.json();
  activeDiagnosticID = slowReceipt.id;
  latestDiagnostic = { state: 'none', errorCode: 'none', resultClass: 'none' };
  stage = 'cancel button enabled';
  const cancelButton = page.getByRole('button', { name: 'Cancel diagnostic', exact: true });
  await expect(cancelButton).toBeEnabled();
  stage = 'cancel provider started';
  await expect.poll(async () => readFile(join(fixture.artifacts, 'provider-started'), 'utf8').catch(() => ''), { timeout: 5000 }).toBe('started\n');
  const cancelPromise = page.waitForResponse((response) => response.request().method() === 'POST'
    && new URL(response.url()).pathname === `/api/v1/diagnostics/${slowReceipt.id}/cancel`);
  stage = 'cancel request';
  await cancelButton.click();
  const canceledResponse = await cancelPromise;
  stage = 'cancel response status';
  assert.equal(canceledResponse.status(), 200);
  stage = 'cancel response state';
  const canceledBody = await canceledResponse.json();
  if (['pending', 'running', 'succeeded', 'failed', 'canceled', 'timed_out', 'unknown'].includes(canceledBody.state)) cancelResponseState = canceledBody.state;
  assert.equal(canceledBody.state, 'canceled');
  stage = 'cancel result rendering';
  await waitForDiagnostic('canceled');

  stage = 'keyboard skip navigation';
  const skipLink = page.getByRole('link', { name: 'Skip to content', exact: true });
  for (let steps = 0; steps < 30; steps += 1) {
    await page.keyboard.press('Tab');
    if (await skipLink.evaluate((element) => element === document.activeElement)) break;
  }
  await expect(skipLink).toBeFocused();
  await page.keyboard.press('Enter');
  await expect.poll(() => new URL(page.url()).hash).toBe('#main');

  stage = 'light and dark desktop and mobile layouts';
  const selectTheme = async (theme) => {
    const dark = await page.locator('html').evaluate((element) => element.classList.contains('dark'));
    if (dark === (theme === 'dark')) return;
    if (await page.evaluate(() => window.innerWidth <= 860)) {
      await page.getByRole('button', { name: 'Menu', exact: true }).click();
      await page.getByRole('dialog', { name: 'Navigation menu', exact: true })
        .getByRole('button', { name: `Switch to ${theme} theme`, exact: true }).click();
      await page.keyboard.press('Escape');
    } else {
      await page.getByRole('button', { name: `Switch to ${theme} theme`, exact: true }).click();
    }
    await expect.poll(() => page.locator('html').evaluate((element) => element.classList.contains('dark')))
      .toBe(theme === 'dark');
  };
  for (const width of [1440, 360]) {
    await page.setViewportSize({ width, height: 844 });
    for (const theme of ['light', 'dark']) {
      await selectTheme(theme);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth
        && document.body.scrollWidth <= window.innerWidth), true);
      await page.screenshot({ path: join(fixture.artifacts, `datasources-${width}-${theme}.png`), fullPage: true, animations: 'disabled' });
    }
  }
  assert.equal(pageErrors, 0);
  await context.close();
  await browser.close();
  browser = undefined;
  process.stdout.write('PASS browser-datasources\n');
} catch (error) {
  const networkMatch = typeof error?.message === 'string' ? error.message.match(/net::(ERR_[A-Z_]+)/)?.[1] : undefined;
  const networkCode = networkMatch && networkMatch.length <= 40 ? networkMatch : 'none';
  const responseReadClasses = ['body_json_invalid', 'body_resource_missing', 'body_cache_evicted', 'body_read_failed'];
  const errorClass = error?.name === 'ResponseReadError' && responseReadClasses.includes(error.category) ? error.category
    : error?.name === 'TimeoutError' ? 'timeout' : error?.code === 'ERR_ASSERTION' ? 'assertion'
    : typeof error?.message === 'string' && /Response\.(?:body|json):/.test(error.message) ? 'body_unavailable'
    : typeof error?.message === 'string' && /element is not attached|element was detached/i.test(error.message) ? 'element_detached' : 'other';
  const safeAssertionValue = (value) => ['pending', 'running', 'succeeded', 'failed', 'canceled', 'timed_out', 'unknown'].includes(value)
    ? value : Number.isInteger(value) && value >= 100 && value <= 599 ? String(value) : 'none';
  const actual = errorClass === 'assertion' ? safeAssertionValue(error.actual) : 'none';
  const expected = errorClass === 'assertion' ? safeAssertionValue(error.expected) : 'none';
  const line = typeof error?.stack === 'string' ? error.stack.match(/check-datasource-browser\.mjs:(\d{1,4}):\d+/)?.[1] ?? 'none' : 'none';
  await Promise.race([Promise.all([latestAPIRead.catch(() => {}), latestDiagnosticRead.catch(() => {})]), new Promise((resolve) => setTimeout(resolve, 1000))]);
  const safeStage = typeof stage === 'string' ? stage.replace(/[^A-Za-z0-9 _-]/g, '').slice(0, 48) : 'fixture input';
  const status = Number.isInteger(latestAPI.status) && latestAPI.status >= 100 && latestAPI.status <= 599 ? latestAPI.status : 'none';
  const requestID = /^(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/.test(latestAPI.requestID) ? latestAPI.requestID : 'none';
  const apiCode = safeCodes.has(latestAPI.code) ? latestAPI.code : 'none';
  const diagnosticState = ['pending', 'running', 'succeeded', 'failed', 'canceled', 'timed_out', 'unknown'].includes(latestDiagnostic.state) ? latestDiagnostic.state : 'none';
  const diagnosticErrorCode = ['authentication', 'authentication_failed', 'deadline', 'diagnostic_failed', 'incomplete', 'network', 'network_failed', 'query', 'query_failed', 'rate_limited', 'tls', 'tls_failed', 'timeout', 'unsupported', 'assignment_revoked'].includes(latestDiagnostic.errorCode) ? latestDiagnostic.errorCode : 'none';
  const diagnosticResultClass = ['complete', 'empty', 'incomplete'].includes(latestDiagnostic.resultClass) ? latestDiagnostic.resultClass : 'none';
  const diagnostic = `stage=${safeStage}\nnetwork=${networkCode}\nclass=${errorClass}\nsource_line=${line}\nassert_actual=${actual}\nassert_expected=${expected}\ncancel_response_state=${cancelResponseState}\napi_status=${status}\nrequest_id=${requestID}\napi_code=${apiCode}\ndiagnostic_state=${diagnosticState}\ndiagnostic_error_code=${diagnosticErrorCode}\ndiagnostic_result_class=${diagnosticResultClass}\n`;
  if (authenticated && page && typeof fixture?.artifacts === 'string') {
    try {
      await page.screenshot({
        path: join(fixture.artifacts, 'failure.png'),
        timeout: 2000,
        animations: 'disabled',
        fullPage: true,
        mask: [page.locator('input, textarea, [contenteditable="true"], [data-secret-display], [data-secret-value], [class*="secret"]')],
      });
    } catch {}
  }
  if (Buffer.byteLength(diagnostic) <= 512 && typeof fixture?.artifacts === 'string') {
    try { await writeFile(join(fixture.artifacts, 'failure.txt'), diagnostic, { mode: 0o600 }); } catch {}
  }
  const marker = `FAIL browser-datasources stage=${safeStage.replaceAll(' ', '_')} network=${networkCode} class=${errorClass} source_line=${line} assert_actual=${actual} assert_expected=${expected} cancel_response_state=${cancelResponseState} api_status=${status} request_id=${requestID} api_code=${apiCode} diagnostic_state=${diagnosticState} diagnostic_error_code=${diagnosticErrorCode} diagnostic_result_class=${diagnosticResultClass}\n`;
  process.stdout.write(Buffer.byteLength(marker) <= 512 ? marker : 'FAIL browser-datasources stage=none network=none class=other source_line=none assert_actual=none assert_expected=none cancel_response_state=none api_status=none request_id=none api_code=none diagnostic_state=none diagnostic_error_code=none diagnostic_result_class=none\n');
  process.exitCode = 1;
} finally {
  await browser?.close().catch(() => {});
}
