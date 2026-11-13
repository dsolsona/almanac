import { expect, test, type Locator, type Page } from '@playwright/test';

async function capture(page: Page, area: Locator, path: string) {
  const viewport = page.viewportSize()!, bounds = await area.boundingBox();
  if (bounds!.height > viewport.height) await page.setViewportSize({ width: viewport.width, height: Math.ceil(bounds!.height) + 64 });
  await area.scrollIntoViewIfNeeded(); await area.screenshot({ path });
}

test('selected alert detail cards and exact keyboard inspection @matrix', async ({ page }, info) => {
  await page.goto('/design-system#alert-detail'); await page.evaluate(() => document.fonts.ready);
  const example = page.locator('#alert-detail'), detail = example.locator('.alert-occurrence-detail');
  await expect(detail.getByRole('heading', { level: 1 })).toHaveText('Checkout sustained burn');
  await expect(detail.getByRole('link', { name: 'Checkout availability' })).toHaveAttribute('href', '/design-system#alert-detail-slo');
  await expect(detail.getByText('12.5×', { exact: true })).toHaveCSS('color', info.project.name.endsWith('dark') ? 'rgb(243, 157, 165)' : 'rgb(165, 43, 56)');
  const chart = detail.getByRole('group', { name: /Inspect Burn rate/ });
  await expect(chart.locator('[data-chart-ready="true"]')).toBeVisible();
  await chart.scrollIntoViewIfNeeded();
  const plotBounds = (await chart.locator('.timeline-chart__plot').boundingBox())!;
  await page.mouse.move(plotBounds.x + plotBounds.width - 22, plotBounds.y + plotBounds.height / 2);
  await expect(chart.getByRole('tooltip')).toContainText('12.5 ×');
  await page.mouse.move(0, 0);
  await chart.focus(); await expect(chart).toBeFocused(); await page.keyboard.press('End');
  await expect(chart.getByRole('tooltip')).toContainText('12.5 ×');
  await expect(chart.getByRole('tooltip')).toContainText('7.4 ×');
  await page.keyboard.press('Home'); await expect(chart.getByRole('tooltip')).toContainText('09:42');
  await page.keyboard.press('Escape'); await expect(chart.getByRole('tooltip')).toHaveCount(0);
  const geometry = await detail.evaluate(element => ({ width: document.documentElement.scrollWidth, viewport: innerWidth, title: element.querySelector('h1')!.getBoundingClientRect().toJSON() }));
  expect(geometry.width).toBe(geometry.viewport);
  await expect(detail.getByRole('heading', { name: 'Burn rate', exact: true })).toHaveCSS('font-family', /Almanac Slab/);
  if (info.project.name === 'desktop-dark') await capture(page, detail, '../.local/alert-detail-keyboard-focus-dark.png');
  await detail.getByRole('heading', { level: 1 }).click();
  await capture(page, detail, `../.local/alert-detail-${info.project.name}.png`);
  await detail.getByText('How this is calculated', { exact: true }).click();
  await expect(detail.getByText(/375 bad.*30,000/)).toBeVisible();
  await expect(detail.getByText('1.25%', { exact: true })).toBeVisible();
  await detail.getByText(/Original firing evidence/).click();
  await expect(detail.getByRole('region', { name: 'Original firing evidence' })).toBeVisible();
  await capture(page, detail, `../.local/alert-detail-calculation-${info.project.name}.png`);
  await example.getByRole('button', { name: 'Show long detail names', exact: true }).click();
  await expect(detail.getByRole('heading', { level: 1 })).toContainText('all regional gateways');
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(page.viewportSize()!.width);
  await example.getByRole('button', { name: 'Detail viewer example', exact: true }).click();
  await expect(detail.getByRole('button', { name: 'Acknowledge', exact: true })).toHaveCount(0);
});

