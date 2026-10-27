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
  assert.equal(detailResponse.status(), 200);
  const detail = await detailResponse.json();
  const runtime = detail.runtime;
  assert.ok(runtime);
  assert.equal(runtime.reachability, 'connected');
  assert.equal(runtime.desired_configuration_revision, '1');
  assert.equal(runtime.applied_configuration_revision, '1');
  assert.equal(runtime.collection_explanation, 'no_assignment');
  assert.deepEqual(runtime.capabilities, []);
  assert.equal(runtime.last_attempt_at, null);
  assert.equal(runtime.last_successful_query_at, null);
  assert.equal(runtime.next_expected_execution_at, null);

  const panel = page.getByRole('region', { name: 'Agent runtime', exact: true });
  await expect(panel).toBeVisible();
  await expect(panel.locator('.agents-runtime__state')).toContainText('Connected');
  const runtimeField = (label) => panel.locator('.agents-runtime__meta > div')
    .filter({ has: panel.locator('dt', { hasText: label }) }).locator('dd');
  await expect(runtimeField('Desired configuration revision')).toHaveText('1');
  await expect(runtimeField('Applied configuration revision')).toHaveText('1');
  await expect(runtimeField('Collection explanation')).toHaveText('No datasources are assigned to this agent.');
  await expect(runtimeField('Last collection attempt')).toHaveText('Not reported');
  await expect(runtimeField('Last successful query')).toHaveText('No successful query has been reported.');
  await expect(runtimeField('Next expected execution')).toHaveText('Not reported');
  await expect(panel.getByText('No provider capabilities were reported.', { exact: true })).toBeVisible();

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
