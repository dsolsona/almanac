import { expect, test } from '@playwright/test';

test('charts load on demand and render SVG with exact table paging and hover details', async ({ page }, testInfo) => {
  const chartRequests: string[] = [];
  page.on('request', (request) => { if (/echarts|components\/charts|ChartsSection|chartFixture/.test(request.url())) chartRequests.push(request.url()); });
  await page.goto('/design-system');
  await expect(page.getByRole('heading', { name: 'Clear signals. Steady decisions.' })).toBeVisible();
  expect(chartRequests).toHaveLength(0);
  await page.getByRole('button', { name: 'Charts' }).click();
  await expect(page.getByRole('heading', { name: 'Aligned charts and exact values' })).toBeVisible();
  const plot = page.locator('.timeline-chart__plot');
  await expect(plot.locator('svg')).toBeVisible();
  await expect(page.getByRole('table').locator('tbody').getByText('99.970000 %', { exact: true }).first()).toBeVisible();
  await expect(page.getByText('Complete', { exact: true }).first()).toBeVisible();
  await expect(page.getByText('page 1 of 2')).toBeVisible();
  await page.locator('.timeline-chart').screenshot({ path: testInfo.outputPath('timeline-panel.png') });
  await page.locator('.exact-values').screenshot({ path: testInfo.outputPath('exact-values-page1.png') });
  await page.getByRole('button', { name: 'Next' }).click();
  await expect(page.getByText('page 2 of 2')).toBeVisible();
  await expect(page.getByRole('button', { name: 'Next' })).toBeDisabled();
  await expect.poll(() => chartRequests.length).toBeGreaterThan(0);

  await plot.scrollIntoViewIfNeeded();
  const box = await plot.boundingBox();
  if (!box) throw new Error('Chart plot has no visible bounds');
  const moveToMinute = (minute: number) => page.mouse.move(box.x + 66 + (minute / 16) * (box.width - 66 - 20), box.y + box.height * 0.18);
  await moveToMinute(4);
  const tooltip = page.locator('.timeline-chart-tooltip');
  await expect(tooltip).toBeVisible();
  await expect(tooltip).toContainText('SLI · Example service');
  await expect(tooltip).toContainText('99.960000 % · Partial');
  await moveToMinute(5);
  await expect(tooltip).toContainText('SLI · Example service: Missing');
  await expect(tooltip).not.toContainText('99.960000 %');
  await moveToMinute(8);
  await expect(tooltip).toContainText('No sample at this timestamp');
  await expect(tooltip).not.toContainText('99.950000 %');
  await moveToMinute(10);
  await expect(tooltip).toContainText('SLI · Example service: Zero traffic');
  await page.screenshot({ path: test.info().outputPath('charts.png'), fullPage: true });
});

test('chart theme updates and keyboard pagination work without page overflow', async ({ page }, testInfo) => {
  await page.goto('/design-system');
  await page.getByRole('button', { name: 'Charts' }).click();
  const svg = page.locator('.timeline-chart__plot svg');
  await expect(svg).toBeVisible();
  const before = await svg.locator('path').first().getAttribute('stroke');
  const startedDark = await page.locator('.theme-toggle').getAttribute('aria-pressed') === 'true';
  await page.getByRole('button', { name: new RegExp(`switch to ${startedDark ? 'light' : 'dark'} theme`, 'i') }).click();
  if (startedDark) await expect(page.locator('html')).not.toHaveClass(/dark/);
  else await expect(page.locator('html')).toHaveClass(/dark/);
  await expect.poll(() => svg.locator('path').first().getAttribute('stroke')).not.toBe(before);

  const dimensions = await page.evaluate(() => ({ viewport: innerWidth, document: document.documentElement.scrollWidth }));
  expect(dimensions.document).toBeLessThanOrEqual(dimensions.viewport);
  const scrollRegion = page.getByRole('region', { name: 'Exact timeline values' });
  await scrollRegion.focus();
  await expect(scrollRegion).toBeFocused();
  const next = page.getByRole('button', { name: 'Next' });
  await next.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByText('page 2 of 2')).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('charts-theme.png'), fullPage: true });
});
