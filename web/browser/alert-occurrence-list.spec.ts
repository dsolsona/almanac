import { expect, test, type Locator, type Page } from '@playwright/test';

async function capture(page: Page, area: Locator, path: string) {
  const viewport = page.viewportSize()!, bounds = await area.boundingBox();
  if (bounds!.height > viewport.height) await page.setViewportSize({ width: viewport.width, height: Math.ceil(bounds!.height) + 64 });
  await area.scrollIntoViewIfNeeded(); await area.screenshot({ path });
}

test('selected alert list evidence, grouping and keyboard filters @matrix', async ({ page }, testInfo) => {
  await page.goto('/design-system#alert-occurrences');
  await page.evaluate(() => document.fonts.ready);
  const example = page.locator('#alert-occurrences'), list = example.locator('.alert-occurrence-list');
  const records = list.getByRole('region', { name: 'Alert records' });
  await list.scrollIntoViewIfNeeded();
  await expect(list.getByText('22.8×')).toHaveCount(2);
  await expect(list.getByText('Waiting for calculation')).toBeVisible();
  await expect(list.getByText('Missing observations prevent recovery')).toBeVisible();
  const layout = await page.evaluate(() => ({ viewport: innerWidth, width: document.documentElement.scrollWidth, overflow: [...document.querySelectorAll('body *')].filter(element => element.getBoundingClientRect().right > innerWidth && !element.closest('.alert-table-scroll,.catalogue-nav')).slice(-20).map(element => ({ tag: element.tagName, className: element.className, right: element.getBoundingClientRect().right })) }));
  await testInfo.attach('list-layout', { body: JSON.stringify(layout), contentType: 'application/json' });
  expect(layout, 'page width and uncontained overflow').toMatchObject({ width: layout.viewport });
  if (testInfo.project.name.startsWith('mobile')) {
    expect(await records.evaluate(element => element.scrollWidth > element.clientWidth)).toBe(true);
    await records.focus(); await page.keyboard.press('End');
  }
  await capture(page, example.locator('.alert-list-example'), `../.local/alert-list-${testInfo.project.name}.png`);
  const filter = list.getByRole('button', { name: 'Filter Alert', exact: true });
  await filter.focus(); await filter.press('Enter');
  const dialog = list.getByRole('dialog', { name: 'Filter alert', exact: true });
  await expect(dialog).toBeVisible();
  await dialog.getByLabel('Alert name').fill('checkout');
  await page.keyboard.press('Escape'); await expect(filter).toBeFocused();
  await filter.press('Enter'); await expect(dialog.getByLabel('Alert name')).toHaveValue('');
  await dialog.getByLabel('Alert name').fill('checkout'); await dialog.getByRole('button', { name: 'Apply', exact: true }).click();
  await expect(list.getByText('1–2 of 2 alerts')).toBeVisible();
  await list.getByRole('button', { name: 'Clear filters', exact: true }).click();
  await expect(list.getByRole('searchbox', { name: 'Search alerts', exact: true })).toBeFocused();
  await list.getByRole('button', { name: 'Group by SLO', exact: true }).click();
  await expect(list.getByText('2 alerts · 1 firing')).toBeVisible();
  await expect(list.getByText('1–4 of 4 alerts')).toBeVisible();
  await capture(page, example.locator('.alert-list-example'), `../.local/alert-list-grouped-${testInfo.project.name}.png`);
});

test('acknowledgement conflicts preserve lifecycle and history distinguishes recovered gaps', async ({ page }) => {
  await page.goto('/design-system#alert-occurrences');
  const example = page.locator('#alert-occurrences'), table = example.getByRole('table', { name: 'Active alerts' });
  await example.getByRole('button', { name: 'Conflict on acknowledgement', exact: true }).click();
  const checkout = table.getByRole('row').filter({ has: page.getByRole('link', { name: 'Checkout burn', exact: true }) });
  await checkout.getByRole('button', { name: 'Acknowledge Checkout burn', exact: true }).click();
  await expect(checkout.getByRole('alert')).toContainText('The alert changed');
  await expect(checkout.getByText('Firing', { exact: true })).toBeVisible();
  await expect(example.getByRole('status')).toHaveText('');
  await example.getByRole('button', { name: 'Conflict on acknowledgement', exact: true }).click();
  await checkout.getByRole('button', { name: 'Acknowledge Checkout burn', exact: true }).click();
  await expect(checkout.getByText('Acknowledged', { exact: true })).toBeVisible();
  await expect(checkout.getByText('Alex Rivera')).toBeVisible();
  await expect(checkout.getByRole('link', { name: 'View Checkout burn', exact: true })).toBeFocused();
  await checkout.getByText('High severity').click();
  await expect(example.getByRole('status')).toContainText('Opened the local alert example');
  await example.getByRole('button', { name: 'Show history example' }).click();
  await expect(example.getByText('Detected after recovery')).toBeVisible();
  await expect(example.getByText('SLO archived', { exact: true })).toBeVisible();
  await expect(example.getByText('Unreconstructed evaluation gap', { exact: true })).toBeVisible();
  await expect(example.getByRole('table').getByRole('button', { name: /Acknowledge/ })).toHaveCount(0);
});

