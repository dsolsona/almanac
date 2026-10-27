import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { writeFile } from 'node:fs/promises';
import { request } from 'node:https';
import { join } from 'node:path';
import { expect } from '@playwright/test';
import { runAgentLifecycleJourney } from './check-agent-lifecycle-browser.mjs';

function tokenEvidence(token) {
  const match = /^almanac_agent_v1_([0-9a-f-]{36})\.([A-Za-z0-9_-]{43})$/.exec(token);
  assert.ok(match);
  const secret = Buffer.from(match[2], 'base64url');
  assert.equal(secret.length, 32);
  return { credential_id: match[1], verifier_sha256: createHash('sha256').update(secret).digest('hex') };
}

// The caller catches all exceptions and writes only a safe stage label. No traces,
// raw HTTP bodies, clipboard values or token-bearing screenshots are retained.
export async function runAgentRegistrationJourney({ page, context, fixture, setStage }) {
  const origin = new URL(fixture.url).origin;
  const expectSelectedAgentURL = async (agentID, stage) => {
    setStage(`${stage}; expected pathname=/agents agent=${agentID}`);
    await expect(page).toHaveURL((url) => url.pathname === '/agents' && url.searchParams.get('agent') === agentID);
  };
  let createRequests = 0;
  let firstKey;
  let firstSemantics;
  page.on('request', (incoming) => {
    if (incoming.method() === 'POST' && new URL(incoming.url()).pathname === '/api/v1/agents') createRequests += 1;
  });
  setStage('agent screen authorization');
  await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
  setStage('agent list first page');
  await expect(page.getByRole('region', { name: 'Agent records', exact: true }).and(page.locator('[tabindex="0"]')).locator('tbody tr')).toHaveCount(50);
  setStage('agent list second page');
  await page.getByRole('button', { name: 'Next agents', exact: true }).click();
  await expect(page.getByRole('button', { name: 'fixture-051', exact: true })).toBeVisible();
  await expect(page).toHaveURL((url) => url.pathname === '/agents' && url.searchParams.has('agents_cursor'));
  setStage('agent list browser back');
  await page.goBack();
  await expect(page).toHaveURL((url) => url.pathname === '/agents' && !url.searchParams.has('agent') && !url.searchParams.has('agents_cursor'));
  await expect(page.getByRole('button', { name: 'fixture-001', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'fixture-001', exact: true }).click();
  const selectedAgentID = '10000000-0000-4000-8000-000000000001';
  await expectSelectedAgentURL(selectedAgentID, 'agent selection URL and browser history');
  const selectedURL = page.url();
  await page.goBack();
  await expect(page).toHaveURL((url) => url.pathname === '/agents' && !url.searchParams.has('agent'));
  await expect(page.getByRole('button', { name: 'fixture-001', exact: true })).toBeVisible();
  await page.goForward();
  await expectSelectedAgentURL(selectedAgentID, 'agent selection browser forward');
  const history = page.getByRole('region', { name: 'Credential history', exact: true }).and(page.locator('[tabindex="0"]'));
  setStage('agent selection browser forward URL confirmed; waiting for credential history region');
  await expect(history).toBeVisible();
  setStage('direct agent selection URL reload');
  await page.goto(selectedURL);
  await expectSelectedAgentURL(selectedAgentID, 'direct agent selection URL reload');
  await expect(page.getByText('10000000-0000-4000-8000-000000000001', { exact: true })).toBeVisible();
  setStage('credential history first page');
  await expect(history.locator('tbody tr')).toHaveCount(50);
  setStage('credential history second page');
  await page.getByRole('button', { name: 'Next credential history', exact: true }).click();
  await expect(page).toHaveURL((url) => url.pathname === '/agents' && url.searchParams.get('agent') === selectedAgentID && url.searchParams.has('credentials_cursor'));
  await expect(history.locator('tbody tr')).toHaveCount(1);
  await expect(history).toContainText('20000000-0000-4000-8000-000000000051');
  setStage('credential history cursor browser history');
  await page.goBack();
  await expect(page).toHaveURL((url) => url.pathname === '/agents' && url.searchParams.get('agent') === selectedAgentID && !url.searchParams.has('credentials_cursor'));
  await expect(history.locator('tbody tr')).toHaveCount(50);
  await page.goForward();
  await expect(page).toHaveURL((url) => url.pathname === '/agents' && url.searchParams.get('agent') === selectedAgentID && url.searchParams.has('credentials_cursor'));
  await expect(history.locator('tbody tr')).toHaveCount(1);
  setStage('credential history reload');
  await page.reload();
  await expect(page).toHaveURL((url) => url.pathname === '/agents' && url.searchParams.get('agent') === selectedAgentID && url.searchParams.has('credentials_cursor'));
  await expect(history.locator('tbody tr')).toHaveCount(1);
  await page.getByRole('button', { name: 'Back to agents', exact: true }).click();
  await expect(page).toHaveURL((url) => url.pathname === '/agents' && !url.searchParams.has('agent'));

  const lost = fixture.scenario === 'agents-lost';
  const unavailable = fixture.scenario === 'agents-unavailable';
  const expectedCreates = lost || unavailable ? 2 : 1;
  const name = lost ? 'browser-lost' : unavailable ? 'browser-unavailable' : 'browser-normal';
  let evidence;
  const selectTheme = async (theme) => {
    const dark = await page.locator('html').evaluate((element) => element.classList.contains('dark'));
    if (dark === (theme === 'dark')) return;
    if (await page.evaluate(() => window.innerWidth <= 860)) {
      await page.getByRole('button', { name: 'Menu', exact: true }).click();
      await page.getByRole('dialog', { name: 'Navigation menu' }).getByRole('button', { name: `Switch to ${theme} theme` }).click();
      await page.keyboard.press('Escape');
    } else {
      await page.getByRole('button', { name: `Switch to ${theme} theme` }).click();
    }
  };
  if (lost) {
    await page.route('**/api/v1/agents', async (route) => {
      try {
      const incoming = route.request();
      if (incoming.method() !== 'POST') { await route.continue(); return; }
      const url = new URL(incoming.url());
      assert.equal(url.origin, origin);
      const headers = await incoming.allHeaders();
      firstKey = headers['idempotency-key'];
      const body = incoming.postDataJSON();
      firstSemantics = { name: body.name, display_name: body.display_name, lifetime_seconds: body.lifetime_seconds };
      const issued = await new Promise((resolve, reject) => {
        const upstream = request(url, { method: 'POST', headers, ca: fixture.certificate, timeout: 5000, agent: false }, (response) => {
          let data = '';
          response.on('data', (chunk) => { data += chunk; if (data.length > 4096) response.destroy(new Error('fixture response bound')); });
          response.on('error', reject);
          response.on('end', () => {
            try { assert.equal(response.statusCode, 201); resolve(JSON.parse(data)); } catch { reject(new Error('fixture issuance failed')); }
          });
        });
        upstream.on('timeout', () => upstream.destroy(new Error('fixture deadline')));
        upstream.on('error', reject);
        upstream.end(incoming.postDataBuffer());
      });
      evidence = { agent_id: issued.agent.id, ...tokenEvidence(issued.token) };
      await route.abort('failed');
      } catch {
        await writeFile(join(fixture.artifacts, 'injection-failure.txt'), 'Committed-response loss injection failed.\n', { mode: 0o600 });
        await route.abort('failed');
      }
    }, { times: 1 });
  }

  if (unavailable) {
    let abortedFirstCreate = false;
    await page.route('**/api/v1/agents', async (route) => {
      if (route.request().method() === 'POST' && !abortedFirstCreate) {
        abortedFirstCreate = true;
        const incoming = route.request();
        const headers = await incoming.allHeaders();
        firstKey = headers['idempotency-key'];
        const body = incoming.postDataJSON();
        firstSemantics = { name: body.name, display_name: body.display_name, lifetime_seconds: body.lifetime_seconds };
        setStage('first registration request intercepted');
        await route.abort('failed');
      }
      else await route.continue();
    }, { times: 1 });
  }

  setStage(lost ? 'committed registration response loss' : unavailable ? 'registration connection failure' : 'fresh registration');
  await page.getByRole('button', { name: 'Register agent', exact: true }).click();
  if (unavailable) setStage('registration dialog opened');
  let registration = page.getByRole('dialog', { name: 'Register agent', exact: true });
  await registration.getByLabel('Agent name', { exact: true }).fill(name);
  await registration.getByLabel('Display name', { exact: true }).fill('Browser acceptance agent');
  let freshResponse = !lost && !unavailable
    ? page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/agents')
    : null;
  await registration.getByRole('button', { name: 'Register agent', exact: true }).click();

  if (unavailable) {
    setStage('waiting for connection feedback');
    await expect(page.getByRole('heading', { name: 'Registration not confirmed', exact: true })).toBeVisible();
    setStage('registration recovery heading visible');
    assert.equal(await page.locator('output[aria-label="Agent token"]').count(), 0);
    const status = page.getByRole('status');
    await expect(status).toContainText('Registration is not confirmed. The agent records currently shown may not include this request.');
    setStage('connection status content verified');
    const currentTheme = await page.locator('html').evaluate((element) => element.classList.contains('dark')) ? 'dark' : 'light';
    await page.screenshot({ path: join(fixture.artifacts, `agent-registration-unavailable-focus-${currentTheme}.png`), fullPage: true, animations: 'disabled' });
    setStage('connection focus screenshot captured');
    setStage('checking settled status focus');
    await expect(status).toBeFocused();
    setStage('connection status focus verified');
    for (const width of [1440, 360]) {
      await page.setViewportSize({ width, height: 900 });
      for (const theme of ['light', 'dark']) {
        await selectTheme(theme);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
        await page.screenshot({ path: join(fixture.artifacts, `agent-registration-unavailable-${width}-${theme}.png`), fullPage: true, animations: 'disabled' });
      }
    }
    await page.setViewportSize({ width: 1440, height: 900 });
    await selectTheme('light');
    setStage('connection feedback screenshots captured');
    await expect(page.locator('section[aria-labelledby="panel-agent-records"]')).toContainText('Registration is not confirmed. The agent records currently shown may not include this request.');
    await expect(page.getByText(/No response was received from Almanac/)).toBeVisible();
    await expect(page.getByLabel('Current password')).toHaveCount(0);
    assert.equal(await page.locator('output[aria-label="Agent token"]').count(), 0);
    assert.equal(createRequests, 1);
    setStage('connection failure same-request retry');
    await page.getByRole('link', { name: 'Agents', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Registration not confirmed', exact: true })).toBeVisible();
    await page.getByRole('button', { name: 'Retry registration', exact: true }).click();
    registration = page.getByRole('dialog', { name: 'Retry registration', exact: true });
    await expect(registration.getByRole('button', { name: 'Retry registration', exact: true })).toBeVisible();
    freshResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/agents');
    await registration.getByRole('button', { name: 'Retry registration', exact: true }).click();
  }

  if (lost) {
    // The safe intent exists while the first POST is still pending. Wait for
    // the client's actual lost-response recovery, not that early intent state.
    await expect(page.getByRole('dialog', { name: 'Retry registration', exact: true })).not.toBeVisible();
    await expect(page.getByRole('heading', { name: 'Registration not confirmed', exact: true })).toBeVisible();
    await expect(page.getByText(/Registration may still have completed/)).toBeVisible();
    assert.equal(createRequests, 1);
    setStage('lost-response retry dialog');
    // Observe rejection immediately: UI assertions below may fail before this
    // waiter settles, and an unhandled rejection would bypass safe diagnostics.
    const retryResponse = page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === '/api/v1/agents').then((response) => ({ response }), () => ({ response: null }));
    registration = page.getByRole('dialog', { name: 'Retry registration', exact: true });
    if (!await registration.isVisible()) await page.getByRole('button', { name: 'Retry registration', exact: true }).click();
    await registration.getByRole('button', { name: 'Retry registration', exact: true }).click();
    setStage('lost-response retry response');
    const { response } = await retryResponse;
    assert.ok(response);
    assert.equal(response.status(), 200);
    const repeated = response.request().postDataJSON();
    assert.equal((await response.request().allHeaders())['idempotency-key'], firstKey);
    assert.deepEqual({ name: repeated.name, display_name: repeated.display_name, lifetime_seconds: repeated.lifetime_seconds }, firstSemantics);
    const result = await response.json();
    assert.equal(result.replayed, true);
    assert.equal(result.secret_state, 'secret_unavailable');
    assert.equal(Object.hasOwn(result, 'token'), false);
    await expect(page.getByRole('dialog', { name: 'Agent token', exact: true })).not.toBeVisible();
    await expect(page.getByRole('status').filter({ hasText: /token.*(?:unavailable|no longer available)/i })).toBeVisible();
    await expect.poll(() => new URL(page.url()).searchParams.get('agent')).toBe(evidence.agent_id);
    await expect(page.getByText(evidence.agent_id, { exact: true })).toBeVisible();
    assert.equal(createRequests, 2);
  } else {
    setStage('one-time token keyboard and clipboard');
    const dialog = page.getByRole('dialog', { name: 'Agent token', exact: true });
    await expect(dialog).toBeVisible();
    setStage('one-time token initial focus');
    await expect(dialog.getByRole('button', { name: 'Close token', exact: true })).toBeFocused();
    setStage('one-time token issuance evidence');
    const token = await dialog.locator('output[aria-label="Agent token"]').textContent();
    assert.ok(token);
    assert.ok(freshResponse);
    const response = await freshResponse;
    assert.equal(response.status(), 201);
    if (unavailable) assert.equal((await response.request().allHeaders())['idempotency-key'], firstKey);
    if (unavailable) {
      const retry = response.request().postDataJSON();
      assert.equal((await response.request().allHeaders())['idempotency-key'], firstKey);
      assert.deepEqual({ name: retry.name, display_name: retry.display_name, lifetime_seconds: retry.lifetime_seconds }, firstSemantics);
    }
    const issued = await response.json();
    evidence = { agent_id: issued.agent.id, ...tokenEvidence(token) };
    assert.equal(evidence.credential_id, issued.credential.id);
    assert.equal(token, issued.token);
    setStage('one-time token clipboard permission');
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin });
    await dialog.getByRole('button', { name: 'Copy token', exact: true }).click();
    setStage('one-time token clipboard equality');
    assert.equal(await page.evaluate(() => navigator.clipboard.readText()), token);
    setStage('one-time token copy announcement');
    await expect(dialog.getByRole('status').filter({ hasText: /^Token copied\.$/ })).toBeVisible();
    setStage('one-time token close and focus restoration');
    if (fixture.scenario === 'agents-close') await dialog.getByRole('button', { name: 'Close token', exact: true }).click();
    else await page.keyboard.press('Escape');
    await expect(dialog).not.toBeVisible();
    await expect(page.getByRole('button', { name: 'Register agent', exact: true })).toBeFocused();
    assert.equal(await page.locator('body').textContent().then((text) => text.includes(token)), false);
    setStage('created agent navigation and reload');
    await expect.poll(() => new URL(page.url()).searchParams.get('agent')).toBe(evidence.agent_id);
    await page.reload();
    await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
    await expect(page.getByText(evidence.agent_id, { exact: true })).toBeVisible();
    await expect(history.locator('tbody tr')).toHaveCount(1);
    await expect(history).toContainText(evidence.credential_id);
    await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
    assert.equal(createRequests, expectedCreates);
    setStage('agent layout and token-free captures');
    for (const width of [1440, 1024, 360]) {
      await page.setViewportSize({ width, height: 900 });
      for (const theme of ['light', 'dark']) {
        await selectTheme(theme);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true);
        await page.screenshot({ path: join(fixture.artifacts, `agent-detail-${width}-${theme}.png`), fullPage: true, animations: 'disabled' });
      }
    }
    if (fixture.scenario === 'agents-lifecycle') {
      await runAgentLifecycleJourney({ page, context, fixture, setStage, registration: evidence, tokenEvidence });
    }
    setStage('real session invalidation and UI reconciliation');
    // Revoke the real session independently of the UI's local authentication
    // state, then exercise the management read/reconciliation boundary.
    const invalidation = await page.evaluate(async () => {
      const response = await fetch('/api/v1/auth/context', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}', credentials: 'same-origin' });
      if (response.status !== 200) return response.status;
      const context = await response.json();
      const logout = await fetch('/api/v1/auth/logout', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token }, body: '{}', credentials: 'same-origin' });
      return logout.status;
    });
    assert.equal(invalidation, 204);
    await page.getByRole('button', { name: 'Back to agents', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
    await expect(page.getByRole('region', { name: 'Credential history', exact: true })).toHaveCount(0);
    await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
    assert.equal(createRequests, expectedCreates);
    await page.getByLabel('Login', { exact: true }).fill(fixture.login);
    await page.getByLabel('Password', { exact: true }).fill(fixture.permanent);
    await page.getByLabel('Password', { exact: true }).press('Enter');
    await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
  }
  assert.ok(evidence?.agent_id);
  await writeFile(join(fixture.artifacts, 'agent-registration.json'), JSON.stringify({ ...evidence, replayed: lost, create_requests: createRequests }), { mode: 0o600 });
}
