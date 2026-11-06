import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { api } from './support/api.ts';
import { restartApp } from './support/stack.ts';

// Runs after every other journey: restart the application container and check that what the
// journeys created is still there and that collection resumes. Sessions are revoked on restart
// only if the server says so; signing in again proves the rotated password persisted.
test('data and collection survive an application restart', async ({ page }) => {
  test.setTimeout(240_000);
  await page.goto('/slis');
  await expect(page.locator('.sli-list-name strong').first()).toBeVisible();
  const names = await page.locator('.sli-list-name strong').allTextContents();
  expect(names.length).toBeGreaterThan(0);
  // The end of the newest observed interval across the SLIs, in milliseconds (0 if none).
  const latest = async () => {
    const page1 = await api(page, 'GET', '/api/v1/slis?limit=200');
    return Math.max(0, ...page1.body.items
      .filter((item: { latest_observation: { outcome: string } | null }) => item.latest_observation?.outcome === 'observed')
      .map((item: { latest_observation: { interval_end: string } }) => Date.parse(item.latest_observation.interval_end)));
  };
  const before = await latest();
  expect(before).toBeGreaterThan(0);

  restartApp();

  await page.goto('/');
  if (await page.getByRole('heading', { name: 'Sign in', exact: true }).isVisible()) {
    await page.getByLabel('Login', { exact: true }).fill('admin');
    await page.getByLabel('Password', { exact: true }).fill(readFileSync('e2e/.auth/password', 'utf8'));
    await page.getByLabel('Password', { exact: true }).press('Enter');
  }
  await page.goto('/slis');
  await expect(page.locator('.sli-list-name strong')).toHaveText(names);

  // The agent reconnects by itself and collection continues: a newer observed interval than before the restart arrives.
  await expect.poll(latest, { timeout: 180_000, intervals: [5_000] }).toBeGreaterThan(before);
});
