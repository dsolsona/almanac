import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import { request } from 'node:https';
import { join } from 'node:path';
import { chromium, expect } from '@playwright/test';
import { runAgentRegistrationJourney } from './check-agent-browser.mjs';

let source = '';
for await (const chunk of process.stdin) {
  source += chunk;
  if (source.length > 16 * 1024) throw new Error('fixture input too large');
}
const fixture = JSON.parse(source);
source = '';
assert.ok(['onboarding', 'lost-login', 'lost-password', 'agents', 'agents-close', 'agents-lost', 'agents-unavailable', 'agents-lifecycle'].includes(fixture.scenario));
const origin = new URL(fixture.url);
assert.equal(origin.protocol, 'https:');
assert.equal(origin.hostname, 'localhost');
assert.equal(origin.pathname, '/');
assert.match(fixture.spki, /^[A-Za-z0-9+/]{43}=$/);

let stage = 'launch';
let browser;
let page;
let failed = false;
const counts = { login: 0, password: 0, logout: 0 };
const managementReads = [];

// Exercise the browser's normal same-origin GET behavior against the real API.
// Requiring an Origin header here would break fetch even for a valid session.
async function inspectManagementRead(expectedStatus) {
  const [incoming, result] = await Promise.all([
    page.waitForRequest((req) => req.method() === 'GET' && new URL(req.url()).pathname === '/api/v1/agents'),
    page.evaluate(async () => {
      const response = await fetch('/api/v1/agents', { credentials: 'same-origin' });
      return { status: response.status, cache: response.headers.get('cache-control'), body: await response.json() };
    }),
  ]);
  assert.equal((await incoming.allHeaders()).origin, undefined);
  assert.equal(result.status, expectedStatus);
  assert.equal(result.cache, 'no-store');
  if (expectedStatus === 200) assert.deepEqual(result.body, { items: [], next_cursor: '' });
  else assert.equal(result.body.code, expectedStatus === 403 ? 'forbidden' : 'unauthenticated');
  managementReads.push({ status: expectedStatus, originHeaderPresent: false });
}

// Send the real mutation with the fixture CA, then deliberately lose its response.
// This is fault injection after the actual backend transaction, not an API stub.
async function commitThenDrop(route) {
  const incoming = route.request();
  const url = new URL(incoming.url());
  assert.equal(url.origin, origin.origin);
  const headers = await incoming.allHeaders();
  const status = await new Promise((resolve, reject) => {
    const upstream = request(url, {
      method: incoming.method(), headers, ca: fixture.certificate,
      timeout: 5000, agent: false,
    }, (response) => {
      response.resume();
      response.on('end', () => resolve(response.statusCode));
      response.on('error', reject);
    });
    upstream.on('timeout', () => upstream.destroy(new Error('fixture deadline')));
    upstream.on('error', reject);
    upstream.end(incoming.postDataBuffer());
  });
  assert.equal(status, 200);
  await route.abort('failed');
}

async function heading(name) {
  await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
}

async function selectTheme(theme) {
  const dark = await page.locator('html').evaluate((element) => element.classList.contains('dark'));
  if (dark === (theme === 'dark')) return;
  if (await page.evaluate(() => window.innerWidth <= 860) && await page.getByRole('button', { name: 'Menu', exact: true }).count()) {
    await page.getByRole('button', { name: 'Menu', exact: true }).click();
    await page.getByRole('dialog', { name: 'Navigation menu' }).getByRole('button', { name: `Switch to ${theme} theme` }).click();
    await page.keyboard.press('Escape');
  } else {
    await page.getByRole('button', { name: `Switch to ${theme} theme` }).click();
  }
}

async function signOut() {
  if (await page.evaluate(() => window.innerWidth <= 860)) {
    await page.getByRole('button', { name: 'Menu', exact: true }).click();
  }
  await page.getByRole('button', { name: 'Sign out', exact: true }).click();
}

async function signIn(password) {
  await page.getByLabel('Login', { exact: true }).fill(fixture.login);
  await page.getByLabel('Password', { exact: true }).fill(password);
  await page.getByLabel('Password', { exact: true }).press('Enter');
}

async function setPassword(setStage) {
  setStage('password rotation current password input');
  await page.getByLabel('Current password', { exact: true }).fill(fixture.initial);
  setStage('password rotation new password input');
  await page.getByLabel('New password', { exact: true }).fill(fixture.permanent);
  setStage('password rotation confirmation input');
  await page.getByLabel('Confirm new password', { exact: true }).fill(fixture.permanent);
  setStage('password rotation submit');
  await page.getByRole('button', { name: 'Set password', exact: true }).click();
}

