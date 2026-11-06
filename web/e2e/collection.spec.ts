import { expect, test, type Page } from '@playwright/test';
import { metricsURL, startAgent } from './support/stack.ts';
import { api, unique } from './support/api.ts';

// The product's core journey, in the order a new operator follows it: register an agent and run it,
// connect a Prometheus-compatible datasource through it, define an SLI and see real collected data,
// then set an SLO on it and read its evaluation. Each step builds on the previous one.
test.describe.configure({ mode: 'serial' });

const agentName = unique('collector');
const datasourceName = unique('Metrics');
const sliName = unique('Request success');
const sloName = unique('Request availability');
let agentID = '';
let datasourceID = '';

// The agent keeps running after this file: the restart journey checks that it reconnects.
// scripts/e2e.sh removes it with the stack.

async function reloadUntil(page: Page, check: () => Promise<void>, timeout = 120_000) {
  await expect(async () => { await page.reload(); await check(); }).toPass({ timeout, intervals: [2_000, 5_000] });
}

test('register an agent in the UI and see it connect', async ({ page }) => {
  await page.goto('/agents');
  await page.getByRole('button', { name: 'Register agent', exact: true }).click();
  const dialog = page.getByRole('dialog', { name: 'Register agent', exact: true });
  await dialog.getByLabel('Agent name (identifier)', { exact: true }).fill(agentName);
  await dialog.getByLabel('Display name', { exact: true }).fill(agentName);
  await dialog.getByRole('button', { name: 'Register agent', exact: true }).click();

  const token = page.locator('output[aria-label="Agent token"]');
  await expect(token).toBeVisible();
  const credential = (await token.textContent())!.trim();
  expect(credential.length).toBeGreaterThan(20);
  await page.getByRole('button', { name: 'Close token', exact: true }).click();
  await expect(token).toHaveCount(0);
  const agents = await api(page, 'GET', '/api/v1/agents?limit=200');
  agentID = agents.body.items.find((item: { name: string }) => item.name === agentName).id;

  startAgent(credential);
  await reloadUntil(page, async () => {
    await expect(page.getByText(/Connected · heartbeat/)).toBeVisible({ timeout: 2_000 });
  });
});

test('connect a datasource through the agent, test it and preview real metrics', async ({ page }) => {
  // A real series in the stack's VictoriaMetrics, so the preview proves the agent queried it.
  // Stamped a minute ago: VictoriaMetrics hides the newest 30 s from queries (search.latencyOffset).
  const series = `e2e_requests_total{journey="${datasourceName}"} 4242 ${Date.now() - 60_000}\n`;
  const pushed = await fetch(`${metricsURL}/api/v1/import/prometheus`, { method: 'POST', body: series });
  expect(pushed.ok).toBe(true);
  // VictoriaMetrics makes imported samples searchable after a short flush; wait for that, not a fixed delay.
  await expect.poll(async () => (await (await fetch(`${metricsURL}/api/v1/query?query=${encodeURIComponent(`e2e_requests_total{journey="${datasourceName}"}`)}`)).json()).data.result.length, { timeout: 30_000 }).toBe(1);

  await page.goto('/datasources');
  await page.getByRole('button', { name: 'New datasource', exact: true }).click();
  await page.getByLabel('Display name', { exact: true }).fill(datasourceName);
  await page.getByLabel('Prometheus endpoint', { exact: true }).fill('http://victoriametrics:8428');
  // With one connected agent the form preselects it; otherwise pick ours from the picker.
  if (await page.getByLabel('Assigned agent', { exact: true }).count()) {
    await page.getByLabel('Assigned agent', { exact: true }).click();
    await page.locator(`#datasource-agent-option-${agentID}`).click();
  }
  await expect(page.getByRole('region', { name: 'Agent', exact: true })).toContainText(agentName);
  await page.getByRole('radio', { name: '15 s', exact: true }).click();
  await page.getByRole('button', { name: 'Create datasource', exact: true }).click();

  await expect(page.getByRole('heading', { name: datasourceName, exact: true })).toBeVisible();
  datasourceID = new URL(page.url()).pathname.split('/').pop()!;
  await reloadUntil(page, async () => {
    await expect(page.getByText(/agent has the current settings/)).toBeVisible({ timeout: 2_000 });
  }, 60_000);

  await page.getByRole('button', { name: 'Test connection' }).first().click();
  await expect(page.locator('.diagnostic-result').first()).toContainText('Succeeded', { timeout: 30_000 });

  await page.getByLabel('PromQL query', { exact: true }).fill(`e2e_requests_total{journey="${datasourceName}"}`);
  await page.getByRole('button', { name: 'Run preview', exact: true }).click();
  const preview = page.locator('.diagnostic-result').last();
  await expect(preview).toContainText('Succeeded', { timeout: 30_000 });
  await expect(preview).toContainText('4242');
});

