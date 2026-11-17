import {mkdirSync, readFileSync, writeFileSync} from 'node:fs';
import {expect, test} from '@playwright/test';
import {api, expectOk, unique} from '../e2e/support/api.ts';
import {psql, startAgent} from '../e2e/support/stack.ts';
import {runAlertEngineLive} from './alert-engine.live.ts';

const root = '../.local/alert-acceptance';
const mode = process.env.E2E_ALERT_ENGINE_MODE ?? 'smoke';
if (!['smoke', 'live'].includes(mode)) throw new Error('E2E_ALERT_ENGINE_MODE must be smoke or live');
const size = Number(process.env.E2E_ALERT_ENGINE_SIZE ?? '1');
if (![1, 100, 200].includes(size)) throw new Error('E2E_ALERT_ENGINE_SIZE must be 1, 100 or 200');
const marking = readFileSync(new URL('../../internal/storage/testdata/mark_observation_fixture.sql', import.meta.url), 'utf8');

test('deployed engine creates durable occurrences and globally acknowledges them', async ({page}) => {
  test.skip(mode !== 'smoke');
  test.setTimeout(15 * 60_000);
  if (process.env.E2E_ALERT_ACCEPTANCE !== '1') throw new Error('requires the acceptance-only deployment');
  mkdirSync(root, {recursive: true});
  const started = new Date().toISOString();
  const name = unique('engine');
  const registered = await expectOk(api(page, 'POST', '/api/v1/agents', {name, display_name: 'Engine acceptance collector'}));
  const agentContainer = startAgent(registered.body.token);
  const agentID = registered.body.agent.id;
  await expect.poll(async () => (await api(page, 'GET', `/api/v1/agents/${agentID}`)).body.runtime?.reachability,
    {timeout: 60_000}).toBe('connected');
  const source = await expectOk(api(page, 'POST', '/api/v1/datasources', {
    name, display_name: name, provider: 'prometheus', configuration_format: 'prometheus-http-v1',
    endpoint: 'http://victoriametrics:8428', tls: {insecure_skip_verify: false, ca_pem: null},
    authentication: {mode: 'none', username: null, password_secret: {action: 'clear'}, token_secret: {action: 'clear'}},
    polling_interval_seconds: 60, agent_id: agentID,
  }));
  const slis: string[] = [];
  for (let i = 0; i < size; i++) {
    const result = await expectOk(api(page, 'POST', '/api/v1/slis', {
      name: `${name}-${i}`, display_name: `Engine SLI ${i}`, datasource_id: source.body.id,
      measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: 'vector(10000)', total_query: 'vector(10000)',
    }));
    slis.push(result.body.id);
  }
  await expect.poll(() => Number(psql(`SELECT count(DISTINCT sli_id) FROM almanac_sli_observations WHERE sli_id=ANY(ARRAY[${slis.map(id=>`'${id}'`).join(',')}]::uuid[]) AND outcome='observed'`)),
    {timeout: 120_000, intervals: [1000]}).toBe(size);
  const start = psql(`SELECT to_char(date_trunc('minute',now())-interval '7 hours','YYYY-MM-DD"T"HH24:MI:SS"Z"')`);
  // At most 420 historical rows per SLI transaction, with authentic collector
  // provenance and the same source/dirty marking as other isolated fixtures.
  for (const id of slis) {
    psql(`BEGIN;
UPDATE almanac_sli_measurements SET effective_from='${start}' WHERE sli_id='${id}';
WITH proof AS (SELECT * FROM almanac_sli_observations WHERE sli_id='${id}' AND outcome='observed' ORDER BY interval_start LIMIT 1)
INSERT INTO almanac_sli_observations
(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,
 execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,p.sli_id,p.measurement_revision,'observed',9700,10000,decode(repeat('ab',32),'hex'),'{}',
 gen_random_uuid(),gen_random_uuid(),p.agent_id,p.session_generation,p.configuration_revision,p.lease_id,point,point+interval '1 minute'
FROM proof p CROSS JOIN LATERAL generate_series('${start}'::timestamptz,p.interval_start-interval '1 minute',interval '1 minute') point
ON CONFLICT DO NOTHING;
${marking.replaceAll('$1', `'${id}'::uuid`)}
COMMIT;`);
  }
  const service = await expectOk(api(page, 'POST', '/api/v1/services', {name, display_name: 'Engine acceptance'}));
  const fleet: {sli: string; slo: string; policy: string}[] = [];
  for (const [index, sli] of slis.entries()) {
    const slo = await expectOk(api(page, 'POST', '/api/v1/slos', {name: `${name}-${index}`, display_name: `Engine SLO ${index}`,
      service_id: service.body.id, sli_id: sli, target: '0.999', rolling_window_seconds: 2592000, evaluation_valid_from: start}));
    const created = await expectOk(api(page, 'POST', '/api/v1/alert-policies', {name: `Engine policy ${index}`, slo_id: slo.body.id,
      severity: 'high', enabled: true, confirm_non_30_day_presets: false,
      conditions: [{id: 'fast', short_window_seconds: 300, long_window_seconds: 3600, threshold: '14.4'},
        {id: 'sustained', short_window_seconds: 1800, long_window_seconds: 21600, threshold: '6'}]}));
    fleet.push({sli, slo: slo.body.id, policy: created.body.policy.id});
  }
  writeFileSync(`${root}/fleet.json`, JSON.stringify({started, size, start, fleet, agentContainer, productionActivated: false}, null, 2));
  const policyIDs = fleet.map(item=>`'${item.policy}'`).join(',');
  await expect.poll(() => Number(psql(`SELECT count(DISTINCT policy_id) FROM almanac_alert_occurrences WHERE policy_id=ANY(ARRAY[${policyIDs}]::uuid[])`)),
    {timeout: 5 * 60_000, intervals: [1000]}).toBe(size);
  const occurrence = (await expectOk(api(page, 'GET', `/api/v1/alerts?scope=all&slo_id=${fleet[0]!.slo}`))).body.items[0].alert;
  expect(occurrence.state).toBe('firing');
  await page.goto(`/alerts/${occurrence.id}`);
  await page.getByRole('button', {name: 'Acknowledge', exact: true}).click();
  await expect(page.getByText('Acknowledged', {exact: true}).first()).toBeVisible();
  const after = await expectOk(api(page, 'GET', `/api/v1/alerts/${occurrence.id}`));
  expect(after.body.state).toBe('acknowledged');
  const journals = JSON.parse(psql(`SELECT json_agg(to_jsonb(e)) FROM almanac_alert_evaluations e WHERE policy_id=ANY(ARRAY[${policyIDs}]::uuid[])`));
  writeFileSync(`${root}/smoke.json`, JSON.stringify({started, completed: new Date().toISOString(), size,
    fleet, journals, acknowledged: occurrence.id, capacityAcceptance: false, productionActivated: false}, null, 2));
});


test('live independent policy fleets preserve evidence, ingestion and navigation', async ({page, browser}) => {
  test.skip(mode !== 'live');
  test.setTimeout(125 * 60_000);
  await runAlertEngineLive(page, browser, size);
});
