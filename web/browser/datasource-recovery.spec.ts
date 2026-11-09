import { expect, test } from '@playwright/test';

test('pending datasource moves fit the viewport and retain keyboard assignment controls @matrix', async ({ page }, info) => {
  const id = '00000000-0000-4000-8000-000000000001';
  const source = '00000000-0000-4000-8000-000000000002';
  const target = '00000000-0000-4000-8000-000000000003';
  const at = '2026-11-08T12:00:00Z';
  const runtime = { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['collect', 'connection_test', 'query_preview'] }] };
  const agents = [{ id: source, display_name: 'Previous collector for production metrics', enabled: true, runtime }, { id: target, display_name: 'Replacement collector for production metrics', enabled: true, runtime }];
  await page.route('**/api/**', (route) => route.fulfill({ status: 503, json: { code: 'fixture_only' } }));
  await page.route('**/api/v1/auth/context', (route) => route.fulfill({ json: { csrf_token: 'fixture-csrf', session: { user_id: id, login: 'Admin', role: 'administrator', restricted: false } } }));
  await page.route('**/api/v1/agents?*', (route) => route.fulfill({ json: { items: agents, next_cursor: '' } }));
  await page.route(`**/api/v1/datasources/${id}`, (route) => route.fulfill({ headers: { ETag: `"datasource:${id}:2"` }, json: {
    id, name: 'production-metrics', display_name: 'Production metrics', version: '2', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test',
    tls: { insecure_skip_verify: false, ca_pem: null }, authentication: { mode: 'none', username: null, password_configured: false, token_configured: false },
    polling_interval_seconds: 15, settling_delay_seconds: 60, agent_id: target, created_at: at, updated_at: at,
    reassignment: { source_agent_id: source, target_agent_id: target, requested_at: at, authority_expires_at: at, state: 'waiting_for_collection' },
  } }));
  await page.goto(`/datasources/${id}`);
  await expect(page.getByRole('heading', { name: 'Updating datasource', exact: true })).toBeVisible();
  const theme = info.project.name.endsWith('-dark') ? 'dark' : 'light';
  await expect.poll(() => page.locator('html').evaluate((element) => getComputedStyle(element).colorScheme)).toBe(theme);
  const overflow = await page.evaluate(() => [...document.querySelectorAll('body *')].filter((element) => element.getBoundingClientRect().right > innerWidth + 1).map((element) => `${element.tagName}.${element.className}`).slice(0, 12));
  expect(overflow).toEqual([]);
  const assignment = page.getByRole('region', { name: 'Agent', exact: true });
  await assignment.getByRole('button', { name: 'Change', exact: true }).focus();
  await page.keyboard.press('Enter');
  const picker = page.getByRole('combobox', { name: 'Assigned agent', exact: true });
  await expect(picker).toBeFocused();
  await picker.press('ArrowDown');
  await expect(page.getByRole('listbox')).toBeVisible();
  await picker.press('Escape');
  await expect(page.getByRole('listbox')).toHaveCount(0);
  await expect(picker).toBeFocused();
  await page.screenshot({ path: `../.local/datasource-recovery/${info.project.name}.png`, fullPage: true });
});