test('detail preserves supplied handoff, calculation waiting, original evidence and administrative cause', async ({ page }) => {
  await page.goto('/design-system#alert-detail');
  const example = page.locator('#alert-detail'), detail = example.locator('.alert-occurrence-detail');
  await example.getByRole('button', { name: 'Show condition handoff', exact: true }).click();
  await expect(detail.getByText('Acknowledged', { exact: true })).toBeVisible();
  await expect(detail.getByRole('region', { name: 'Current alert state' }).getByText('Jamie Morgan', { exact: true })).toBeVisible();
  await expect(detail.getByRole('button', { name: /Sustained burn/ })).toHaveAttribute('aria-pressed', 'true');
  await expect(detail.getByText('12.5×', { exact: true })).toBeVisible();
  await detail.getByRole('button', { name: /Fast burn/ }).click();
  await expect(detail.getByRole('button', { name: /Fast burn/ })).toHaveAttribute('aria-pressed', 'true');
  await expect(detail.getByText('8×', { exact: true })).toBeVisible();
  await expect(detail.getByRole('group', { name: 'Inspect condition evidence' }).getByText('Not matching', { exact: true })).toBeVisible();
  await example.getByRole('button', { name: 'Show unavailable evidence', exact: true }).click();
  await detail.getByRole('button', { name: /Sustained burn/ }).click();
  await expect(detail.getByRole('region', { name: 'Alert evidence' }).getByText('Missing observations prevent recovery.')).toBeVisible();
  await expect(detail.getByText('Acknowledged', { exact: true })).toBeVisible();
  await capture(page, detail, '../.local/alert-detail-unavailable.png');
  await example.getByRole('button', { name: 'Show calculation waiting', exact: true }).click();
  await expect(detail.getByText('Waiting for aggregate preparation')).toBeVisible();
  await expect(detail.locator('time[datetime="2026-11-12T10:38:00Z"]')).toBeVisible();
  await example.getByRole('button', { name: 'Show archived occurrence', exact: true }).click();
  await expect(detail.getByText('Disabled', { exact: true })).toBeVisible();
  await expect(detail.getByText('SLO archived', { exact: true })).toBeVisible();
  await expect(detail.getByRole('button', { name: 'Acknowledge', exact: true })).toHaveCount(0);
});

