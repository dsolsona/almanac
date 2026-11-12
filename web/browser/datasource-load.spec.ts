import { expect, test, type Route } from '@playwright/test';
import { readFile } from 'node:fs/promises';

test('datasource code loads during session validation while protected reads wait', async ({ page }) => {
  const html = await readFile('dist/index.html', 'utf8');
  const session = Promise.withResolvers<Route>();
  let datasourceRequested = false;
  let protectedReads = 0;
  page.on('request', request => {
    if (/\/DatasourcesPage-[\w-]+\.js$/.test(new URL(request.url()).pathname)) datasourceRequested = true;
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
    if (path.startsWith('/api/v1/')) {
      if (['/api/v1/agents', '/api/v1/datasources', '/api/v1/slis'].includes(path)) protectedReads++;
      await route.fulfill({ json: { items: [], next_cursor: '' } });
      return;
    }
    await route.continue();
  });
  await page.goto('/datasources');
  const pendingSession = await session.promise;
  try {
    await expect.poll(() => datasourceRequested).toBe(true);
    expect(protectedReads).toBe(0);
  } finally {
    await pendingSession.fulfill({ json: { csrf_token: 'fixture-only', session: { user_id: '00000000-0000-4000-8000-000000000001', login: 'fixture-admin', role: 'administrator', restricted: false } } });
  }
  await expect(page.getByRole('heading', { name: 'No datasources yet' })).toBeVisible();
  expect(protectedReads).toBeGreaterThan(0);
});