for (const colorScheme of ['light', 'dark'] as const) {
  test(`touch list filters and pending acknowledgement in ${colorScheme}`, async ({ browser, baseURL }) => {
    const context = await browser.newContext({ baseURL, viewport: { width: 360, height: 800 }, hasTouch: true, isMobile: true, colorScheme });
    try {
      const page = await context.newPage(); await page.goto('/design-system#alert-occurrences'); await page.evaluate(() => document.fonts.ready);
      const example = page.locator('#alert-occurrences'), list = example.locator('.alert-occurrence-list');
      await list.getByRole('button', { name: 'Filters', exact: true }).tap();
      const dialog = list.getByRole('dialog', { name: 'Filter alerts', exact: true });
      const applyBounds = await dialog.getByRole('button', { name: 'Apply', exact: true }).boundingBox();
      expect(applyBounds!.y + applyBounds!.height).toBeLessThanOrEqual(800);
      await dialog.getByLabel('Severity', { exact: true }).selectOption('high');
      const filterGeometry = await dialog.evaluate(element => ({ top: element.getBoundingClientRect().top, scroll: element.scrollTop, header: element.querySelector('header')!.getBoundingClientRect().toJSON(), content: element.querySelector('.ui-dialog__content')!.getBoundingClientRect().toJSON() }));
      expect(filterGeometry.header.y, JSON.stringify(filterGeometry)).toBeGreaterThanOrEqual(filterGeometry.top);
      await page.screenshot({ path: `../.local/alert-list-filters-touch-${colorScheme}.png` });
      await dialog.getByRole('button', { name: 'Apply', exact: true }).tap();
      await expect(list.getByText('1–1 of 1 alert')).toBeVisible();
      await example.getByRole('button', { name: 'Hold acknowledgement', exact: true }).tap();
      const button = list.getByRole('button', { name: 'Acknowledge Checkout burn', exact: true });
      await button.scrollIntoViewIfNeeded(); expect((await button.boundingBox())!.height).toBeGreaterThanOrEqual(44);
      await button.tap(); await expect(button).toBeDisabled();
      await expect(list.getByText('Firing', { exact: true })).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await capture(page, example.locator('.alert-list-example'), `../.local/alert-list-touch-${colorScheme}.png`);
    } finally { await context.close(); }
  });
}


test('catalogue pages retain global totals across next, previous and page-size changes', async ({ page }) => {
  await page.goto('/design-system#alert-occurrences');
  const example = page.locator('#alert-occurrences'), list = example.locator('.alert-occurrence-list');
  await example.getByRole('button', { name: 'Show paginated example', exact: true }).click();
  await expect(list.getByText('1–10 of 25 alerts', { exact: true })).toBeVisible();
  await expect(list.getByRole('table').locator('tbody tr')).toHaveCount(10);
  await list.getByRole('button', { name: 'Next ›', exact: true }).click();
  await expect(list.getByText('11–20 of 25 alerts', { exact: true })).toBeVisible();
  await expect(list.getByRole('link', { name: 'Checkout burn', exact: true })).toHaveCount(0);
  await list.getByRole('button', { name: 'Next ›', exact: true }).click();
  await expect(list.getByText('21–25 of 25 alerts', { exact: true })).toBeVisible();
  await expect(list.getByRole('button', { name: 'Next ›', exact: true })).toBeDisabled();
  await list.getByRole('button', { name: '‹ Previous', exact: true }).click();
  await expect(list.getByText('11–20 of 25 alerts', { exact: true })).toBeVisible();
  await list.getByLabel('Per page', { exact: true }).selectOption('20');
  await expect(list.getByText('1–20 of 25 alerts', { exact: true })).toBeVisible();
  await expect(list.getByRole('table').locator('tbody tr')).toHaveCount(20);
});

test('acknowledging a filtered last page returns to a valid page', async ({ page }) => {
  await page.goto('/design-system#alert-occurrences');
  const example = page.locator('#alert-occurrences'), list = example.locator('.alert-occurrence-list');
  await example.getByRole('button', { name: 'Show paginated example', exact: true }).click();
  await list.getByRole('button', { name: 'Filter State', exact: true }).click();
  const dialog = list.getByRole('dialog', { name: 'Filter state', exact: true });
  await dialog.getByLabel('State', { exact: true }).selectOption('firing');
  await dialog.getByRole('button', { name: 'Apply', exact: true }).click();
  await list.getByRole('button', { name: 'Next ›', exact: true }).click();
  await expect(list.getByText('11–19 of 19 alerts', { exact: true })).toBeVisible();
  for (let index = 0; index < 9; index++) await list.getByRole('table').getByRole('button', { name: /^Acknowledge / }).first().click();
  await expect(list.getByText('1–10 of 10 alerts', { exact: true })).toBeVisible();
  await expect(list.getByRole('table').locator('tbody tr')).toHaveCount(10);
  await expect(list.getByRole('button', { name: '‹ Previous', exact: true })).toBeDisabled();
});