test('detail acknowledgement errors and committed resolution retain usable focus', async ({ page }) => {
  await page.goto('/design-system#alert-detail');
  const example = page.locator('#alert-detail'), detail = example.locator('.alert-occurrence-detail');
  await example.getByRole('button', { name: 'Fail acknowledgement', exact: true }).click();
  await detail.getByRole('button', { name: 'Acknowledge', exact: true }).click();
  await expect(detail.getByRole('alert')).toBeFocused();
  await expect(detail.getByText('Firing', { exact: true })).toBeVisible();
  await detail.getByRole('button', { name: 'Refresh alert', exact: true }).click();
  await example.getByRole('button', { name: 'Resolve on acknowledgement', exact: true }).click();
  await detail.getByRole('button', { name: 'Acknowledge', exact: true }).click();
  await expect(detail.getByText('Resolved', { exact: true })).toBeVisible();
  await expect(detail.getByRole('heading', { level: 1 })).toBeFocused();
  const evidence = detail.getByRole('region', { name: 'Alert evidence' });
  await expect(evidence.getByText('Not matching ·', { exact: true })).toBeVisible();
  await expect(evidence.getByText('1×', { exact: true })).toBeVisible();
  await expect(evidence.getByText('2×', { exact: true })).toBeVisible();
  const chart = detail.getByRole('group', { name: /Inspect Burn rate/ });
  await chart.focus(); await page.keyboard.press('End');
  await expect(chart.getByRole('tooltip')).toContainText('1 ×'); await expect(chart.getByRole('tooltip')).toContainText('2 ×');
  await page.keyboard.press('Escape');
  await detail.getByText('Original firing evidence', { exact: true }).click();
  const original = detail.getByRole('region', { name: 'Original firing evidence' });
  await expect(original.getByRole('heading', { name: 'Sustained burn · Matching' })).toBeVisible();
  await expect(original.getByText('6.8×', { exact: true })).toBeVisible();
  await detail.getByText('How this is calculated', { exact: true }).click();
  await expect(detail.getByText('30 bad of 30,000', { exact: true })).toBeVisible();
  await expect(detail.getByText('720 bad of 360,000', { exact: true })).toBeVisible();
  await expect(detail.getByText('0.2%', { exact: true })).toBeVisible();
  await detail.getByRole('link', { name: 'Checkout availability' }).click();
  await expect(page).toHaveURL(/#alert-detail-slo$/);
  await expect(page.getByRole('heading', { name: 'Linked fictional SLO', exact: true })).toBeVisible();
});

for (const colorScheme of ['light', 'dark'] as const) {
  test(`touch detail point inspection and pending acknowledgement in ${colorScheme}`, async ({ browser, baseURL }) => {
    const context = await browser.newContext({ baseURL, viewport: { width: 360, height: 800 }, hasTouch: true, isMobile: true, colorScheme });
    try {
      const page = await context.newPage(); await page.goto('/design-system#alert-detail');
      const example = page.locator('#alert-detail'), detail = example.locator('.alert-occurrence-detail');
      const plot = detail.locator('.timeline-chart__plot'); await plot.scrollIntoViewIfNeeded();
      await expect(plot).toHaveAttribute('data-chart-ready', 'true');
      const bounds = (await plot.boundingBox())!; await page.touchscreen.tap(bounds.x + bounds.width - 30, bounds.y + bounds.height / 2);
      const tooltip = detail.getByRole('tooltip'); await expect(tooltip).toBeVisible();
      const panel = (await tooltip.boundingBox())!;
      expect(panel.x).toBeGreaterThanOrEqual(0); expect(panel.x + panel.width).toBeLessThanOrEqual(360);
      expect(await detail.getByRole('group', { name: /Inspect Burn rate/ }).evaluate(element => getComputedStyle(element).getPropertyValue('-webkit-tap-highlight-color'))).toBe('rgba(0, 0, 0, 0)');
      expect(panel.y).toBeGreaterThanOrEqual(0); expect(panel.y + panel.height).toBeLessThanOrEqual(800);
      await page.screenshot({ path: `../.local/alert-detail-touch-inspection-${colorScheme}.png` });
      await detail.getByText('How this is calculated', { exact: true }).tap(); await expect(tooltip).toHaveCount(0);
      await example.getByRole('button', { name: 'Hold acknowledgement', exact: true }).tap();
      const acknowledge = detail.getByRole('button', { name: 'Acknowledge', exact: true }); await acknowledge.scrollIntoViewIfNeeded();
      expect((await acknowledge.boundingBox())!.height).toBeGreaterThanOrEqual(44);
      await acknowledge.tap(); await expect(acknowledge).toBeDisabled();
      await expect(detail.getByText('Firing', { exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(360);
    } finally { await context.close(); }
  });
}

test('failed lazy detail loading preserves the catalogue and reload retries', async ({ page }) => {
  let requests = 0;
  await page.route('**/src/catalogue/AlertDetailExample.tsx*', async route => {
    requests++;
    if (requests === 1) await route.abort('failed'); else await route.continue();
  });
  await page.goto('/design-system#alert-detail');
  await expect(page.getByRole('alert').filter({ hasText: 'Alert detail could not be loaded.' })).toBeVisible();
  await page.getByRole('button', { name: 'Primary', exact: true }).click();
  await expect(page.locator('.demo-feedback').filter({ hasText: 'Button action' })).toContainText('Count: 1');
  await page.getByRole('button', { name: 'Reload catalogue to retry', exact: true }).click();
  await expect(page.locator('#alert-detail').getByRole('heading', { level: 1 })).toHaveText('Checkout sustained burn');
  expect(requests).toBe(2);
});
