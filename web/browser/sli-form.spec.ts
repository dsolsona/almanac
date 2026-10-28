import { expect, test } from '@playwright/test';

const datasource = {
  id: '8ed93d7e-51e3-4a3e-9222-3e8a612de20a', name: 'metrics-prod', display_name: 'Production metrics', version: '4',
  provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test',
  tls: { insecure_skip_verify: false, ca_pem: null },
  authentication: { mode: 'none', username: null, password_configured: false, token_configured: false },
  polling_interval_seconds: 60, settling_delay_seconds: 60, agent_id: 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c',
  created_at: '2026-10-20T12:00:00Z', updated_at: '2026-10-21T12:00:00Z',
};

test('SLI form controls align and fit the viewport', async ({ page }, testInfo) => {
  await page.route('**/api/v1/auth/context', (route) => route.fulfill({ json: {
    csrf_token: 'local-visual-fixture', session: { user_id: 'visual-user', login: 'visual', role: 'administrator', restricted: false },
  } }));
  await page.route('**/api/v1/datasources?*', (route) => route.fulfill({ json: { items: [datasource], next_cursor: '' } }));
  await page.goto('/slis/new');
  const displayName = page.getByLabel('Display name', { exact: true });
  const name = page.getByLabel('SLI name', { exact: true });
  await expect(displayName).toBeVisible();
  await displayName.fill('Request success');
  await expect(name).toHaveValue('request-success');
  const displayBounds = await displayName.boundingBox();
  const nameBounds = await name.boundingBox();
  expect(displayBounds).not.toBeNull();
  expect(nameBounds).not.toBeNull();
  if (!testInfo.project.name.startsWith('mobile')) {
    expect(Math.abs(displayBounds!.y - nameBounds!.y)).toBeLessThanOrEqual(1);
    expect(Math.abs(displayBounds!.height - nameBounds!.height)).toBeLessThanOrEqual(1);
  } else {
    expect(nameBounds!.y).toBeGreaterThan(displayBounds!.y + displayBounds!.height);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(testInfo.project.use.viewport!.width);
  }
  await expect.poll(() => page.locator('html').evaluate((element) => getComputedStyle(element).colorScheme))
    .toBe(testInfo.project.name.endsWith('dark') ? 'dark' : 'light');
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: testInfo.outputPath(`sli-form-${testInfo.project.name}.png`), fullPage: true, animations: 'disabled' });
});
