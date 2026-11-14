import { expect, test, type Locator } from '@playwright/test';

async function expectCursorAtLastSample(group: Locator) {
  // ECharts can adjust grid insets when narrow labels change. Compare the
  // inspection marker with the rendered series endpoint, not a fixed inset.
  await expect.poll(() => group.evaluate(element => {
    const color = getComputedStyle(document.documentElement).getPropertyValue('--crit').trim();
    const path = element.querySelector<SVGPathElement>(`svg path[stroke="${color}"][stroke-width="2"]`)!;
    const end = path.getPointAtLength(path.getTotalLength());
    const point = new DOMPoint(end.x, end.y).matrixTransform(path.getScreenCTM()!);
    return Math.abs(element.querySelector('.timeline-inspection__cursor')!.getBoundingClientRect().x - point.x);
  })).toBeLessThan(2);
}

test('reopening a normalized or cancelled duration selects its committed text @matrix', async ({ page }) => {
  await page.goto('/design-system#alert-policy');
  const form = page.locator('#alert-policy').getByRole('form', { name: 'Alert policy' });
  const row = form.locator('.alert-condition-row').first();
  // Bind the persistent field ID: its accessible name changes with the live preset.
  await form.getByRole('button', { name: 'Edit short window for Fast burn', exact: true }).click();
  const fieldId = await form.getByRole('textbox', { name: 'Short window for Fast burn', exact: true }).getAttribute('id');
  const field = form.locator(`[id="${fieldId}"]`);
  await field.fill('10 minutes'); await field.press('Enter');
  for (const action of ['normalized', 'cancelled']) {
    if (action === 'cancelled') {
      await field.click(); await field.fill('25 minutes'); await field.press('Escape');
    }
    await field.click();
    await expect(field).toHaveValue('10 min');
    expect(await field.evaluate(element => {
      const input = element as HTMLInputElement;
      return input.value.slice(input.selectionStart ?? 0, input.selectionEnd ?? 0);
    })).toBe('10 min');
    await field.press('Escape');
  }
  await field.click(); await page.keyboard.type('15 min'); await field.press('Enter');
  await expect(field).toHaveText('15 min');
  // Ensure no layout wrapper is needed for the shared control correction.
  await expect(row).toBeVisible();
});

test('persistent inspection follows real chart resize @matrix', async ({ page }, info) => {
  await page.goto('/design-system#alert-detail');
  const detail = page.locator('#alert-detail .alert-occurrence-detail');
  const group = detail.getByRole('group', { name: /Inspect Burn rate/ });
  const plot = group.locator('.timeline-chart__plot');
  await expect(plot).toHaveAttribute('data-chart-ready', 'true');
  await group.focus(); await page.keyboard.press('End');
  const beforePlot = (await plot.boundingBox())!;
  await expectCursorAtLastSample(group);
  // Resize the chart host itself: a viewport change may leave the capped card
  // unchanged or trigger unrelated parent renders that hide the stale geometry.
  await plot.evaluate((element, width) => { (element as HTMLElement).style.width = `${width}px`; }, beforePlot.width - 100);
  expect((await plot.boundingBox())!.width).toBeLessThan(beforePlot.width - 90);
  await expect.poll(async () => Number(await plot.locator('svg').getAttribute('width'))).toBe((await plot.boundingBox())!.width);
  await expectCursorAtLastSample(group);
  const tooltip = group.getByRole('tooltip');
  await expect(tooltip).toContainText('12.5 ×');
  const panel = (await tooltip.boundingBox())!;
  expect(panel.x).toBeGreaterThanOrEqual(0);
  expect(panel.x + panel.width).toBeLessThanOrEqual(page.viewportSize()!.width);
  await page.screenshot({ path: `../.local/alert-ui-review-fixes/inspection-resize-${info.project.name}.png` });
});

test('retained creation mock keeps every condition control inside intermediate cards', async ({ page }) => {
  for (const theme of ['light', 'dark']) for (const width of [761, 800, 820, 860, 861]) {
    await page.setViewportSize({ width, height: 1100 });
    await page.goto(`file:///workspace/docs/design/mocks/alert-options/index.html?theme=${theme}#a/new`);
    await page.evaluate(() => document.fonts.ready);
    const rows = page.locator('.condition-row');
    await expect(rows).toHaveCount(2);
    for (const row of await rows.all()) {
      // The grid may itself overflow. Compare controls with the actual card,
      // rather than accepting the grid's overflowing min-content width.
      const bounds = (await page.locator('.conditions-section').boundingBox())!;
      for (const control of await row.locator('.condition-value,.remove-condition').all()) {
        const rect = (await control.boundingBox())!;
        expect(rect.x).toBeGreaterThanOrEqual(bounds.x);
        expect(rect.x + rect.width).toBeLessThanOrEqual(bounds.x + bounds.width + 1);
      }
    }
    await page.screenshot({ path: `../.local/alert-ui-review-fixes/mock-${theme}-${width}.png`, fullPage: true });
  }
});

