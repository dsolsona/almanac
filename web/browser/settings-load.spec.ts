import { expect, test, type Route } from '@playwright/test';
import { readFile } from 'node:fs/promises';

test('settings code loads during session validation without requesting protected data', async ({ page }) => {
  const html = await readFile('dist/index.html', 'utf8');
  const session = Promise.withResolvers<Route>();
  const id = '00000000-0000-4000-8000-000000000001';
  let settingsRequested = false;
  let accountReads = 0;
  page.on('request', request => {
    if (/\/SettingsPage-[\w-]+\.js$/.test(new URL(request.url()).pathname)) settingsRequested = true;
  });
  await page.route('**/*', async route => {
    if (route.request().isNavigationRequest()) {
      await route.fulfill({ contentType: 'text/html', body: html });
      return;
    }
    const path = new URL(route.request().url()).pathname;
    if (path.startsWith('/assets/')) {
      await route.fulfill({ contentType: path.endsWith('.css') ? 'text/css' : 'text/javascript', body: await readFile(`dist${path}`) });
      return;
    }
    if (path === '/api/v1/auth/context') {
      session.resolve(route);
      return;
    }
    if (path === '/api/v1/account') {
      accountReads++;
      await route.fulfill({ json: { id, full_name: 'Fixture admin', email: 'admin@example.test', role: 'administrator', status: 'active', version: '1', last_sign_in: null, sign_in_history_known: false, is_last_admin: true } });
      return;
    }
    if (path.startsWith('/api/')) {
      await route.fulfill({ status: 202, json: {} });
      return;
    }
    await route.continue();
  });
  await page.goto('/settings/account');
  const pendingSession = await session.promise;
  try {
    await expect.poll(() => settingsRequested).toBe(true);
    expect(accountReads).toBe(0);
  } finally {
    await pendingSession.fulfill({ json: { csrf_token: 'fixture-only', session: { user_id: id, login: 'fixture-admin', role: 'administrator', restricted: false } } });
  }
  await expect(page.getByRole('heading', { name: 'Your account', exact: true })).toBeVisible();
  await expect(page.getByLabel('Full name', { exact: true })).toHaveValue('Fixture admin');
  expect(accountReads).toBe(1);
});
