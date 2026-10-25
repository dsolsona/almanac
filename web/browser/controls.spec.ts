import { expect, test } from '@playwright/test';

test('calendar supports focused keyboard navigation, commit, clear, cancel and Escape', async ({ page }, testInfo) => {
  await page.goto('/design-system');
  const trigger = page.getByRole('button', { name: /Start date/ });
  await trigger.click();
  const dialog = page.getByRole('dialog', { name: 'Choose Start date' });
  await expect(dialog).toBeVisible();
  await expect(page.getByRole('button', { name: /October 25, 2026, selected, today/ })).toBeFocused();
  await page.screenshot({ path: testInfo.outputPath('calendar-open.png') });

  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('button', { name: /October 26, 2026/ })).toBeFocused();
  await page.keyboard.press('Home');
  await expect(page.getByRole('button', { name: /October 26, 2026/ })).toBeFocused();
  await page.keyboard.press('End');
  await expect(page.getByRole('grid', { name: 'November 2026' })).toBeVisible();
  await expect(page.getByRole('button', { name: /November 1, 2026/ })).toBeFocused();
  await page.keyboard.press('PageDown');
  await expect(page.getByRole('grid', { name: 'December 2026' })).toBeVisible();
  await page.keyboard.press('Shift+PageUp');
  await expect(page.getByRole('grid', { name: 'December 2025' })).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  await expect(trigger).toBeFocused();
  await expect(page.getByText('Calendar selected', { exact: false })).toHaveCount(0);

  await trigger.click();
  await page.getByRole('button', { name: 'Next month' }).click();
  await page.keyboard.press('Enter');
  await expect(dialog).toBeHidden();
  await expect(page.getByRole('status').filter({ hasText: 'Calendar selected 2026-11-25.' })).toBeVisible();
  await trigger.click();
  await page.getByRole('button', { name: 'Clear' }).click();
  await expect(page.getByRole('status').filter({ hasText: 'Calendar selection cleared.' })).toBeVisible();
  await expect(trigger).toContainText('Choose date');
});

test('switches retain native keyboard and disabled behavior, with visible checked state', async ({ page }, testInfo) => {
  await page.goto('/design-system');
  const section = page.locator('.feedback-controls');
  await expect(section).toBeVisible();
  const enabled = page.getByRole('switch', { name: 'Notifications' });
  const disabled = page.getByRole('switch', { name: 'Maintenance mode' });
  const lockedDate = page.getByRole('button', { name: /Locked date/ });
  await expect(enabled).toHaveAccessibleDescription('Send a local example notification.');
  await expect(disabled).toBeDisabled();
  await expect(lockedDate).toBeDisabled();
  await enabled.focus();
  await page.keyboard.press('Space');
  await expect(enabled).toBeChecked();
  await expect(page.locator('.ui-switch').filter({ has: enabled }).getByText('On', { exact: true })).toBeVisible();
  await section.screenshot({ path: testInfo.outputPath('switches.png') });
  await page.keyboard.press('Space');
  await expect(enabled).not.toBeChecked();
});

test('loading animation uses transform and becomes static under reduced motion', async ({ page }) => {
  await page.goto('/design-system');
  const segment = page.locator('.ui-loading__segment').first();
  await expect(page.getByRole('status', { name: 'Loading local example' })).toBeVisible();
  const initialTransform = await segment.evaluate((element) => getComputedStyle(element).transform);
  await expect.poll(() => segment.evaluate((element) => getComputedStyle(element).animationName)).toBe('ui-loading-slide');
  await expect.poll(() => segment.evaluate((element) => getComputedStyle(element).transform)).not.toBe(initialTransform);
  const movingStyles = await segment.evaluate((element) => ({ duration: getComputedStyle(element).animationDuration, transitionProperty: getComputedStyle(element).transitionProperty }));
  expect(movingStyles.duration).toBe('0.9s');
  expect(movingStyles.transitionProperty).not.toContain('width');
  await page.emulateMedia({ reducedMotion: 'reduce' });
  await expect.poll(() => segment.evaluate((element) => getComputedStyle(element).animationName)).toBe('none');
  await expect(segment).toBeVisible();
  await expect(page.getByRole('status', { name: 'Loading local example' })).toBeVisible();
});
