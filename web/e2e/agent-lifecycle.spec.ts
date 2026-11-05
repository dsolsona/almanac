import { expect, test } from '@playwright/test';
import { api, expectOk, unique } from './support/api.ts';

// Managing an agent's credentials and lifecycle: rotate a credential (the new token is shown once),
// revoke one, disable the agent, then delete it. The agent is registered through the API because
// registration itself is covered by the collection journey.
test('rotate and revoke credentials, then disable and delete the agent', async ({ page }) => {
  const name = unique('lifecycle');
  const registered = await expectOk(api(page, 'POST', '/api/v1/agents', { name, display_name: name }));
  const agentID: string = registered.body.agent.id;

  await page.goto(`/agents?agent=${agentID}`);
  await expect(page.getByRole('heading', { name, exact: true })).toBeVisible();
  const history = page.getByRole('region', { name: 'Credential history', exact: true });
  await expect(history.locator('.agents-credential')).toHaveCount(1);

  await test.step('rotate the credential and see the new token once', async () => {
    await page.getByRole('button', { name: 'Rotate credential', exact: true }).click();
    const rotation = page.getByRole('dialog', { name: 'Rotate credential', exact: true });
    await rotation.getByRole('radiogroup', { name: 'Keep the previous credential valid for', exact: true }).getByRole('radio', { name: 'Immediately', exact: true }).click();
    await rotation.getByRole('button', { name: 'Rotate credential', exact: true }).click();
    const tokenDialog = page.getByRole('dialog', { name: 'Agent token', exact: true });
    const token = (await tokenDialog.locator('output[aria-label="Agent token"]').textContent())!;
    expect(token.length).toBeGreaterThan(20);
    await tokenDialog.getByRole('button', { name: 'Close token', exact: true }).click();
    await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
    expect(await page.locator('body').textContent()).not.toContain(token);
    await expect(history.locator('.agents-credential')).toHaveCount(2);
  });

  await test.step('revoke the current credential', async () => {
    const current = history.locator('.agents-credential').filter({ hasText: 'Current' });
    await current.getByRole('button', { name: 'Revoke credential', exact: true }).click();
    const revoke = page.getByRole('dialog', { name: 'Revoke credential', exact: true });
    await revoke.getByRole('checkbox', { name: 'I understand this action takes effect immediately.' }).check();
    await revoke.getByRole('button', { name: 'Revoke credential', exact: true }).click();
    await expect(revoke).not.toBeVisible();
    await expect(history.getByText(/revoked/).first()).toBeVisible();
  });

  await test.step('disable the agent; the state survives a reload', async () => {
    await page.getByRole('button', { name: 'Disable agent', exact: true }).click();
    const disable = page.getByRole('dialog', { name: 'Disable agent', exact: true });
    await disable.getByRole('checkbox', { name: 'I understand this action takes effect immediately.' }).check();
    await disable.getByRole('button', { name: 'Disable agent', exact: true }).click();
    await expect(disable).not.toBeVisible();
    await page.reload();
    await expect(page.getByText('Disabled', { exact: true }).first()).toBeVisible();
  });

  await test.step('delete the agent; it leaves the list', async () => {
    await page.getByRole('button', { name: 'Delete agent', exact: true }).click();
    const deletion = page.getByRole('dialog', { name: 'Delete agent', exact: true });
    await expect(deletion.getByText('No datasources use this agent.')).toBeVisible();
    await deletion.getByRole('checkbox').check();
    await deletion.getByRole('button', { name: 'Delete agent', exact: true }).click();
    await expect(page.getByRole('status').filter({ hasText: 'Agent deleted' })).toBeVisible();
    await expect(page.getByRole('button', { name, exact: true })).toHaveCount(0);
    expect((await api(page, 'GET', `/api/v1/agents/${agentID}`)).status).toBe(404);
  });
});
