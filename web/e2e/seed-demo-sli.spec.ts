import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { api, expectOk, unique } from './support/api.ts';
import { psql, startAgent, stopAgent } from './support/stack.ts';

const seed = readFileSync(new URL('../../scripts/seed-demo-sli.sql', import.meta.url), 'utf8');
const mark = readFileSync(new URL('../../internal/storage/testdata/mark_observation_fixture.sql', import.meta.url), 'utf8');

test('seed a demo SLI and recalculate an existing SLO', async ({ page }) => {
  test.setTimeout(240_000);
  const name = unique('seed-demo-sli');
  const service = await expectOk(api(page, 'POST', '/api/v1/services', { name, display_name: 'Checkout' }));
  const registration = await expectOk(api(page, 'POST', '/api/v1/agents', { name, display_name: 'Demo fixture collector' }));
  const agent = startAgent(registration.body.token);
  let stopped = false;
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
    stopAgent(agent);
    stopped = true;

    const start = new Date(Date.parse(timeOfFirst()) - 3 * 60_000).toISOString();
    // Establish an older, already-valid definition in the disposable stack.
    psql(`UPDATE almanac_sli_measurements SET effective_from='${start}' WHERE sli_id='${id}';`);
    const slo = await expectOk(api(page, 'POST', '/api/v1/slos', {
      name, display_name: 'Demo availability', service_id: service.body.id, sli_id: id,
      target: '0.995', rolling_window_seconds: 86400, evaluation_valid_from: start,
    }));
    const status = async () => (await api(page, 'GET', `/api/v1/slos/${slo.body.id}/status`)).body.current?.performance;
    const collect = () => psql(`\\set sli '${name}'
\\set days 1
BEGIN;
${seed}
PREPARE mark_fixture(uuid) AS
${mark}
EXECUTE mark_fixture(:'fixture_id');
COMMIT;`);
    const initial = Number(psql(`SELECT count(*) FROM almanac_sli_observations WHERE sli_id='${id}';`));
    await expect.poll(async () => Number((await status())?.total), { timeout: 60_000 }).toBe(initial * 100);
    const [,, firstEnd, firstCount] = collect().split('|');
    const [,, secondEnd, secondCount] = collect().split('|');
    const seeded = Number(firstCount) + Number(secondCount);
    // If the minute changes between calls, only the newly settled point can be added.
    expect(Number(secondCount)).toBeLessThanOrEqual((Date.parse(secondEnd!) - Date.parse(firstEnd!)) / 60_000);
    expect(seeded).toBeGreaterThanOrEqual(3);
    await expect.poll(async () => {
      const result = await status();
      return [Number(result?.good), Number(result?.total), Number(result?.remaining_fraction)];
    }, { timeout: 60_000 }).toEqual([(initial + seeded) * 99, (initial + seeded) * 100, -1]);
  } finally {
    if (!stopped) stopAgent(agent);
  }
});
