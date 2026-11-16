import {mkdirSync, readFileSync, writeFileSync} from 'node:fs';
import {expect, test} from '@playwright/test';
import {api, expectOk, unique} from './support/api.ts';
import {compose, psql, startAgent, stopAgent} from './support/stack.ts';

const marking = readFileSync(new URL('../../internal/storage/testdata/mark_observation_fixture.sql', import.meta.url), 'utf8');

test('ordinary backend fires, globally acknowledges and resolves one alert', async ({page}) => {
  test.setTimeout(10 * 60_000);
  if (process.env.E2E_ALERT_ACCEPTANCE === '1') throw new Error('ordinary engine check requires the normal binary');
  const beganAt = new Date().toISOString();
  const name = unique('ordinary-engine');
  const metric = name.replaceAll('-', '_');
  const publishGood = (good: number) => compose('exec', '-T', 'app', 'curl', '--fail', '--silent', '--show-error',
    '--data-raw', `${metric} ${good}\n`, 'http://victoriametrics:8428/api/v1/import/prometheus');
  publishGood(9700);
  const registration = await expectOk(api(page, 'POST', '/api/v1/agents', {name, display_name: 'Ordinary alert collector'}));
  const agent = startAgent(registration.body.token);
  try {
    await expect.poll(async () => (await expectOk(api(page, 'GET', `/api/v1/agents/${registration.body.agent.id}`))).body.runtime?.reachability,
      {timeout: 60_000}).toBe('connected');
    const source = await expectOk(api(page, 'POST', '/api/v1/datasources', {
      name, display_name: name, provider: 'prometheus', configuration_format: 'prometheus-http-v1',
      endpoint: 'http://victoriametrics:8428', tls: {insecure_skip_verify: false, ca_pem: null},
      authentication: {mode: 'none', username: null, password_secret: {action: 'clear'}, token_secret: {action: 'clear'}},
      polling_interval_seconds: 60, agent_id: registration.body.agent.id,
    }));
    const sli = await expectOk(api(page, 'POST', '/api/v1/slis', {
      name, display_name: 'Ordinary engine SLI', datasource_id: source.body.id,
      measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: `${metric} or vector(9700)`, total_query: 'vector(10000)',
    }));
    const id = sli.body.id as string;
    await expect.poll(() => Number(psql(`SELECT count(*) FROM almanac_sli_observations WHERE sli_id='${id}' AND outcome='observed'`)),
      {timeout: 120_000, intervals: [1000, 2000, 5000]}).toBeGreaterThan(0);
    const start = psql(`SELECT to_char(date_trunc('minute',clock_timestamp())-interval '10 minutes','YYYY-MM-DD"T"HH24:MI:SS"Z"')`);
    // Only the disposable isolated fixture gets historical rows. Live changes
    // below come from the real collector and provider, without rewriting facts.
    psql(`BEGIN;
UPDATE almanac_sli_measurements SET effective_from='${start}' WHERE sli_id='${id}';
WITH proof AS (SELECT * FROM almanac_sli_observations WHERE sli_id='${id}' AND outcome='observed' ORDER BY interval_start LIMIT 1)
INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,
 execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,p.sli_id,p.measurement_revision,'observed',9700,10000,decode(repeat('ab',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),
 p.agent_id,p.session_generation,p.configuration_revision,p.lease_id,point,point+interval '1 minute'
FROM proof p CROSS JOIN LATERAL generate_series('${start}'::timestamptz,p.interval_start-interval '1 minute',interval '1 minute') point
ON CONFLICT DO NOTHING;
${marking.replaceAll('$1', `'${id}'::uuid`)}
COMMIT;`);
    const service = await expectOk(api(page, 'POST', '/api/v1/services', {name, display_name: 'Ordinary alert operation'}));
    const slo = await expectOk(api(page, 'POST', '/api/v1/slos', {
      name, display_name: 'Ordinary engine SLO', service_id: service.body.id, sli_id: id,
      target: '0.999', rolling_window_seconds: 2592000, evaluation_valid_from: start,
    }));
    const policy = await expectOk(api(page, 'POST', '/api/v1/alert-policies', {
      name: `Ordinary engine functional check ${name}`, slo_id: slo.body.id, severity: 'high', enabled: true,
      confirm_non_30_day_presets: false,
      // A supported custom one-minute condition keeps this functional check
      // short. Recommended fast/sustained presets have separate coverage.
      conditions: [{id: 'functional', short_window_seconds: 60, long_window_seconds: 60, threshold: '14.4'}],
    }));
    let occurrence: {id: string; state: string} | undefined;
    await expect.poll(async () => {
      occurrence = (await expectOk(api(page, 'GET', `/api/v1/alerts?scope=all&slo_id=${slo.body.id}`))).body.items[0]?.alert;
      return occurrence?.state;
    // New policies skip cutoffs before activation. Settling and dispatch
    // allowances can place the first eligible minute beyond two minutes.
    }, {timeout: 5 * 60_000, intervals: [1000, 2000, 5000]}).toBe('firing');
    const occurrenceID = occurrence!.id;
    await page.goto(`/alerts/${occurrenceID}`);
    await page.getByRole('button', {name: 'Acknowledge', exact: true}).click();
    await expect(page.getByText('Acknowledged', {exact: true}).first()).toBeVisible();
    expect((await expectOk(api(page, 'GET', `/api/v1/alerts/${occurrenceID}`))).body.state).toBe('acknowledged');
    console.log('Ordinary engine fired and global acknowledgement persisted; publishing healthy provider data.');
    publishGood(10000);
    await expect.poll(async () => (await expectOk(api(page, 'GET', `/api/v1/alerts/${occurrenceID}`))).body.state,
      {timeout: 240_000, intervals: [1000, 2000, 5000]}).toBe('resolved');
    await page.reload();
    await expect(page.getByText('Resolved', {exact: true}).first()).toBeVisible();
    const records = (await expectOk(api(page, 'GET', `/api/v1/alerts?scope=all&slo_id=${slo.body.id}`))).body.items;
    expect(records.map((item: {alert: {id: string}}) => item.alert.id)).toEqual([occurrenceID]);
    mkdirSync('../.local/ordinary-alert-engine', {recursive: true});
    writeFileSync('../.local/ordinary-alert-engine/result.json', JSON.stringify({beganAt, completedAt: new Date().toISOString(),
      policy: policy.body.policy.id, slo: slo.body.id, occurrence: occurrenceID,
      states: ['firing', 'acknowledged', 'resolved'], ordinaryBinary: process.env.E2E_ALERT_ACCEPTANCE !== '1',
      source: 'live provider changes and ordinary backend processing', capacityAcceptance: false}, null, 2));
  } finally {stopAgent(agent);}
});
