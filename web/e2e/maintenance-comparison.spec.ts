import { readFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { api, expectOk, unique } from './support/api.ts';
import { psql, startAgent, stopAgent } from './support/stack.ts';

const mark = readFileSync(new URL('../../internal/storage/testdata/mark_observation_fixture.sql', import.meta.url), 'utf8');

test('compare the same measured population before and after excluding maintenance', async ({ page }, info) => {
  test.setTimeout(240_000);
  const name = unique('maintenance-population');
  const service = await expectOk(api(page, 'POST', '/api/v1/services', { name, display_name: 'Checkout' }));
  const registration = await expectOk(api(page, 'POST', '/api/v1/agents', { name, display_name: 'Maintenance comparison collector' }));
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

    const first = Date.parse(timeOfFirst());
    const start = new Date(first - 3 * 60_000).toISOString();
    const end = new Date(first - 2 * 60_000).toISOString();
    // Extend only the disposable fixture. Borrow real collector provenance and
    // add one failed maintenance interval and two ordinary pre-existing intervals.
    psql(`BEGIN;
UPDATE almanac_sli_measurements SET effective_from='${start}' WHERE sli_id='${id}';
WITH proof AS (SELECT * FROM almanac_sli_observations WHERE sli_id='${id}' AND outcome='observed' ORDER BY interval_start LIMIT 1)
INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,
 execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,p.sli_id,p.measurement_revision,'observed',CASE WHEN point='${start}'::timestamptz THEN 0 ELSE 99 END,
 100,decode(repeat('ab',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),p.agent_id,p.session_generation,p.configuration_revision,p.lease_id,
 point,point+interval '1 minute'
FROM proof p CROSS JOIN LATERAL generate_series('${start}'::timestamptz,p.interval_start-interval '1 minute',interval '1 minute') point;
${mark.replaceAll('$1', `'${id}'::uuid`)}
COMMIT;`);
    const slo = await expectOk(api(page, 'POST', '/api/v1/slos', {
      name, display_name: 'Purchase flow availability', service_id: service.body.id, sli_id: id,
      target: '0.99', rolling_window_seconds: 86400, evaluation_valid_from: start,
    }));
    const route = `/slos/${slo.body.id}`;
    const recorded = Number(psql(`SELECT count(*) FROM almanac_sli_observations WHERE sli_id='${id}' AND outcome='observed';`));
    const eligibleTotal = (recorded - 1) * 100;
    const rawTotal = recorded * 100;
    expect(recorded).toBeGreaterThanOrEqual(4);

    await expectOk(api(page, 'POST', '/api/v1/maintenance', {
      title: 'Checkout database upgrade', reason: 'Replace the database replicas.',
      start, end, slo_ids: [slo.body.id],
    }));
    await expect.poll(async () => {
      const response = await api(page, 'GET', `/api/v1${route}/status`);
      const current = response.body.current;
      return [current?.quality.excluded_intervals, Number(current?.quality.excluded_total), Number(current?.performance.total)];
    }, { timeout: 60_000, intervals: [1000, 3000] }).toEqual([1, 100, eligibleTotal]);

    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(route);
    const comparison = page.locator('summary').filter({ hasText: 'Maintenance calculation comparison' });
    await expect(comparison).toContainText('1 excluded interval');
    await comparison.click();
    const total = page.getByRole('row', { name: /Total events/ }).getByRole('cell');
    await expect(total).toHaveText([eligibleTotal.toLocaleString('en'), rawTotal.toLocaleString('en')]);
    const failed = page.getByRole('row', { name: /Unsuccessful/ }).getByRole('cell');
    await expect(failed).toHaveText([(recorded - 1).toLocaleString('en'), (recorded + 99).toLocaleString('en')]);
    const attainment = page.getByRole('row', { name: /Attainment/ }).getByRole('cell');
    await expect(attainment.first()).toHaveText('99%');
    const expectedRawAttainment = Number((((recorded - 1) * 99 * 100) / rawTotal).toFixed(10));
    await expect(attainment.last()).toHaveText(`${expectedRawAttainment}%`);
    const screenshot = info.outputPath('maintenance-population-comparison.png');
    await page.locator('.slo-detail-disclosure').filter({ hasText: 'Maintenance calculation comparison' }).screenshot({ path: screenshot });
    await info.attach('maintenance-population-comparison', { path: screenshot, contentType: 'image/png' });
  } finally {
    if (!stopped) stopAgent(agent);
  }
});
