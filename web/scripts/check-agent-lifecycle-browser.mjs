import assert from 'node:assert/strict';
import { writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { expect } from '@playwright/test';
import { readResponseJSON } from './response-read-diagnostics.mjs';

export async function runAgentLifecycleJourney({ page, context, fixture, setStage, registration, tokenEvidence }) {
  const origin = new URL(fixture.url).origin;
  const agentPath = `/api/v1/agents/${registration.agent_id}`;
  const history = page.locator('.agents-credentials').and(page.getByRole('region', { name: 'Credential history', exact: true }));
  const mutations = [];
  const inspectEmptyDialog = async (dialog, label) => {
    await expect(dialog.getByLabel('Current password', { exact: true })).toHaveCount(0);
    await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
    for (const width of [1440, 360]) {
      await page.setViewportSize({ width, height: 900 });
      for (const theme of ['light', 'dark']) {
        // The page theme button is inert behind a modal. Set the same root class
        // used by the shared theme control solely for modal visual inspection.
        await page.locator('html').evaluate((element, dark) => element.classList.toggle('dark', dark), theme === 'dark');
        await expect(dialog).toBeVisible();
        assert.equal(await dialog.evaluate((element) => element.getBoundingClientRect().width <= window.innerWidth), true);
        await page.screenshot({ path: join(fixture.artifacts, `${label}-${width}-${theme}.png`), animations: 'disabled' });
      }
    }
  };
  page.on('request', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname.startsWith(`${agentPath}/`)) mutations.push(new URL(request.url()).pathname);
  });
  const submit = async (dialog, suffix) => {
    setStage(`lifecycle ${suffix} response wait`);
    const [response] = await Promise.all([
      page.waitForResponse((response) => response.request().method() === 'POST' && new URL(response.url()).pathname === `${agentPath}${suffix}`),
      dialog.getByRole('button', { name: suffix === '/disable' ? 'Disable agent' : suffix === '/credentials/rotate' ? 'Rotate credential' : 'Revoke credential', exact: true }).click(),
    ]);
    setStage(`lifecycle ${suffix} response status ${response.status()}`);
    assert.equal(response.status(), 200);
    return response;
  };

  setStage('credential rotation confirmation');
  await page.getByRole('button', { name: 'Rotate credential', exact: true }).click();
  const rotation = page.getByRole('dialog', { name: 'Rotate credential', exact: true });
  await inspectEmptyDialog(rotation, 'rotation-form');
  setStage('credential rotation overlap entry');
  await rotation.getByLabel('Keep the previous credential valid for', { exact: true }).selectOption('0');
  setStage('credential rotation no premature mutation');
  assert.equal(mutations.length, 0);
  const rotatedResponse = await submit(rotation, '/credentials/rotate');
  setStage('credential rotation response decoding');
  const rotated = await readResponseJSON(rotatedResponse);
  setStage('credential rotation fresh issuance and target');
  assert.equal(rotated.replayed, false);
  assert.equal(rotated.agent.id, registration.agent_id);
  assert.equal(rotated.previous_credential.id, registration.credential_id);
  setStage('credential rotation exact retirement time');
  assert.equal(rotated.previous_credential.retire_at, rotated.credential.issued_at);
  setStage('credential rotation expected version and overlap');
  assert.equal((await rotatedResponse.request().allHeaders())['if-match'], `"agent:${registration.agent_id}:1"`);
  assert.equal(rotatedResponse.request().postDataJSON().overlap_seconds, 0);
  setStage('replacement token copy and close');
  const tokenDialog = page.getByRole('dialog', { name: 'Agent token', exact: true });
  await expect(tokenDialog).toBeVisible();
  const token = await tokenDialog.locator('output[aria-label="Agent token"]').textContent();
  assert.equal(token, rotated.token);
  const replacement = tokenEvidence(token);
  assert.equal(replacement.credential_id, rotated.credential.id);
  assert.notEqual(replacement.credential_id, registration.credential_id);
  await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin });
  await tokenDialog.getByRole('button', { name: 'Copy token', exact: true }).click();
  assert.equal(await page.evaluate(() => navigator.clipboard.readText()), token);
  await expect(tokenDialog.getByRole('status').filter({ hasText: /^Token copied\.$/ })).toBeVisible();
  await tokenDialog.getByRole('button', { name: 'Close token', exact: true }).click();
  await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
  assert.equal((await page.locator('body').textContent()).includes(token), false);
  await expect(history.locator('.agents-credential')).toHaveCount(2);

  setStage('credential revocation confirmation');
  const row = history.locator('.agents-credential').filter({ hasText: replacement.credential_id });
  await row.getByRole('button', { name: 'Revoke credential', exact: true }).click();
  const revoke = page.getByRole('dialog', { name: 'Revoke credential', exact: true });
  await expect(revoke).toContainText(replacement.credential_id);
  await inspectEmptyDialog(revoke, 'revoke-confirmation');
  await revoke.getByRole('checkbox', { name: 'I understand this action takes effect immediately.' }).check();
  assert.equal(mutations.length, 1);
  const revokedResponse = await submit(revoke, `/credentials/${replacement.credential_id}/revoke`);
  setStage('credential revocation response decoding');
  const revoked = await readResponseJSON(revokedResponse);
  assert.equal(revoked.changed, true);
  assert.equal(revoked.credential.id, replacement.credential_id);
  assert.ok(revoked.credential.revoked_at);
  assert.equal((await revokedResponse.request().allHeaders())['if-match'], `"agent:${registration.agent_id}:2"`);
  await expect(revoke).not.toBeVisible();
  await expect(row).toContainText(revoked.credential.revoked_at.replace('T', ' '));

  setStage('agent disable confirmation');
  await page.getByRole('button', { name: 'Disable agent', exact: true }).click();
  const disable = page.getByRole('dialog', { name: 'Disable agent', exact: true });
  await expect(disable).toContainText('browser-normal');
  await inspectEmptyDialog(disable, 'disable-confirmation');
  await disable.getByRole('checkbox', { name: 'I understand this action takes effect immediately.' }).check();
  assert.equal(mutations.length, 2);
  const disabledResponse = await submit(disable, '/disable');
  setStage('agent disable response decoding');
  const disabled = await readResponseJSON(disabledResponse);
  assert.equal(disabled.agent.enabled, false);
  assert.equal((await disabledResponse.request().allHeaders())['if-match'], `"agent:${registration.agent_id}:3"`);
  await expect(disable).not.toBeVisible();
  await expect(page.getByText('Disabled', { exact: true })).toBeVisible();
  assert.equal(mutations.length, 3);
  await page.reload();
  await expect(page.getByText('Disabled', { exact: true })).toBeVisible();
  await expect(history.locator('.agents-credential')).toHaveCount(2);
  await expect(page.locator('output[aria-label="Agent token"]')).toHaveCount(0);
  setStage('unassigned agent deletion');
  await page.getByRole('button', { name: 'Delete agent', exact: true }).click();
  const deletion = page.getByRole('dialog', { name: 'Delete agent', exact: true });
  await expect(deletion.getByText('No datasources use this agent.')).toBeVisible();
  await inspectEmptyDialog(deletion, 'delete-confirmation');
  await deletion.getByRole('checkbox').check();
  const [deleted] = await Promise.all([
    page.waitForResponse((response) => response.request().method() === 'DELETE' && new URL(response.url()).pathname === agentPath),
    deletion.getByRole('button', { name: 'Delete agent', exact: true }).click(),
  ]);
  assert.equal(deleted.status(), 200);
  assert.deepEqual(deleted.request().postDataJSON(), {});
  await expect(page.getByRole('button', { name: 'Back to agents' })).toHaveCount(0);
  assert.equal(await page.evaluate(async (path) => (await fetch(path, { credentials: 'same-origin' })).status, agentPath), 404);
  await writeFile(join(fixture.artifacts, 'agent-lifecycle.json'), JSON.stringify({
    agent_id: registration.agent_id, ...replacement, mutation_count: mutations.length,
  }), { mode: 0o600 });
}
