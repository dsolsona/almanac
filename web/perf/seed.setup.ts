import { writeFileSync } from 'node:fs';
import { expect, test } from '@playwright/test';
import { api, expectOk, unique } from '../e2e/support/api.ts';
import { psql, startAgent } from '../e2e/support/stack.ts';

// Performance fixture (ADR-0033): one page of 20 SLIs and 20 SLOs with PERF_DAYS of one-minute history.
// A real agent collects every SLI first, so retained history carries real collection provenance; the
// history behind that first interval is then written in SQL, and the SLOs are created through the API
// with a historical start so the backend's own preparation computes every budget and chart.
const SIZE = 20;
const DAYS = Number(process.env.PERF_DAYS ?? 35);
const TARGET = 0.999;
// Constant good-of-10,000 ratios, so the expected values are exact: remaining budget = 1 − (1 − r) / (1 − target).
const TOTAL = 10_000;
const GOOD = [10_000, 9_995, 9_990, 9_980, 9_950];
const expectedBudget = (good: number) => `${Math.round((1 - (1 - good / TOTAL) / (1 - TARGET)) * 100)}%`;

test('seed the performance fleet', async ({ page }) => {
  test.setTimeout(30 * 60_000);
  await page.goto('/');

  const agentName = unique('perf-agent');
  const registered = await expectOk(api(page, 'POST', '/api/v1/agents', { name: agentName, display_name: 'Performance agent' }));
  startAgent(registered.body.token);
  const agentID: string = registered.body.agent.id;
  await expect.poll(async () => (await api(page, 'GET', `/api/v1/agents/${agentID}`)).body.runtime?.reachability, { timeout: 60_000 }).toBe('connected');

  const none = { action: 'clear' };
  const datasource = await expectOk(api(page, 'POST', '/api/v1/datasources', {
    name: 'perf-metrics', display_name: 'Performance metrics', provider: 'prometheus', configuration_format: 'prometheus-http-v1',
    endpoint: 'http://victoriametrics:8428', tls: { insecure_skip_verify: false, ca_pem: null },
    authentication: { mode: 'none', username: null, password_secret: none, token_secret: none },
    polling_interval_seconds: 60, agent_id: agentID,
  }));

  const slis: { id: string; good: number }[] = [];
  for (let index = 0; index < SIZE; index += 1) {
    const good = GOOD[index % GOOD.length]!;
    const created = await expectOk(api(page, 'POST', '/api/v1/slis', {
      name: `perf-sli-${index}`, display_name: `Performance SLI ${String(index).padStart(2, '0')}`, datasource_id: datasource.body.id,
      measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: `vector(${good})`, total_query: `vector(${TOTAL})`,
    }));
    slis.push({ id: created.body.id, good });
  }

  // Every SLI needs one live interval from the agent before history can borrow its provenance.
  // The message lists each SLI's latest outcome, so a timeout says which ones never reported and why.
  await expect.poll(async () => {
    const list = await api(page, 'GET', '/api/v1/slis?limit=200');
    return list.body.items.filter((item: { id: string }) => slis.some((sli) => sli.id === item.id))
      .map((item: { name: string; latest_observation: { outcome: string; error_code: string | null } | null }) => `${item.name}:${item.latest_observation ? `${item.latest_observation.outcome}${item.latest_observation.error_code ? `/${item.latest_observation.error_code}` : ''}` : 'none'}`)
      .filter((state: string) => !state.endsWith(':observed'));
  }, { timeout: 5 * 60_000, intervals: [5_000] }).toEqual([]);

  const ids = `ARRAY[${slis.map((sli) => `'${sli.id}'`).join(',')}]::uuid[]`;
  const goods = slis.map((sli) => `('${sli.id}'::uuid, ${sli.good})`).join(',');
  const start = psql(`SELECT to_char(date_trunc('minute', now() AT TIME ZONE 'UTC') - interval '${DAYS} days', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`);
  const chunks: string[] = [];
  for (let day = 0; day <= DAYS; day += 1) {
    const from = `'${start}'::timestamptz + interval '${day} days'`;
    const to = `'${start}'::timestamptz + interval '${day + 1} days'`;
    // Records and values commit together, as ingestion does; the aggregator must never see one without the other.
    chunks.push(`BEGIN;
INSERT INTO public.almanac_sli_coverage
(interval_start,sli_id,measurement_revision,interval_end,outcome,result_digest,missing_components,
 execution_id,delivery_id,observation_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,p.sli_id,p.measurement_revision,point+interval '1 minute','observed',decode(repeat('ab',32),'hex'),'{}',
 gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),p.agent_id,p.session_generation,p.configuration_revision,p.lease_id,point,point+interval '1 minute'
FROM public.perf_proofs p CROSS JOIN LATERAL generate_series(${from}, least(${to}, p.first_live) - interval '1 minute', interval '1 minute') point
ON CONFLICT DO NOTHING;
INSERT INTO public.almanac_sli_observation_values (interval_start,sli_id,measurement_revision,good_count,total_count)
SELECT c.interval_start,c.sli_id,c.measurement_revision,p.good,${TOTAL}
FROM public.almanac_sli_coverage c JOIN public.perf_proofs p USING (sli_id,measurement_revision)
WHERE c.interval_start >= ${from} AND c.interval_start < least(${to}, p.first_live) AND c.outcome='observed'
ON CONFLICT DO NOTHING;
COMMIT;`);
  }
  // The fixture's own table lives in this disposable stack's database; each statement runs as its own call.
  psql(`
DROP TABLE IF EXISTS public.perf_proofs;
CREATE UNLOGGED TABLE public.perf_proofs AS
SELECT proof.*, g.good, first.first_live FROM (
  SELECT DISTINCT ON (sli_id) sli_id,measurement_revision,agent_id,session_generation,configuration_revision,lease_id
  FROM public.almanac_sli_coverage WHERE sli_id = ANY(${ids}) AND outcome='observed' ORDER BY sli_id, interval_start DESC
) proof
JOIN (VALUES ${goods}) AS g(sli_id, good) USING (sli_id)
JOIN (SELECT sli_id, min(interval_start) AS first_live FROM public.almanac_sli_coverage WHERE sli_id = ANY(${ids}) GROUP BY sli_id) first USING (sli_id);
DO $$ DECLARE month date; BEGIN
  FOR month IN SELECT generate_series(date_trunc('month', '${start}'::timestamptz), now(), interval '1 month')::date LOOP
    EXECUTE format('CREATE TABLE IF NOT EXISTS public.%I PARTITION OF public.almanac_sli_coverage FOR VALUES FROM (%L) TO (%L)', 'almanac_sli_coverage_' || to_char(month, 'YYYYMM'), month, month + interval '1 month');
    EXECUTE format('CREATE TABLE IF NOT EXISTS public.%I PARTITION OF public.almanac_sli_observation_values FOR VALUES FROM (%L) TO (%L)', 'almanac_sli_observation_values_' || to_char(month, 'YYYYMM'), month, month + interval '1 month');
  END LOOP;
END $$;
UPDATE public.almanac_sli_measurements SET effective_from = '${start}' WHERE sli_id = ANY(${ids});
`);
  // One day per call keeps each call short and shows progress; a day is 20 × 1,440 intervals.
  for (const [day, chunk] of chunks.entries()) {
    const begun = Date.now();
    psql(chunk);
    console.log(`history day ${day + 1}/${chunks.length} written in ${Date.now() - begun} ms`);
  }
  psql('DROP TABLE public.perf_proofs;');

  const services: string[] = [];
  for (const name of ['checkout', 'payments', 'search']) {
    services.push((await expectOk(api(page, 'POST', '/api/v1/services', { name: `perf-${name}`, display_name: `Performance ${name}` }))).body.id);
  }
  const slos: { id: string; sli: string; expectedBudget: string }[] = [];
  for (const [index, sli] of slis.entries()) {
    const created = await expectOk(api(page, 'POST', '/api/v1/slos', {
      name: `perf-slo-${index}`, display_name: `Performance SLO ${String(index).padStart(2, '0')}`, service_id: services[index % services.length],
      sli_id: sli.id, target: String(TARGET), rolling_window_seconds: 30 * 86400, evaluation_valid_from: start,
    }));
    slos.push({ id: created.body.id, sli: sli.id, expectedBudget: expectedBudget(sli.good) });
  }

  // Preparation runs in the backend; measurement starts only once every SLO's history is published.
  await expect.poll(async () => {
    const overview = await api(page, 'GET', '/api/v1/slos/overview?limit=50');
    return (overview.body.items ?? []).filter((item: { budget_history_state?: string; current: { performance: { remaining_fraction: string | null } } }) =>
      item.budget_history_state === 'ready' && item.current.performance.remaining_fraction !== null).length;
  }, { timeout: 20 * 60_000, intervals: [10_000] }).toBe(SIZE);

  writeFileSync('perf/.fleet.json', JSON.stringify({ days: DAYS, size: SIZE, slis, slos }, null, 2));
});