test('define an SLI and see its first collected interval', async ({ page }) => {
  test.setTimeout(240_000);
  await page.goto('/slis');
  await page.getByRole('button', { name: 'New SLI', exact: true }).click();
  await page.getByLabel('Display name', { exact: true }).fill(sliName);
  // With one datasource the form preselects it; otherwise pick ours from the picker.
  if (await page.getByLabel('Datasource', { exact: true }).count()) {
    await page.getByLabel('Datasource', { exact: true }).click();
    await page.locator(`#sli-datasource-option-${datasourceID}`).click();
  }
  await expect(page.locator('.ui-summary').filter({ hasText: datasourceName })).toBeVisible();
  await page.getByLabel('Good events', { exact: true }).fill('vector(99)');
  await page.getByLabel('Total events', { exact: true }).fill('vector(100)');
  await page.getByRole('button', { name: 'Create SLI', exact: true }).click();
  await expect(page.getByRole('heading', { name: sliName, exact: true })).toBeVisible();

  // Settling is fixed at 60 s and the cadence is 15 s, so the first interval lands within ~90 s.
  await reloadUntil(page, async () => {
    await expect(page.locator('.sli-counts strong')).toHaveText(['99', '100'], { timeout: 2_000 });
  }, 180_000);

  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'SLIs' }).click();
  const row = page.getByRole('row').filter({ hasText: sliName });
  await expect(row.getByText('Up to date')).toBeVisible();
  await expect(row.locator('td[data-label="Last sample"] b')).toHaveText('99%');
  await expect(row.getByRole('link', { name: 'Create SLO' })).toBeVisible();
});

test('rename the SLI from its page', async ({ page }) => {
  await page.goto('/slis');
  await page.getByRole('link', { name: sliName, exact: true }).click();
  await page.getByRole('button', { name: 'Edit SLI', exact: true }).first().click();
  const dialog = page.getByRole('dialog', { name: 'Edit SLI' });
  await dialog.getByLabel('Display name', { exact: true }).fill(`${sliName} renamed`);
  await dialog.getByRole('button', { name: 'Save display name', exact: true }).click();
  await expect(page.getByRole('heading', { name: `${sliName} renamed`, exact: true })).toBeVisible();
  await expect(dialog).toBeHidden();
  // Rename it back: the SLO step below picks the SLI by its original name.
  await page.getByRole('button', { name: 'Edit SLI', exact: true }).first().click();
  await dialog.getByLabel('Display name', { exact: true }).fill(sliName);
  await dialog.getByRole('button', { name: 'Save display name', exact: true }).click();
  await expect(page.getByRole('heading', { name: sliName, exact: true })).toBeVisible();
});

test('set an SLO on the SLI and read its evaluation', async ({ page }) => {
  test.setTimeout(180_000);
  await page.goto('/slos');
  await page.getByRole('button', { name: 'New SLO', exact: true }).click();
  const service = unique('Checkout');
  await page.getByLabel('Service', { exact: true }).fill(service);
  await page.getByRole('button', { name: `Create service “${service}”`, exact: true }).click();
  await expect(page.getByLabel('Service display name', { exact: true })).toHaveValue(service);
  await page.getByRole('button', { name: 'Create service', exact: true }).click();
  await expect(page.getByLabel('Service display name', { exact: true })).toHaveCount(0);
  await page.getByLabel('Display name', { exact: true }).fill(sloName);
  await page.getByLabel('SLI', { exact: true }).click();
  await page.locator('[id^="slo-sli-option-"]').filter({ hasText: sliName }).click();
  await page.getByRole('radio', { name: '99.9%', exact: true }).click();
  await page.getByRole('radio', { name: '30 days', exact: true }).click();
  await page.getByRole('button', { name: 'Create SLO', exact: true }).click();
  await expect(page.getByRole('heading', { name: sloName, exact: true })).toBeVisible();

  // Every interval is 99 good of 100 against a 99.9% target: 1% bad events against a 0.1% allowance
  // leaves 1 − 0.01 / 0.001 = −900% of the budget, so the SLO is breached.
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'SLOs' }).click();
  await reloadUntil(page, async () => {
    const row = page.locator('.app-slo-row').filter({ hasText: sloName });
    await expect(row.locator('.app-slo-budget b')).toHaveText('-900%', { timeout: 3_000 });
    await expect(row.locator('.app-slo-objective')).toContainText('99.9% · 30 d');
  });

  const row = page.locator('.app-slo-row').filter({ hasText: sloName });
  await row.locator('.app-slo-objective').click();
  await expect(page.getByRole('heading', { name: sloName, exact: true })).toBeVisible();
  await expect(page.locator('.app-status-facts')).toContainText('99%');
  await expect(page.getByRole('heading', { name: 'Timeline', exact: true })).toBeVisible();
});

test('rename the SLO from its page', async ({ page }) => {
  await page.goto('/slos');
  await page.getByRole('link', { name: sloName, exact: true }).click();
  await page.getByRole('button', { name: 'Edit SLO', exact: true }).first().click();
  const dialog = page.getByRole('dialog', { name: 'Edit SLO' });
  await dialog.getByLabel('Display name', { exact: true }).fill(`${sloName} renamed`);
  await dialog.getByRole('button', { name: 'Save display name', exact: true }).click();
  await expect(page.getByRole('heading', { name: `${sloName} renamed`, exact: true })).toBeVisible();
});