test('invalid condition editors retain visible focus without changing geometry', async ({ page }) => {
  for (const theme of ['light', 'dark']) for (const width of [360, 1440]) {
    await page.setViewportSize({ width, height: 1000 });
    // The same URL at another width retains the draft in this static mock.
    // Start a fresh document for each independent form-entry scenario.
    await page.goto('about:blank');
    await page.goto(`file:///workspace/docs/design/mocks/alert-options/index.html?theme=${theme}#a/new`);
    await page.getByLabel('Alert policy name', { exact: true }).fill('Checkout burn');
    const picker = page.getByRole('combobox', { name: 'SLO', exact: true });
    await picker.fill('Checkout');
    await page.getByRole('option').first().click();
    const control = page.locator('.condition-control').first();
    await control.getByRole('button').click();
    const field = control.getByRole('textbox');
    await field.fill('invalid duration');
    await page.getByRole('button', { name: 'Create alert policy', exact: true }).click();
    await expect(field).toHaveAttribute('aria-invalid', 'true');
    await expect(field).toBeFocused();
    const editor = control.locator('.condition-input');
    const bounds = await editor.boundingBox();
    // Invalid state keeps its message; focus uses the same single accent border
    // as other text fields, rather than disappearing behind the error border.
    const focusBorder = await editor.evaluate(element => getComputedStyle(element).borderColor);
    const accent = await editor.evaluate(element => {
      const probe = document.createElement('span');
      probe.style.color = 'var(--accent)'; element.append(probe);
      const color = getComputedStyle(probe).color; probe.remove(); return color;
    });
    expect(focusBorder).toBe(accent);
    await expect(page.locator('#form-error')).toContainText('Enter a duration');
    await control.screenshot({ path: `../.local/alert-ui-review-fixes/invalid-focus-${theme}-${width}.png` });
    await field.fill('10 min');
    await field.press('Escape');
    await expect(control.getByRole('button')).toBeFocused();
    await control.getByRole('button').press('Enter');
    await expect(field).toBeFocused();
    const reopened = (await editor.boundingBox())!;
    // Screenshots and focus may scroll the mobile viewport by a fractional
    // pixel. Control dimensions, rather than viewport coordinates, are stable.
    expect({ width: reopened.width, height: reopened.height }).toEqual({ width: bounds!.width, height: bounds!.height });
  }
});

for (const colorScheme of ['light', 'dark'] as const) {
  test(`touch inspection follows shrink and expansion in ${colorScheme}`, async ({ browser, baseURL }) => {
    const context = await browser.newContext({ baseURL, viewport: { width: 360, height: 800 }, hasTouch: true, isMobile: true, colorScheme });
    try {
      const page = await context.newPage(); await page.goto('/design-system#alert-detail');
      const group = page.locator('#alert-detail').getByRole('group', { name: /Inspect Burn rate/ });
      const plot = group.locator('.timeline-chart__plot');
      await plot.scrollIntoViewIfNeeded(); await expect(plot).toHaveAttribute('data-chart-ready', 'true');
      const bounds = (await plot.boundingBox())!;
      await page.touchscreen.tap(bounds.x + bounds.width - 20, bounds.y + bounds.height / 2);
      await expect(group.getByRole('tooltip')).toContainText('12.5 ×');
      await expectCursorAtLastSample(group);
      for (const width of [bounds.width - 80, bounds.width]) {
        await plot.evaluate((element, size) => { (element as HTMLElement).style.width = `${size}px`; }, width);
        await expect.poll(async () => Number(await plot.locator('svg').getAttribute('width'))).toBe(width);
        await expectCursorAtLastSample(group);
        await expect(group.getByRole('tooltip')).toContainText('12.5 ×');
      }
      await page.screenshot({ path: `../.local/alert-ui-review-fixes/touch-resize-${colorScheme}.png` });
      await page.goto('/design-system#alert-occurrences');
      const example = page.locator('#alert-occurrences');
      await example.getByRole('button', { name: 'Show history example' }).tap();
      const closure = example.getByText('Closed 08:20 UTC');
      await closure.scrollIntoViewIfNeeded(); await closure.tap();
      await expect(example.getByText('2026-11-12 08:20 UTC')).toBeVisible();
      await expect(example.getByRole('heading', { name: 'Alert history', exact: true })).toBeVisible();
      await page.screenshot({ path: `../.local/alert-ui-review-fixes/touch-closure-${colorScheme}.png` });
    } finally { await context.close(); }
  });
}
