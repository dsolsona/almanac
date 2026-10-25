import { expect, test } from '@playwright/test';

test('catalogue foundations and shared controls work at this viewport and theme', async ({ page }, testInfo) => {
  await page.goto('/design-system');
  await expect(page.getByRole('heading', { name: 'Clear signals. Steady decisions.' })).toBeVisible();
  await expect(page.getByRole('navigation', { name: 'Catalogue sections' })).toBeVisible();
  await expect(page.getByText('Paper', { exact: true })).toBeVisible();
  const theme = testInfo.project.name.includes('dark') ? 'dark' : 'light';
  await expect.poll(() => page.locator('html').evaluate((element) => getComputedStyle(element).colorScheme)).toBe(theme);
  await expect.poll(() => page.evaluate(() => document.fonts.check('400 20px "Almanac Slab"'))).toBe(true);
  const dimensions = await page.evaluate(() => ({ viewport: innerWidth, document: document.documentElement.scrollWidth }));
  expect(dimensions.document).toBeLessThanOrEqual(dimensions.viewport);
  const geometry = await page.evaluate(() => ({
    buttonPadding: parseFloat(getComputedStyle(document.querySelector('.ui-button')!).paddingLeft),
    panelPadding: parseFloat(getComputedStyle(document.querySelector('.ui-panel')!).paddingLeft),
    navigationGap: parseFloat(getComputedStyle(document.querySelector('.catalogue-nav')!).columnGap),
    titleSize: parseFloat(getComputedStyle(document.querySelector('.catalogue-intro h1')!).fontSize),
  }));
  expect(geometry.buttonPadding).toBeGreaterThanOrEqual(16);
  expect(geometry.panelPadding).toBeGreaterThanOrEqual(16);
  expect(geometry.navigationGap).toBeGreaterThanOrEqual(12);
  expect(geometry.titleSize).toBeLessThanOrEqual(44);
  const contrast = await page.evaluate(() => {
    const luminance = (color: string) => {
      const rgb = color.match(/[\d.]+/g)!.slice(0, 3).map(Number).map((value) => {
        const channel = value / 255;
        return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * rgb[0]! + 0.7152 * rgb[1]! + 0.0722 * rgb[2]!;
    };
    const ratio = (selector: string) => {
      const style = getComputedStyle(document.querySelector(selector)!);
      const values = [luminance(style.color), luminance(style.backgroundColor)].sort((a, b) => b - a);
      return (values[0]! + 0.05) / (values[1]! + 0.05);
    };
    return Object.fromEntries(['body', '.ui-button--primary', '.ui-status--healthy', '.ui-status--warning', '.ui-status--breached', '.ui-status--no-data', '.ui-status--stale'].map((selector) => [selector, ratio(selector)]));
  });
  for (const [selector, ratio] of Object.entries(contrast)) expect(ratio, `${selector} contrast`).toBeGreaterThanOrEqual(4.5);
  await page.screenshot({ path: testInfo.outputPath('catalogue.png'), fullPage: true });

  const invalid = page.getByRole('textbox', { name: 'Query interval' });
  await expect(invalid).toHaveAttribute('aria-invalid', 'true');
  const errorId = await invalid.getAttribute('aria-describedby');
  expect(errorId).toContain('catalogue-invalid-error');
  await expect(page.locator(`#${errorId}`)).toContainText('Enter a duration');
  await expect(page.getByRole('button', { name: 'Disabled' })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Pending' })).toBeDisabled();
});

test('keyboard navigation enters dialog, Escape closes it and restores focus', async ({ page }) => {
  await page.goto('/design-system');
  await page.keyboard.press('Tab');
  await expect(page.getByRole('link', { name: 'Skip to catalogue' })).toBeFocused();
  await page.getByRole('button', { name: 'Open dialog' }).focus();
  await page.keyboard.press('Enter');
  const dialog = page.getByRole('dialog', { name: 'A local dialog' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('textbox', { name: 'Example input' })).toBeVisible();
  await page.keyboard.press('Tab');
  expect(await dialog.evaluate((element) => element.contains(document.activeElement))).toBe(true);
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(page.getByRole('button', { name: 'Open dialog' })).toBeFocused();
});

test('local pending action blocks repeat input and completes with honest feedback', async ({ page }) => {
  await page.goto('/design-system');
  const save = page.getByRole('button', { name: 'Save local example' });
  await save.click();
  const pending = page.getByRole('button', { name: 'Saving locally' });
  await expect(pending).toBeDisabled();
  await expect(pending).toHaveAttribute('aria-busy', 'true');
  await expect(page.getByRole('status').filter({ hasText: 'Saved locally for this page only.' })).toBeVisible();
  await expect(page.getByText('Count: 1.')).toBeVisible();
});

test('theme choice is interactive and remains stored after reload', async ({ page }) => {
  await page.addInitScript(() => {
    if (!sessionStorage.getItem('almanac-theme-test-initialized')) {
      localStorage.setItem('almanac-theme', 'light');
      sessionStorage.setItem('almanac-theme-test-initialized', 'true');
    }
  });
  await page.goto('/design-system');
  const toggle = page.getByRole('button', { name: /switch to dark theme/i });
  await toggle.click();
  await expect(page.locator('html')).toHaveClass(/dark/);
  await expect(page.locator('html')).toHaveCSS('color-scheme', 'dark');
  await page.reload();
  await expect(page.locator('html')).toHaveClass(/dark/);
  await expect(page.getByRole('button', { name: /switch to light theme/i })).toHaveAttribute('aria-pressed', 'true');
});

test('catalogue is dev-only and unsupported development paths are honest', async ({ page }) => {
  await page.goto('/unrecognized-path');
  await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible();
  await expect(page.getByText(/Clear signals/)).toHaveCount(0);
});

test('coarse pointer controls use the 44px target size', async ({ browser }) => {
  const context = await browser.newContext({ viewport: { width: 360, height: 800 }, hasTouch: true });
  try {
    const page = await context.newPage();
    await page.goto('http://127.0.0.1:5198/design-system');
    expect(await page.evaluate(() => matchMedia('(pointer: coarse)').matches)).toBe(true);
    await expect(page.locator('.ui-button').first()).toHaveCSS('min-height', '44px');
    await expect(page.locator('.ui-control').first()).toHaveCSS('min-height', '44px');
    await expect(page.locator('.theme-toggle')).toHaveCSS('min-height', '44px');
  } finally {
    await context.close();
  }
});
