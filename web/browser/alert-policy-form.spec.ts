import { expect, test, type Locator, type Page } from '@playwright/test';

async function captureForm(page: Page, area: Locator, path: string) {
  const viewport = page.viewportSize()!, bounds = await area.boundingBox();
  // Set the capture height before measuring the clip. An implicit screenshot
  // resize can move preceding catalogue content and clip a tall mobile example.
  if (bounds!.height > viewport.height) await page.setViewportSize({ width: viewport.width, height: Math.ceil(bounds!.height) + 64 });
  await area.scrollIntoViewIfNeeded();
  await area.screenshot({ path });
}

test('approved policy form geometry and keyboard editing @matrix', async ({ page }, testInfo) => {
  await page.goto('/design-system#alert-policy');
  await page.evaluate(() => document.fonts.ready);
  const example = page.locator('#alert-policy'), form = example.getByRole('form', { name: 'Alert policy' });
  await form.scrollIntoViewIfNeeded();
  const name = form.getByLabel('Alert policy name'), picker = form.getByRole('combobox', { name: 'SLO' });
  await name.fill('Checkout sustained burn');
  const beforeName = await name.boundingBox(), beforePicker = await picker.boundingBox();
  await picker.fill('checkout'); await picker.press('ArrowDown'); await picker.press('Enter');
  await expect(form.getByRole('button', { name: 'Change SLO' })).toBeFocused();
  const afterName = await name.boundingBox(), selected = await form.locator('.ui-combobox__selection').boundingBox();
  expect(selected!.height).toBe(beforePicker!.height);
  expect(afterName!.height).toBe(beforeName!.height);
  if (testInfo.project.name.startsWith('desktop')) expect(Math.abs(afterName!.y - selected!.y)).toBeLessThanOrEqual(1);
  const edit = form.getByRole('button', { name: 'Edit short window for Fast burn' });
  const view = await edit.boundingBox();
  await edit.focus(); await edit.press('Enter');
  const input = form.getByRole('textbox', { name: 'Short window for Fast burn' });
  await expect(input).toBeFocused();
  const editor = await input.locator('..').boundingBox();
  expect(editor!.width).toBe(view!.width); expect(editor!.height).toBe(view!.height);
  await input.fill('10 min');
  // Live draft editing changes the preset label, but preserves this input's focus.
  await expect(form.getByRole('textbox', { name: 'Short window for Custom condition' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(form.getByRole('button', { name: 'Edit short window for Custom condition' })).toBeFocused();
  await form.getByRole('button', { name: 'Edit burn threshold for Custom condition' }).click();
  const threshold = form.getByRole('textbox', { name: 'Burn threshold for Custom condition' });
  await threshold.fill('20'); await threshold.press('Tab');
  await expect(form.getByRole('button', { name: 'Edit burn threshold for Custom condition' })).toHaveText('20×');
  await form.getByRole('button', { name: 'Create alert policy' }).click();
  await expect(example.getByRole('status')).toContainText('Local draft: Checkout sustained burn, 2 conditions, high severity.');
  await expect(form.getByRole('combobox', { name: /unit/i })).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await captureForm(page, example.locator('.alert-policy-example'), `../.local/alert-policy-form-${testInfo.project.name}.png`);
});

test('non-30-day preset confirmation and invalid correction remain explicit', async ({ page }) => {
  await page.goto('/design-system#alert-policy');
  await page.evaluate(() => document.fonts.ready);
  const example = page.locator('#alert-policy'), form = example.getByRole('form', { name: 'Alert policy' });
  await form.getByLabel('Alert policy name').fill('Payments burn');
  await form.getByRole('combobox', { name: 'SLO' }).fill('payment');
  await form.getByRole('combobox', { name: 'SLO' }).press('ArrowDown');
  await form.getByRole('combobox', { name: 'SLO' }).press('Enter');
  await form.getByRole('button', { name: 'Create alert policy' }).click();
  const confirmation = form.getByRole('checkbox');
  await expect(confirmation).toBeFocused();
  await expect(confirmation).toHaveAccessibleDescription(/Confirm these presets/);
  await confirmation.check();
  await form.getByRole('button', { name: 'Edit short window for Fast burn' }).click();
  await form.getByRole('textbox', { name: 'Short window for Fast burn' }).fill('wrong');
  await form.getByRole('button', { name: 'Create alert policy' }).click();
  const invalid = form.getByRole('textbox', { name: 'Short window for Custom condition' });
  await expect(invalid).toBeFocused(); await expect(invalid).toHaveValue('wrong');
  await invalid.fill('5 min');
  await expect(form.getByRole('textbox', { name: 'Short window for Fast burn' })).toBeFocused();
  await page.keyboard.press('Enter');
  await form.getByRole('button', { name: 'Create alert policy' }).click();
  await expect(example.getByRole('status')).toContainText('Local draft: Payments burn');
});

for (const colorScheme of ['light', 'dark'] as const) {
  test(`touch editing, condition menu and preserved error draft in ${colorScheme}`, async ({ browser, baseURL }) => {
    const context = await browser.newContext({ baseURL, viewport: { width: 360, height: 800 }, hasTouch: true, isMobile: true, colorScheme });
    try {
      const page = await context.newPage();
      await page.goto('/design-system#alert-policy');
      await page.evaluate(() => document.fonts.ready);
      const example = page.locator('#alert-policy'), form = example.getByRole('form', { name: 'Alert policy' });
      await form.getByLabel('Alert policy name').fill('Mobile checkout');
      const picker = form.getByRole('combobox', { name: 'SLO' });
      await picker.tap(); await picker.fill('missing');
      await expect(form.getByText('No SLOs found.')).toBeVisible();
      await picker.fill('payment');
      await form.getByRole('option', { name: /Payment processing/ }).tap();
      const selected = form.locator('.ui-combobox__selection');
      await expect(selected).toContainText('Payment processing for international card transactions');
      await expect(form.getByRole('button', { name: 'Change SLO' })).toBeVisible();
      const edit = form.getByRole('button', { name: 'Edit long window for Fast burn' });
      expect((await edit.boundingBox())!.height).toBeGreaterThanOrEqual(44);
      await edit.tap();
      await form.getByRole('textbox', { name: 'Long window for Fast burn' }).fill('2 hours');
      await form.getByRole('button', { name: 'Edit burn threshold for Custom condition' }).tap();
      await expect(form.getByRole('button', { name: 'Edit long window for Custom condition' })).toHaveText('2 h');
      await form.getByRole('textbox', { name: 'Burn threshold for Custom condition' }).fill('10');
      await form.getByRole('button', { name: 'Add condition' }).tap();
      const menu = form.getByRole('dialog', { name: 'Add a condition' });
      await expect(menu).toBeVisible();
      await menu.getByRole('button', { name: /Custom condition/ }).tap();
      await expect(form.getByRole('textbox', { name: 'Short window for Custom condition' })).toBeFocused();
      await form.getByLabel('Alert policy name').tap();
      await form.getByRole('button', { name: 'Remove Custom condition' }).last().tap();
      await example.getByRole('button', { name: 'Show error example' }).tap();
      await expect(form.getByRole('alert')).toContainText('Your values are preserved.');
      await expect(form.getByLabel('Alert policy name')).toHaveValue('Mobile checkout');
      await example.getByRole('button', { name: 'Show pending example' }).tap();
      await expect(form.getByRole('button', { name: 'Create alert policy' })).toBeDisabled();
      await expect(form.getByRole('button', { name: 'Change SLO' })).toBeDisabled();
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await captureForm(page, example.locator('.alert-policy-example'), `../.local/alert-policy-touch-${colorScheme}.png`);
    } finally { await context.close(); }
  });
}