async function inspectLayouts(label) {
  for (const width of [1440, 1024, 360]) {
    await page.setViewportSize({ width, height: 900 });
    for (const theme of ['light', 'dark']) {
      await selectTheme(theme);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
      assert.equal(await page.locator('input').evaluateAll((inputs) => inputs.every((input) => input.value === '')), true);
      await page.screenshot({ path: join(fixture.artifacts, `${label}-${width}-${theme}.png`),
        fullPage: true, animations: 'disabled' });
    }
  }
}

try {
  browser = await chromium.launch({ headless: true,
    args: [`--ignore-certificate-errors-spki-list=${fixture.spki}`] });
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: 'light' });
  context.setDefaultTimeout(4000);
  page = await context.newPage();
  let pageErrors = 0;
  page.on('pageerror', () => { pageErrors += 1; });
  page.on('request', (req) => {
    const path = new URL(req.url()).pathname;
    if (req.method() === 'POST' && path === '/api/v1/auth/login') counts.login += 1;
    if (req.method() === 'POST' && path === '/api/v1/auth/password') counts.password += 1;
    if (req.method() === 'POST' && path === '/api/v1/auth/logout') counts.logout += 1;
  });
  stage = 'initial navigation';
  await page.goto(fixture.url);
  stage = 'initial sign-in';
  await heading('Sign in');
  await page.getByRole('link', { name: 'Skip to content' }).focus();
  await expect(page.getByRole('link', { name: 'Skip to content' })).toBeFocused();
  await page.keyboard.press('Enter');
  if (fixture.scenario === 'onboarding') await inspectLayouts('signin');

  if (fixture.scenario === 'lost-login') {
    stage = 'lost login response';
    await page.route('**/api/v1/auth/login', commitThenDrop, { times: 1 });
    await signIn(fixture.initial);
    await expect(page.getByRole('alert')).toContainText(/may have completed/i);
    await heading('Sign in');
    assert.equal(counts.login, 1);
    assert.equal(counts.password, 0);
  }
  stage = 'restricted sign-in';
  await signIn(fixture.initial);
  await heading('Set your permanent password');
  await page.reload();
  await heading('Set your permanent password');
  await expect(page.getByLabel('Current password', { exact: true })).toHaveValue('');
  if (fixture.scenario === 'onboarding') {
    stage = 'restricted management read without Origin';
    await inspectManagementRead(403);
    await inspectLayouts('restricted');
    stage = 'restricted draft logout and sign-in';
    await page.getByLabel('Current password', { exact: true }).fill(fixture.initial);
    await page.getByLabel('New password', { exact: true }).fill('unsubmitted draft password');
    await page.getByLabel('Confirm new password', { exact: true }).fill('unsubmitted draft password');
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await heading('Sign in');
    await signIn(fixture.initial);
    await heading('Set your permanent password');
    await expect(page.getByLabel('Current password', { exact: true })).toHaveValue('');
    await expect(page.getByLabel('New password', { exact: true })).toHaveValue('');
    await expect(page.getByLabel('Confirm new password', { exact: true })).toHaveValue('');
    assert.equal(counts.password, 0);
    stage = 'invalid new password';
    await page.getByLabel('Current password', { exact: true }).fill(fixture.initial);
    await page.getByLabel('New password', { exact: true }).fill('short');
    await page.getByLabel('Confirm new password', { exact: true }).fill('short');
    await page.getByRole('button', { name: 'Set password', exact: true }).click();
    await expect(page.getByLabel('New password', { exact: true })).toHaveAttribute('aria-invalid', 'true');
    assert.equal(counts.password, 0);
  }
  if (fixture.scenario === 'lost-password') {
    await page.route('**/api/v1/auth/password', commitThenDrop, { times: 1 });
  }
  await setPassword((value) => { stage = value; });
  if (fixture.scenario === 'lost-password') {
    await expect(page.getByRole('alert')).toContainText(/may have completed/i);
    await heading('Sign in');
    assert.equal(counts.password, 1);
    assert.equal(counts.login, 1);
    await signIn(fixture.permanent);
  }
  stage = 'password rotation Agents heading';
  await heading('Agents');
  stage = 'password rotation canonical route';
  await expect(page).toHaveURL((url) => url.pathname === '/agents');
  stage = 'password rotation mutation count';
  assert.equal(counts.password, 1);
  stage = 'password rotation reload';
  await page.reload();
  stage = 'password rotation Agents heading after reload';
  await heading('Agents');
  stage = 'account identity after reload';
  const mobileAccount = await page.evaluate(() => window.innerWidth <= 860);
  if (mobileAccount) await page.getByRole('button', { name: 'Menu', exact: true }).click();
  await expect(page.getByText(fixture.login, { exact: true })).toBeVisible();
  if (mobileAccount) await page.keyboard.press('Escape');
  if (fixture.scenario === 'onboarding') {
    stage = 'administrator management read without Origin';
    await inspectManagementRead(200);
    stage = 'administrator agent records loaded';
    await expect(page.getByText('No agents have been registered.', { exact: true })).toBeVisible();
  }
  if (fixture.scenario === 'onboarding') await inspectLayouts('signed-in');
  if (fixture.scenario.startsWith('agents')) {
    await runAgentRegistrationJourney({ page, context, fixture, setStage: (value) => { stage = value; } });
  }
  const cookies = await context.cookies();
  assert.ok(cookies.some((cookie) => cookie.name.startsWith('__Host-') && cookie.httpOnly && cookie.secure && cookie.path === '/' && cookie.sameSite === 'Lax'));
  assert.equal(await page.evaluate(() => document.cookie), '');
  const storage = await page.evaluate(() => ({ local: Object.keys(localStorage), session: Object.keys(sessionStorage) }));
  assert.ok(storage.local.every((key) => key === 'almanac-theme'));
  assert.deepEqual(storage.session, []);
  if (fixture.scenario === 'onboarding') {
    stage = 'mobile sign-out uncertain response';
    const logoutCount = counts.logout;
    await page.route('**/api/v1/auth/logout', (route) => route.abort('failed'), { times: 1 });
    await signOut();
    const logoutAlert = page.getByRole('alert');
    await expect(logoutAlert).toContainText(/sign-out could not be confirmed/i);
    assert.equal(counts.logout, logoutCount + 1);
    await expect(logoutAlert).toBeFocused();
    await expect(page.getByRole('dialog', { name: 'Navigation menu' })).toHaveCount(0);
    await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
    stage = 'mobile sign-out session recovery';
    await page.getByRole('button', { name: 'Menu', exact: true }).click();
    await page.getByRole('dialog', { name: 'Navigation menu' }).getByRole('button', { name: 'Check session', exact: true }).click();
    await heading('Agents');
    await expect(page.getByRole('alert')).toContainText(/sign-out could not be confirmed/i);
    await expect(page.getByRole('dialog', { name: 'Navigation menu' })).toHaveCount(0);
    await inspectManagementRead(200);
  }
  stage = 'logout and credential replacement';
  await signOut();
  await heading('Sign in');
  if (fixture.scenario === 'onboarding') {
    stage = 'signed-out management read without Origin';
    await inspectManagementRead(401);
  }
  await signIn(fixture.initial);
  await expect(page.getByRole('alert')).toBeVisible();
  await heading('Sign in');
  await expect(page.getByLabel('Password', { exact: true })).toHaveValue('');
  await signIn(fixture.permanent);
  await heading('Agents');
  await signOut();
  await heading('Sign in');
  assert.equal(pageErrors, 0);
  await writeFile(join(fixture.artifacts, 'evidence.json'), JSON.stringify({
    scenario: fixture.scenario, chromium: browser.version(), node: process.version,
    completed: true, mutationCounts: counts,
    managementReads,
    inspectedWidths: ['onboarding', 'agents', 'agents-close', 'agents-unavailable', 'agents-lifecycle'].includes(fixture.scenario) ? [1440, 1024, 360] : [1440],
    inspectedThemes: ['onboarding', 'agents', 'agents-close', 'agents-unavailable', 'agents-lifecycle'].includes(fixture.scenario) ? ['light', 'dark'] : ['light'],
  }, null, 2), { mode: 0o600 });
  stage = 'complete';
} catch (error) {
  failed = true;
  // Do not emit Playwright exceptions: their call logs may contain submitted text.
  const networkCode = /net::ERR_[A-Z_]+/.exec(String(error))?.[0] ?? 'none';
  const safeLocation = await page?.evaluate(() => {
    const agent = new URL(location.href).searchParams.get('agent') ?? '';
    return { pathname: location.pathname, agent: /^[0-9a-f-]{36}$/.test(agent) ? agent : '(none or invalid)' };
  }).catch(() => ({ pathname: 'unavailable', agent: 'unavailable' })) ?? { pathname: 'unavailable', agent: 'unavailable' };
  await writeFile(join(fixture.artifacts, 'failure.txt'), `Failed stage: ${stage}\nActual pathname: ${safeLocation.pathname}\nActual agent: ${safeLocation.agent}\nNetwork code: ${networkCode}\nLogin requests: ${counts.login}\nPassword requests: ${counts.password}\n`, { mode: 0o600 });
  try {
    if (!fixture.scenario.startsWith('agents')) {
      await page?.screenshot({ path: join(fixture.artifacts, 'failure.png'),
        mask: [page.locator('input')], timeout: 1000 });
    }
  } catch { /* A closed browser still has the sanitized stage diagnostic. */ }
} finally {
  try { await browser?.close(); } catch { failed = true; }
}
if (failed) process.exitCode = 1;
else console.log(`PASS browser-auth ${fixture.scenario}`);
