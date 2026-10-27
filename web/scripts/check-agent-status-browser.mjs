import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { chromium, expect } from '@playwright/test';

let source = '';
for await (const chunk of process.stdin) {
  source += chunk;
  if (source.length > 16 * 1024) throw new Error('fixture input too large');
}
const fixture = JSON.parse(source);
source = '';
const origin = new URL(fixture.url);
assert.equal(origin.protocol, 'https:');
assert.equal(origin.hostname, 'localhost');
assert.match(fixture.spki, /^[A-Za-z0-9+/]{43}=$/);
assert.match(fixture.agent_id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);

let stage = 'browser launch';
let browser;
try {
  browser = await chromium.launch({ headless: true,
    args: [`--ignore-certificate-errors-spki-list=${fixture.spki}`] });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
  context.setDefaultTimeout(4000);
  const page = await context.newPage();
  let pageErrors = 0;
  page.on('pageerror', () => { pageErrors += 1; });

  stage = 'initial navigation';
  await page.goto(fixture.url);
  stage = 'sign-in heading wait';
  await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
  stage = 'login field fill';
  await page.getByLabel('Login', { exact: true }).fill(fixture.login);
  stage = 'password field fill';
  await page.getByLabel('Password', { exact: true }).fill(fixture.password);
  assert.ok((await page.getByLabel('Password', { exact: true }).inputValue()).length > 0);
  fixture.password = '';
  const loginResponsePromise = page.waitForResponse((response) => response.request().method() === 'POST'
    && new URL(response.url()).pathname === '/api/v1/auth/login');
  stage = 'admin sign-in submit';
  await page.getByLabel('Password', { exact: true }).press('Enter');
  stage = 'admin sign-in response';
  const loginResponse = await loginResponsePromise;
  let loginCode = 'none';
  try {
    const result = await loginResponse.json();
    if (typeof result.code === 'string' && /^[a-z_]{1,64}$/.test(result.code)) loginCode = result.code;
  } catch { /* Successful responses need not contain an error code. */ }
  const loginStatus = loginResponse.status();
  const pathname = await page.evaluate(() => location.pathname);
  const safePath = pathname === '/' || pathname === '/agents' ? pathname : 'unknown';
  stage = `administrator landing after login status=${loginStatus} code=${loginCode} path=${safePath}`;
  assert.equal(loginStatus, 200);
  await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();

  stage = 'real agent detail route and API response';
  const detailURL = new URL(`/agents?agent=${encodeURIComponent(fixture.agent_id)}`, origin);
  const detailResponsePromise = page.waitForResponse((response) => response.request().method() === 'GET'
    && new URL(response.url()).pathname === `/api/v1/agents/${fixture.agent_id}`);
  await page.goto(detailURL.href);
  const detailResponse = await detailResponsePromise;
  stage = `agent detail HTTP status=${detailResponse.status()}`;
  assert.equal(detailResponse.status(), 200);
  const detail = await detailResponse.json();
  const runtime = detail.runtime;
  stage = 'agent detail runtime present';
  assert.ok(runtime);
  stage = 'agent detail reachability connected';
  assert.equal(runtime.reachability, 'connected');
  stage = 'agent detail desired revision one';
  assert.equal(runtime.desired_configuration_revision, '1');
  stage = 'agent detail applied revision one';
  assert.equal(runtime.applied_configuration_revision, '1');
  stage = 'agent detail no-assignment explanation';
  assert.equal(runtime.collection_explanation, 'no_assignment');
  stage = 'agent detail Prometheus diagnostic capability';
  assert.deepEqual(runtime.capabilities, [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test', 'query_preview'] }]);
  stage = 'agent detail absent collection attempt';
  assert.equal(runtime.last_attempt_at, null);
  stage = 'agent detail absent successful query';
  assert.equal(runtime.last_successful_query_at, null);
  stage = 'agent detail absent expected execution';
  assert.equal(runtime.next_expected_execution_at, null);

  stage = 'agent runtime panel rendering';
  const panel = page.getByRole('region', { name: 'Agent runtime', exact: true });
  await expect(panel).toBeVisible();
  stage = 'agent runtime connected label';
  await expect(panel.locator('.agents-runtime__state')).toContainText('Connected');
  const runtimeField = (label) => panel.locator('.agents-runtime__meta > div')
    .filter({ has: page.getByText(label, { exact: true }) }).locator('dd');
  stage = 'agent runtime desired revision label';
  await expect(runtimeField('Desired configuration revision')).toHaveText('1');
  stage = 'agent runtime applied revision label';
  await expect(runtimeField('Applied configuration revision')).toHaveText('1');
  stage = 'agent runtime collection explanation label';
  await expect(runtimeField('Collection explanation')).toHaveText('No datasources are assigned to this agent.');
  stage = 'agent runtime collection attempt label';
  await expect(runtimeField('Last collection attempt')).toHaveText('Not reported');
  stage = 'agent runtime successful query label';
  await expect(runtimeField('Last successful query')).toHaveText('No successful query has been reported.');
  stage = 'agent runtime expected execution label';
  await expect(runtimeField('Next expected execution')).toHaveText('Not reported');
  stage = 'agent runtime capabilities label';
  stage = 'agent runtime Prometheus capability rendering';
  const prometheusCapability = panel.locator('.agents-runtime__capabilities li').filter({ has: page.getByText('prometheus', { exact: true }) });
  await expect(prometheusCapability).toContainText('Configuration formats: prometheus-http-v1');
  await expect(prometheusCapability).toContainText('Operations: connection_test, query_preview');

  stage = 'keyboard skip navigation';
  const skipLink = page.getByRole('link', { name: 'Skip to content', exact: true });
  // The application deliberately focuses its heading after authentication.
  // Verify keyboard reachability from that position, not an assumed body focus.
  for (let steps = 0; steps < 30; steps += 1) {
    await page.keyboard.press('Tab');
    if (await skipLink.evaluate((element) => element === document.activeElement)) break;
  }
  await expect(skipLink).toBeFocused();
  await page.keyboard.press('Enter');
  await expect.poll(() => new URL(page.url()).hash).toBe('#main');

  stage = 'four viewport and theme screenshots';
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
    await page.setViewportSize({ width, height: 900 });
    for (const theme of ['light', 'dark']) {
      await selectTheme(theme);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth
        && document.body.scrollWidth <= window.innerWidth), true);
      await page.screenshot({ path: join(fixture.artifacts, `agent-status-${width}-${theme}.png`),
        fullPage: true, animations: 'disabled' });
    }
  }
  assert.equal(pageErrors, 0);
  await context.close();
  await browser.close();
  browser = undefined;
  process.stdout.write('PASS browser-agent-status\n');
} catch (error) {
  const networkCode = typeof error?.message === 'string'
    ? error.message.match(/net::ERR_[A-Z_]+/)?.[0] ?? 'none' : 'none';
  const errorClass = error?.name === 'TimeoutError' ? 'timeout' : 'other';
  await writeFile(join(fixture.artifacts, 'failure.txt'), `${stage}\nnetwork=${networkCode} class=${errorClass}\n`, { mode: 0o600 });
  process.stdout.write('FAIL browser-agent-status\n');
  process.exitCode = 1;
} finally {
  await browser?.close().catch(() => {});
}
