import { expect, test } from '@playwright/test';
import type { Agent } from '../src/agents/client.ts';
import { api, expectOk } from './support/api.ts';
import { compose, localCommand, psql } from './support/stack.ts';

test('seed local-dev once, then start and reconnect it with the local app', async ({ page }) => {
  test.setTimeout(180_000);
  // A manually registered namesake must never be adopted or receive a new credential.
  psql("INSERT INTO almanac_agents(name, display_name, created_at, updated_at) VALUES ('local-dev', 'Existing agent', now(), now())");
  expect(() => localCommand('agent-seed')).toThrow();
  expect(psql("SELECT display_name FROM almanac_agents WHERE name='local-dev'")).toBe('Existing agent');
  expect(psql('SELECT count(*) FROM almanac_agent_credentials')).toBe('0');
  psql("DELETE FROM almanac_agents WHERE name='local-dev'");

  localCommand('agent-seed');
  const identity = psql("SELECT a.id || ':' || c.id FROM almanac_agents a JOIN almanac_agent_credentials c ON c.agent_id=a.id WHERE a.name='local-dev'");
  const tokenHash = () => compose('exec', '-T', '--user', 'root', 'postgres', 'sha256sum', '/run/almanac-agent/token').trim();
  const originalHash = tokenHash();
  expect(compose('exec', '-T', 'postgres', 'stat', '-c', '%u:%a', '/run/almanac-agent/token').trim()).toBe('10001:600');
  localCommand('agent-seed');
  expect(tokenHash()).toBe(originalHash);
  expect(psql("SELECT a.id || ':' || c.id FROM almanac_agents a JOIN almanac_agent_credentials c ON c.agent_id=a.id WHERE a.name='local-dev'")).toBe(identity);

  // Changing only unused Base64 pad bits preserves the verifier but invalidates the token.
  compose('exec', '-T', '--user', 'root', 'postgres', 'sh', '-c', `
    set -eu
    cp -p /run/almanac-agent/token /run/almanac-agent/token.saved
    token=$(cat /run/almanac-agent/token)
    last=$(printf '%s' "$token" | tail -c 1 | tr 'AEIMQUYcgkosw048' 'BFJNRVZdhlptx159')
    printf '%s%s' "\${token%?}" "$last" > /run/almanac-agent/token
  `);
  try {
    expect(() => localCommand('agent-seed')).toThrow();
  } finally {
    compose('exec', '-T', '--user', 'root', 'postgres', 'mv', '/run/almanac-agent/token.saved', '/run/almanac-agent/token');
  }

  localCommand('run');
  const agentID = identity.split(':')[0];
  await page.goto('/agents');
  const connected = async () => (await expectOk(api<Agent>(page, 'GET', `/api/v1/agents/${agentID}`), [200])).body.runtime?.reachability;
  await expect.poll(connected).toBe('connected');
  await page.getByRole('button', { name: 'local-dev', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'local-dev', exact: true })).toBeVisible();
  const generation = psql(`SELECT generation FROM almanac_agent_ownership WHERE agent_id='${agentID}'`);
  compose('stop', 'agent');
  localCommand('start');
  await expect.poll(connected).toBe('connected');
  await expect.poll(() => psql(`SELECT generation FROM almanac_agent_ownership WHERE agent_id='${agentID}'`)).not.toBe(generation);
  expect(tokenHash()).toBe(originalHash);
  expect(compose('logs', 'agent')).not.toContain('almanac_agent_v1_');

  // Explicit revocation stays revoked; seeding is not an authority reset.
  psql(`UPDATE almanac_agent_credentials SET revoked_at=now() WHERE agent_id='${agentID}'`);
  expect(() => localCommand('agent-seed')).toThrow();
  expect(psql(`SELECT count(*) FROM almanac_agent_credentials WHERE agent_id='${agentID}' AND revoked_at IS NOT NULL`)).toBe('1');
  expect(tokenHash()).toBe(originalHash);
});
