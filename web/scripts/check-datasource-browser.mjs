import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { chromium, expect } from '@playwright/test';

let stage = 'fixture input';
let fixture;
let origin;
let browser;
let latestAPI = { status: null, requestID: 'none', code: 'none' };
const safeCodes = new Set([
  'agent_unavailable', 'authentication_unavailable', 'backend_unavailable', 'capability_unavailable',
  'conflict', 'csrf_invalid', 'datasource_unavailable', 'diagnostic_unavailable', 'forbidden',
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
  const page = await context.newPage();
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
    if (response.status() < 400) return;
    response.json().then((body) => {
      if (sequence === apiSequence && body && typeof body === 'object' && safeCodes.has(body.code)) latestAPI.code = body.code;
    }).catch(() => {});
  });

  stage = 'sign-in page';
  await page.goto(fixture.url);
  await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
  await page.getByLabel('Login', { exact: true }).fill(fixture.login);
  await page.getByLabel('Password', { exact: true }).fill(fixture.password);
  await page.getByLabel('Password', { exact: true }).press('Enter');
  fixture.password = '';
  stage = 'authenticated administrator landing';
  await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();

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

  stage = 'UI datasource create page';
  await page.getByRole('button', { name: 'Back to datasources', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Datasources', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Add datasource', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Add datasource', exact: true })).toBeVisible();
  const createdName = `browser-${fixture.datasource_id.slice(0, 8)}`;
  const createdDisplayName = `Browser datasource ${fixture.datasource_id.slice(0, 8)}`;
  await page.getByLabel('Name', { exact: true }).fill(createdName);
  await page.getByLabel('Display name', { exact: true }).fill(createdDisplayName);
  await page.getByLabel('Prometheus endpoint', { exact: true }).fill(source.endpoint);
  await page.getByLabel('Custom CA PEM', { exact: true }).fill(fixture.ca_pem);
  await page.getByLabel('Assigned agent', { exact: true }).selectOption(fixture.agent_id);
  const createPromise = page.waitForResponse((response) => response.request().method() === 'POST'
    && new URL(response.url()).pathname === '/api/v1/datasources');
  await page.getByRole('button', { name: 'Create datasource', exact: true }).click();
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
  const saved = await updateResponse.json();
  assert.equal(saved.display_name, nextName);
  assert.equal(saved.version, String(BigInt(createdDetail.version) + 1n));
  stage = 'UI datasource update confirmation';
  await expect(page.getByRole('status').filter({ hasText: 'Datasource settings saved.' })).toBeVisible();
  await expect(page.getByLabel('Display name', { exact: true })).toHaveValue(nextName);

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

  stage = 'real failed saved-source preview';
  await queryField.fill('vector(');
  const invalidPreviewPromise = page.waitForResponse((response) => response.request().method() === 'POST'
    && new URL(response.url()).pathname === `/api/v1/datasources/${createdID}/query-previews`);
  await page.getByRole('button', { name: 'Run preview', exact: true }).click();
  const invalidPreview = await invalidPreviewPromise;
  assert.equal(invalidPreview.status(), 202);
  const invalidReceipt = await invalidPreview.json();
  assert.match(invalidReceipt.id, /^[0-9a-f-]{36}$/);
  stage = 'safe failed diagnostic result';
  await expect.poll(async () => page.locator('.diagnostic-result').last().innerText(), { timeout: 15000 }).toContain('query_failed');
  await expect(page.locator('.diagnostic-result').last()).toContainText('The datasource rejected the query.');
  await expect(page.locator('.diagnostic-result').last()).toContainText('query_failed');
  assert.equal(await page.locator('body').innerText().then((text) => text.includes(fixture.password)), false);

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
  const networkCode = typeof error?.message === 'string' ? error.message.match(/net::ERR_[A-Z_]+/)?.[0] ?? 'none' : 'none';
  const errorClass = error?.name === 'TimeoutError' ? 'timeout' : 'other';
  const safeStage = typeof stage === 'string' ? stage.replace(/[^A-Za-z0-9 _-]/g, '').slice(0, 96) : 'fixture input';
  const status = Number.isInteger(latestAPI.status) && latestAPI.status >= 100 && latestAPI.status <= 599 ? latestAPI.status : 'none';
  const requestID = /^(?:[0-9a-f]{32}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$/.test(latestAPI.requestID) ? latestAPI.requestID : 'none';
  const apiCode = safeCodes.has(latestAPI.code) ? latestAPI.code : 'none';
  const diagnostic = `stage=${safeStage}\nnetwork=${networkCode}\nclass=${errorClass}\napi_status=${status}\nrequest_id=${requestID}\napi_code=${apiCode}\n`;
  if (Buffer.byteLength(diagnostic) <= 512 && typeof fixture?.artifacts === 'string') {
    try { await writeFile(join(fixture.artifacts, 'failure.txt'), diagnostic, { mode: 0o600 }); } catch {}
  }
  process.stdout.write(`FAIL browser-datasources stage=${safeStage.replaceAll(' ', '_')} network=${networkCode} class=${errorClass} api_status=${status} request_id=${requestID} api_code=${apiCode}\n`);
  process.exitCode = 1;
} finally {
  await browser?.close().catch(() => {});
}
