import { mkdirSync, writeFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { initialPassword } from './support/stack.ts';

// First run: the administrator signs in with the generated one-time password, must choose a
// new one, and lands on an empty Agents page. Saves the session for the other journeys.
export const adminPassword = 'e2e-correct-horse-battery-staple-41';

test('first-run onboarding replaces the initial password and signs the administrator in', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();

  await test.step('a wrong password is refused without revealing why', async () => {
    await page.getByLabel('Login', { exact: true }).fill('admin');
    await page.getByLabel('Password', { exact: true }).fill('not-the-password');
    await page.getByLabel('Password', { exact: true }).press('Enter');
    await expect(page.getByRole('alert')).toContainText('Sign-in was not accepted');
  });

  await test.step('the initial password forces a new one', async () => {
    await page.getByLabel('Password', { exact: true }).fill(initialPassword());
    await page.getByLabel('Password', { exact: true }).press('Enter');
    await expect(page.getByLabel('New password', { exact: true })).toBeVisible();
    await page.getByLabel('New password', { exact: true }).fill('short');
    await page.getByLabel('Confirm new password', { exact: true }).fill('short');
    await page.getByRole('button', { name: 'Set password', exact: true }).click();
    await expect(page.getByRole('alert')).toBeVisible();
    await page.getByLabel('New password', { exact: true }).fill(adminPassword);
    await page.getByLabel('Confirm new password', { exact: true }).fill(adminPassword);
    await page.getByRole('button', { name: 'Set password', exact: true }).click();
  });

  await test.step('the administrator lands on an empty Agents page', async () => {
    await expect(page).toHaveURL(/\/agents$/);
    await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
    await expect(page.getByText('No agents have been registered.')).toBeVisible();
  });

  await test.step('sign out and back in with the new password', async () => {
    await page.getByRole('button', { name: 'Sign out', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
    await page.getByLabel('Login', { exact: true }).fill('admin');
    await page.getByLabel('Password', { exact: true }).fill(adminPassword);
    await page.getByLabel('Password', { exact: true }).press('Enter');
    await expect(page.getByRole('heading', { name: 'Agents', exact: true })).toBeVisible();
  });

  mkdirSync('e2e/.auth', { recursive: true });
  await page.context().storageState({ path: 'e2e/.auth/admin.json' });
  writeFileSync('e2e/.auth/password', adminPassword, { mode: 0o600 });
});
