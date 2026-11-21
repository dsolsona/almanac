import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { api,expectOk,unique } from './support/api.ts';
import { compose, psql, restartApp, startAgent, stopAgent } from './support/stack.ts';

// Runs after every other journey: restart the application container and check that what the
// journeys created is still there and that collection resumes. Sessions are revoked on restart
// only if the server says so; signing in again proves the rotated password persisted.
test('data and collection survive an application restart', async ({ page }) => {
  test.setTimeout(240_000);
  await page.goto('/slis');
  await expect(page.locator('.sli-list-name strong').first()).toBeVisible();
  const names = await page.locator('.sli-list-name strong').allTextContents();
  expect(names.length).toBeGreaterThan(0);
  // The end of the newest observed interval across the SLIs, in milliseconds (0 if none).
  const latest = async () => {
    const page1 = await api(page, 'GET', '/api/v1/slis?limit=200');
    return Math.max(0, ...page1.body.items
      .filter((item: { latest_observation: { outcome: string } | null }) => item.latest_observation?.outcome === 'observed')
      .map((item: { latest_observation: { interval_end: string } }) => Date.parse(item.latest_observation.interval_end)));
  };
  const before = await latest();
  expect(before).toBeGreaterThan(0);

  const provider=await expectOk(api(page,'POST','/api/v1/services',{name:unique('restart-provider'),display_name:'Restart provider'}));
  const service=await expectOk(api(page,'POST','/api/v1/services',{name:unique('restart-service'),display_name:'Restart service',owner:'Persistence',labels:{team:['reliability']},dependencies:[provider.body.id]}));
  const archived=await expectOk(api(page,'POST','/api/v1/services',{name:unique('restart-archived'),display_name:'Archived service'}));
  expect((await api(page,'DELETE','/api/v1/services/'+archived.body.id,undefined,{'If-Match':archived.etag!})).status).toBe(204);
  restartApp();

  await page.goto('/');
  if (await page.getByRole('heading', { name: 'Sign in', exact: true }).isVisible()) {
    await page.getByLabel('Login', { exact: true }).fill('admin');
    await page.getByLabel('Password', { exact: true }).fill(readFileSync('e2e/.auth/password', 'utf8'));
    await page.getByLabel('Password', { exact: true }).press('Enter');
  }
  await page.goto('/slis');
  await expect(page.locator('.sli-list-name strong')).toHaveText(names);

  const persisted=await expectOk(api(page,'GET','/api/v1/services/'+service.body.id));
  expect(persisted.body.owner).toBe('Persistence');expect(persisted.body.labels).toEqual({team:['reliability']});expect(persisted.body.dependencies).toEqual([provider.body.id]);
  expect((await api(page,'GET','/api/v1/services/'+archived.body.id)).status).toBe(404);
  // The agent reconnects by itself and collection continues: a newer observed interval than before the restart arrives.
  await expect.poll(latest, { timeout: 180_000, intervals: [5_000] }).toBeGreaterThan(before);
});


test('repair overdue measurements after the backend and agent restart', async ({ page }) => {
  test.setTimeout(240_000);
  const name = unique('restart-repair');
  const service = await expectOk(api(page, 'POST', '/api/v1/services', { name, display_name: 'Checkout' }));
  const registration = await expectOk(api(page, 'POST', '/api/v1/agents', { name, display_name: 'Historical collector' }));
  let agent = startAgent(registration.body.token);
  try {
    await expect.poll(async () => (await api(page, 'GET', `/api/v1/agents/${registration.body.agent.id}`)).body.runtime?.reachability, { timeout: 30_000 }).toBe('connected');
    const source = await expectOk(api(page, 'POST', '/api/v1/datasources', {
      name, display_name: 'Checkout metrics', provider: 'prometheus', configuration_format: 'prometheus-http-v1',
      endpoint: 'http://victoriametrics:8428', tls: { insecure_skip_verify: false, ca_pem: null },
      authentication: { mode: 'none', username: null, password_secret: { action: 'clear' }, token_secret: { action: 'clear' } },
      polling_interval_seconds: 60, agent_id: registration.body.agent.id,
    }));
    const sli = await expectOk(api(page, 'POST', '/api/v1/slis', {
      name, display_name: 'Successful checkout requests', datasource_id: source.body.id, measurement_kind: 'event_counts',
      component_mode: 'interval_count', good_query: 'vector(99)', total_query: 'vector(100)',
    }));
    const id = String(sli.body.id);
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    const timeOfFirst = () => psql(`SELECT to_char(min(interval_start) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')
      FROM almanac_sli_observations WHERE sli_id='${id}' AND outcome='observed';`);
    await expect.poll(timeOfFirst, { timeout: 120_000, intervals: [1000, 3000] }).toMatch(/Z$/);
    const first = Date.parse(timeOfFirst());
    const start = new Date(first - 12 * 60_000).toISOString();
    stopAgent(agent);
    agent = '';
    // Move the start only in this isolated fixture to create a 12-minute
    // outage that is already older than the automatic delivery allowance.
    psql(`UPDATE public.almanac_sli_measurements SET effective_from='${start}' WHERE sli_id='${id}';`);
    const slo = await expectOk(api(page, 'POST', '/api/v1/slos', {
      name, display_name: 'Availability across restart', service_id: service.body.id, sli_id: id,
      target: '0.995', rolling_window_seconds: 86400, evaluation_valid_from: start,
    }));
    const repaired = () => Number(psql(`SELECT count(*) FROM public.almanac_sli_observations
      WHERE sli_id='${id}' AND backfill_chunk_id IS NOT NULL AND interval_start<'${new Date(first).toISOString()}';`));
    expect(repaired()).toBe(0);

    compose('stop', 'app');
    restartApp();
    agent = startAgent(registration.body.token);
    await page.goto(`/slis/${id}`);
    const disclosure = page.locator('summary').filter({ hasText: 'Historical backfill' });
    await disclosure.click();
    await expect(page.getByRole('list', { name: 'Backfill jobs' })).toContainText('Automatic repair', { timeout: 120_000 });
    await expect.poll(repaired, { timeout: 120_000, intervals: [1000, 5000] }).toBeGreaterThanOrEqual(7);
    await expect.poll(async () => {
      const current = (await api(page, 'GET', `/api/v1/slos/${slo.body.id}/status`)).body.current;
      return [Number(current?.performance?.remaining_fraction), Number(current?.performance?.total) >= 800];
    }, { timeout: 60_000 }).toEqual([-1, true]);
  } finally {
    if (agent) stopAgent(agent);
  }
});
