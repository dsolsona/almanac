// Focused retained-fixture check; no application rows are changed. Run from web/
// through make shell SOCKET=1 with E2E_POSTGRES_STORAGE=disk.
import assert from 'node:assert/strict';
import { performance } from 'node:perf_hooks';
import { psql } from '../e2e/support/stack.ts';
import { publishedHistoryReadinessSQL } from './alert-calculation.readiness.ts';

const started = performance.now();
const id = psql("SELECT id FROM public.almanac_slos WHERE name LIKE 'perf-slo-%' ORDER BY name LIMIT 1");
const cutoff = psql("SELECT to_char(date_trunc('hour',now() AT TIME ZONE 'UTC'),'YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')");
const ids: string[] = JSON.parse(psql("SELECT json_agg(id ORDER BY name) FROM public.almanac_slos WHERE name LIKE 'perf-slo-%'"));
assert.equal(ids.length, 200, 'expected the retained 200-SLO experiment');
const liveStarted = performance.now();
const liveReady = Number(psql(`BEGIN READ ONLY; SET LOCAL statement_timeout='10s'; ${publishedHistoryReadinessSQL(ids, cutoff)}; COMMIT;`));
console.log(`retained fixture: ${liveReady}/200 published histories at ${cutoff} in ${((performance.now()-liveStarted)/1000).toFixed(3)}s`);
assert.equal(liveReady, 200, 'retained seed must actually meet the corrected precondition');
const query = publishedHistoryReadinessSQL([id], cutoff).replaceAll('public.', 'pg_temp.');
const tables = ['almanac_slos', 'almanac_slo_definitions', 'almanac_sli_measurements',
  'almanac_slo_current_projections', 'almanac_slo_derived_generations', 'almanac_slo_extrema_cursor', 'almanac_slo_extrema'];
const cases: [string, string, number][] = [
  ['published history despite stale current deadline', `UPDATE almanac_slo_current_projections
    SET payload=jsonb_set(payload,'{_current_valid_until}','"2000-01-01T00:00:00Z"'),current_valid_until='2000-01-01T00:00:00Z';
    DO $stale$ BEGIN IF NOT (SELECT bool_and(current_valid_until::timestamptz<now()) FROM almanac_slo_current_projections)
      THEN RAISE EXCEPTION 'test did not expire the current deadline'; END IF; END $stale$`, 1],
  ['missing closed hour', 'DELETE FROM almanac_slo_extrema WHERE range_start=(SELECT max(range_start) FROM almanac_slo_extrema WHERE resolution_seconds=3600) AND resolution_seconds=3600', 0],
  ['relevant dirty history', `UPDATE almanac_slo_extrema_cursor SET dirty_from='${cutoff}'::timestamptz-interval '1 hour',dirty_to='${cutoff}'`, 0],
  ['incompatible definition revision', 'UPDATE almanac_slo_derived_generations SET definition_revision=definition_revision+1', 0],
  ['incompatible measurement revision', 'UPDATE almanac_slo_derived_generations SET measurement_revision=measurement_revision+1', 0],
  ['incompatible calculation version', "UPDATE almanac_slo_derived_generations SET calculation_version='obsolete'", 0],
  ['replaced covering extrema', "UPDATE almanac_slo_extrema SET published_at=now()+interval '1 second' WHERE resolution_seconds=3600", 0],
  ['unpublished cache coverage', "UPDATE almanac_slo_current_projections SET payload=jsonb_set(payload,'{_budget_cache,closed_through}','\"2000-01-01T00:00:00Z\"')", 0],
  ['missing projection', 'DELETE FROM almanac_slo_current_projections', 0],
  ['unavailable published history', `UPDATE almanac_slo_current_projections SET payload=jsonb_set(payload,'{budget_history_state}','"unavailable"')`, 0],
  ['open-hour dirt does not block closed history', `UPDATE almanac_slo_extrema_cursor SET dirty_from='${cutoff}',dirty_to='${cutoff}'::timestamptz+interval '1 hour'`, 1],
  ['unrelated exhausted open-hour work', `UPDATE almanac_slo_extrema_cursor SET attempts=5,dirty_from='${cutoff}',dirty_to='${cutoff}'::timestamptz+interval '1 hour'`, 1],
];
// One repeatable-read snapshot, private copies and savepoints prevent live worker
// writes from changing the test and prevent fault injection from affecting them.
const copies = tables.map((table) => {
  const scope = table === 'almanac_slos' ? `id='${id}'` : table === 'almanac_sli_measurements'
    ? `sli_id=(SELECT sli_id FROM public.almanac_slo_definitions WHERE slo_id='${id}' ORDER BY revision DESC LIMIT 1)`
    : `slo_id='${id}'`;
  return `CREATE TEMP TABLE ${table} ON COMMIT DROP AS SELECT * FROM public.${table} WHERE ${scope}; ANALYZE ${table}`;
}).join(';\n');
const checks = cases.map(([name, mutation, expected]) => `SAVEPOINT fault;
${mutation};
DO $check$ BEGIN IF (${query}) <> ${expected} THEN RAISE EXCEPTION '${name}'; END IF; END $check$;
ROLLBACK TO SAVEPOINT fault;`).join('\n');
const result = psql(`BEGIN ISOLATION LEVEL REPEATABLE READ;
SET LOCAL statement_timeout='10s';
${copies};
SET LOCAL search_path=pg_temp;
${checks}
SELECT 'passed ${cases.length} rollback cases';
ROLLBACK;`);
assert.equal(result, `passed ${cases.length} rollback cases`);
console.log(`${result} in ${((performance.now()-started)/1000).toFixed(3)}s`);
