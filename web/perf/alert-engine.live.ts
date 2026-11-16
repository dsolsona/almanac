import {createHash} from 'node:crypto';
import {mkdirSync, readFileSync, rmSync, writeFileSync} from 'node:fs';
import {cpus, totalmem} from 'node:os';
import {expect, request as apiRequest, type Browser, type Page} from '@playwright/test';
import {api, expectOk, unique} from '../e2e/support/api.ts';
import {compose, psql, restartApp, startAgent, stopAgent} from '../e2e/support/stack.ts';
import {ingestionDelta, ingestionMetrics} from './alert-calculation.metrics.ts';
import {resourceDelta, resourceSnapshot} from './resources.ts';
import {calculationEvidence} from './alert-status-evidence.ts';

const minute = 60_000;
const liveDuration = 60 * minute;
const outageDuration = 30 * minute;
const catchupBudget = 5 * minute;
const root = '../.local/alert-acceptance';
const control = `${root}/control`;
const marking = readFileSync(new URL('../../internal/storage/testdata/mark_observation_fixture.sql', import.meta.url), 'utf8');
const sqlID = (id: string) => {
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(id)) throw new Error('invalid fixture UUID');
  return `'${id}'::uuid`;
};
const idsSQL = (ids: string[]) => `ARRAY[${ids.map(sqlID).join(',')}]::uuid[]`;
const sleep = (ms: number) => new Promise<void>(resolve => setTimeout(resolve, Math.max(0, ms)));
const save = (name: string, value: unknown) => writeFileSync(`${root}/${name}.json`, JSON.stringify(value, null, 2));

type FleetItem = {index: number; sli: string; slo: string; policy: string; missing: boolean; maintained: boolean; occurrence?: string};
type Collector = {id: string; container: string};
type Source = {id: string; name: string; agent: Collector};
type Journey = {route: string; kind: string; beganAt: string; completedAt: string; milliseconds: number | null; result: string; traceID?: string};
type APISample = {user: number; route: string; beganAt: string; completedAt: string; milliseconds: number; status: number | null; result: string; evidence?: ReturnType<typeof calculationEvidence>};
type Window = {Start: string; End: string; Quality: string; Good: string; Total: string; Maintenance?: {RequestedRevision: number}};
type SavedEvaluation = {id: string; policy_id: string; scheduled_at: string; effective_cutoff: string; committed_at: string; truth: string; exclusion_revision: number; evidence: {Input: {Windows: Window[]}}};

async function collector(page: Page, name: string): Promise<Collector> {
  const registration = await expectOk(api(page, 'POST', '/api/v1/agents', {name, display_name: name}));
  const container = startAgent(registration.body.token);
  const id = registration.body.agent.id as string;
  await expect.poll(async () => (await expectOk(api(page, 'GET', `/api/v1/agents/${id}`))).body.runtime?.reachability,
    {timeout: minute}).toBe('connected');
  return {id, container};
}
function sourceBody(name: string, agent: string) {
  return {name, display_name: name, provider: 'prometheus', configuration_format: 'prometheus-http-v1',
    endpoint: 'http://victoriametrics:8428', tls: {insecure_skip_verify: false, ca_pem: null},
    authentication: {mode: 'none', username: null, password_secret: {action: 'clear'}, token_secret: {action: 'clear'}},
    polling_interval_seconds: 60, agent_id: agent};
}
async function datasource(page: Page, name: string): Promise<Source> {
  const agent = await collector(page, name);
  const source = await expectOk(api(page, 'POST', '/api/v1/datasources', sourceBody(name, agent.id)));
  return {id: source.body.id, name, agent};
}

// Fixture-only authority writes mirror the existing immutable declaration/change
// and durable-work transaction. Each declaration affects <=100 explicit SLOs;
// no production API, configuration edit or alternate calculation is introduced.
function fragmentedMaintenance(fleet: FleetItem[], base: string) {
  const scopes = fleet.filter(item => item.maintained).map(item => item.slo);
  expect(scopes.length).toBeLessThanOrEqual(100);
  for (let part = 0; part < 12; part++) {
    psql(`BEGIN;
SELECT id FROM almanac_slos WHERE id=ANY(${idsSQL(scopes)}) ORDER BY id FOR NO KEY UPDATE;
SELECT slo_id FROM almanac_slo_exclusion_state WHERE slo_id=ANY(${idsSQL(scopes)}) ORDER BY slo_id FOR UPDATE;
DO $fixture$
DECLARE actor uuid; declaration uuid:=gen_random_uuid(); audit uuid; target record;
 start_at timestamptz:='${base}'::timestamptz+interval '${part * 2} minutes'; now_at timestamptz:=clock_timestamp();
BEGIN
 SELECT id INTO STRICT actor FROM almanac_users WHERE login='admin' AND enabled AND role='administrator';
 INSERT INTO almanac_audit_events(actor,subject_id,action,outcome,occurred_at,request_id)
 VALUES(actor::text,declaration,'maintenance.create','success',now_at,'alert-live-fragment-${part}') RETURNING id INTO audit;
 INSERT INTO almanac_maintenance_exclusions(id,current_revision,creator_id,created_at,updated_at) VALUES(declaration,1,actor,now_at,now_at);
 INSERT INTO almanac_maintenance_exclusion_revisions(exclusion_id,revision,title,reason,starts_at,ends_at,operation,tombstone,actor_id,recorded_at,request_id,audit_id)
 VALUES(declaration,1,'Live acceptance fragment','Explicit isolated acceptance fixture',start_at,start_at+interval '1 minute','create',false,actor,now_at,'alert-live-fragment-${part}',audit);
 INSERT INTO almanac_maintenance_exclusion_scopes(exclusion_id,revision,slo_id) SELECT declaration,1,unnest(${idsSQL(scopes)});
 FOR target IN UPDATE almanac_slo_exclusion_state SET revision=revision+1,changed_at=now_at WHERE slo_id=ANY(${idsSQL(scopes)}) RETURNING slo_id,revision LOOP
  INSERT INTO almanac_slo_exclusion_changes(slo_id,revision,exclusion_id,exclusion_revision,included,coverage_changed,recorded_at)
  VALUES(target.slo_id,target.revision,declaration,1,true,true,now_at);
  INSERT INTO almanac_slo_exclusion_work(slo_id,desired_revision,work_version,affected_start,affected_end,next_attempt_at)
  VALUES(target.slo_id,target.revision,1,start_at,start_at+interval '1 minute',now_at)
  ON CONFLICT(slo_id) DO UPDATE SET desired_revision=excluded.desired_revision,work_version=almanac_slo_exclusion_work.work_version+1,
   affected_start=least(almanac_slo_exclusion_work.affected_start,excluded.affected_start),affected_end=greatest(almanac_slo_exclusion_work.affected_end,excluded.affected_end),next_attempt_at=least(almanac_slo_exclusion_work.next_attempt_at,excluded.next_attempt_at);
 END LOOP;
END $fixture$;
COMMIT;`);
  }
}

async function handoff(page: Page, source: Source) {
  const replacement = await collector(page, unique('engine-handoff'));
  const current = await expectOk(api(page, 'GET', `/api/v1/datasources/${source.id}`));
  const beganAt = new Date().toISOString();
  await expectOk(api(page, 'PUT', `/api/v1/datasources/${source.id}`, sourceBody(source.name, replacement.id), {'If-Match': current.etag!}));
  await expect.poll(async () => {
    const value = (await expectOk(api(page, 'GET', `/api/v1/datasources/${source.id}`))).body;
    return value.agent_id === replacement.id && value.reassignment == null;
  }, {timeout: 2 * minute, intervals: [1000]}).toBe(true);
  const old = source.agent;
  source.agent = replacement;
  stopAgent(old.container);
  return {beganAt, completedAt: new Date().toISOString(), source: source.id, previousAgent: old.id, nextAgent: replacement.id};
}

function health(fleet: FleetItem[]) {
  return JSON.parse(psql(`SELECT json_build_object('at',clock_timestamp(),'policies',coalesce(json_agg(json_build_object(
 'policy',c.policy_id,'next',c.next_scheduled_at,'cutoff',c.pending_cutoff,'waiting',c.waiting_reason,'attempts',c.failure_attempts,'exhausted',c.exhausted,
 'age_seconds',greatest(0,extract(epoch FROM clock_timestamp()-c.next_scheduled_at))) ORDER BY c.policy_id),'[]'::json))
 FROM almanac_alert_cursors c WHERE c.policy_id=ANY(${idsSQL(fleet.map(item => item.policy))})`));
}

async function browserCycle(browser: Browser, fleet: FleetItem[], journeys: Journey[], cycle: number) {
  const context = await browser.newContext({storageState: 'e2e/.auth/admin.json', ignoreHTTPSErrors: true});
  try {
    await context.addInitScript(() => {
      (window as unknown as {engineJourneys: unknown[]}).engineJourneys = [];
      window.addEventListener('almanac:journey', event => (window as unknown as {engineJourneys: unknown[]}).engineJourneys.push((event as CustomEvent).detail));
    });
    const page = await context.newPage();
    const selected = fleet.filter(item => !item.missing)[cycle % fleet.filter(item => !item.missing).length]!;
    for (const route of [
      {name: 'slo.list', path: '/slos?page_size=20'}, {name: 'slo.detail', path: `/slos/${selected.slo}`},
      {name: 'sli.list', path: '/slis?page_size=20'}, {name: 'sli.detail', path: `/slis/${selected.sli}`},
    ]) {
      const beganAt = new Date().toISOString();
      try {
        await page.goto(route.path);
        const handle = await page.waitForFunction(name => (window as unknown as {engineJourneys: {journey: string; result: string; duration: number; traceId?: string}[]}).engineJourneys.find(event => event.journey === name), route.name, {timeout: 15_000});
        const event = await handle.jsonValue() as {result: string; duration: number; traceId?: string};
        journeys.push({route: route.name, kind: 'first-visit', beganAt, completedAt: new Date().toISOString(), milliseconds: event.duration, result: event.result, traceID: event.traceId});
        if (route.name === 'slo.list') await expect(page.locator('.app-slo-row')).toHaveCount(20);
        if (route.name === 'sli.list') await expect(page.locator('.sli-list-table tbody tr')).toHaveCount(20);
      } catch {
        journeys.push({route: route.name, kind: 'first-visit', beganAt, completedAt: new Date().toISOString(), milliseconds: null, result: 'failed'});
      }
    }
    const beganAt = new Date().toISOString(), began = performance.now();
    try {
      await page.goto('/alerts');
      await expect(page.getByRole('heading', {name: 'Alerts', exact: true})).toBeVisible();
      await expect(page.getByRole('link', {name: 'New alert policy', exact: true})).toBeVisible();
      journeys.push({route: 'alert.list', kind: 'first-visit', beganAt, completedAt: new Date().toISOString(), milliseconds: performance.now() - began, result: 'ready'});
    } catch {
      journeys.push({route: 'alert.list', kind: 'first-visit', beganAt, completedAt: new Date().toISOString(), milliseconds: null, result: 'failed'});
    }
  } finally {await context.close();}
}

function lateMissingInterval(item: FleetItem) {
  // A genuinely absent minute after the missing collector stopped; never
  // rewrite a committed observation or manufacture a duplicate conflict.
  const result = psql(`BEGIN;
WITH proof AS (SELECT * FROM almanac_sli_observations WHERE sli_id=${sqlID(item.sli)} AND outcome='observed' ORDER BY interval_start DESC LIMIT 1),
 candidate AS (SELECT p.*,p.interval_start+interval '1 minute' AS late_start FROM proof p
 WHERE p.interval_start+interval '1 minute'<date_trunc('minute',clock_timestamp())-interval '2 minutes')
INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,
 execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT late_start,sli_id,measurement_revision,'observed',10000,10000,decode(repeat('bc',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),agent_id,session_generation,configuration_revision,lease_id,late_start,clock_timestamp()
FROM candidate ON CONFLICT DO NOTHING RETURNING interval_start;
${marking.replaceAll('$1', sqlID(item.sli))}
COMMIT;`);
  return {sli: item.sli, inserted: result};
}

function continuity(fleet: FleetItem[]) {
  return JSON.parse(psql(`WITH cursors AS (SELECT * FROM almanac_alert_cursors WHERE policy_id=ANY(${idsSQL(fleet.map(item => item.policy))})),
 expected AS (SELECT c.policy_id,c.revision,t.at FROM cursors c CROSS JOIN LATERAL generate_series(c.origin_at,c.next_scheduled_at-interval '1 minute',interval '1 minute') t(at)),
 missing AS (SELECT x.* FROM expected x WHERE NOT EXISTS(SELECT 1 FROM almanac_alert_evaluations e WHERE e.policy_id=x.policy_id AND e.revision=x.revision AND e.scheduled_at=x.at)
 AND NOT EXISTS(SELECT 1 FROM almanac_alert_ranges r WHERE r.policy_id=x.policy_id AND r.revision=x.revision AND r.starts_at<=x.at AND r.ends_at>x.at)),
 ordered AS (SELECT policy_id,scheduled_at,committed_at,lag(committed_at) OVER(PARTITION BY policy_id,revision ORDER BY scheduled_at) AS previous_commit FROM almanac_alert_evaluations WHERE policy_id=ANY(${idsSQL(fleet.map(item => item.policy))}))
SELECT json_build_object('expected_boundaries',(SELECT count(*) FROM expected),'unexplained_missing',(SELECT count(*) FROM missing),
 'out_of_order_commits',(SELECT count(*) FROM ordered WHERE committed_at<previous_commit),'exhausted',(SELECT count(*) FROM cursors WHERE exhausted),
 'evaluations',(SELECT count(*) FROM ordered),'ranges',(SELECT coalesce(json_agg(json_build_object('policy',policy_id,'kind',kind,'reason',reason,'start',starts_at,'end',ends_at)),'[]'::json) FROM almanac_alert_ranges WHERE policy_id=ANY(${idsSQL(fleet.map(item => item.policy))})))`));
}

function oracle(fleet: FleetItem[]) {
  // Check first and latest complete decisions for every policy against direct
  // canonical rows and immutable declaration membership at that exact revision.
  const rows = JSON.parse(psql(`WITH selected AS (
 (SELECT DISTINCT ON(policy_id) * FROM almanac_alert_evaluations WHERE policy_id=ANY(${idsSQL(fleet.map(item => item.policy))}) ORDER BY policy_id,scheduled_at)
 UNION
 (SELECT DISTINCT ON(policy_id) * FROM almanac_alert_evaluations WHERE policy_id=ANY(${idsSQL(fleet.map(item => item.policy))}) ORDER BY policy_id,scheduled_at DESC))
 SELECT coalesce(json_agg(json_build_object('id',id,'policy_id',policy_id,'scheduled_at',scheduled_at,'effective_cutoff',effective_cutoff,'committed_at',committed_at,'truth',truth,'exclusion_revision',exclusion_revision,'evidence',evidence)),'[]'::json) FROM selected`)) as SavedEvaluation[];
  const checks: unknown[] = [];
  for (const row of rows) {
    const item = fleet.find(item => item.policy === row.policy_id)!;
    expect(row.exclusion_revision).toBe(item.maintained ? 12 : 0);
    for (const w of row.evidence.Input.Windows) {
      if (w.Quality !== 'complete') {checks.push({evaluation: row.id, quality: w.Quality, checkedNumeric: false}); continue;}
      const totals = JSON.parse(psql(`WITH latest AS (
 SELECT DISTINCT ON(exclusion_id) exclusion_id,exclusion_revision,included FROM almanac_slo_exclusion_changes WHERE slo_id=${sqlID(item.slo)} AND revision<=${row.exclusion_revision} ORDER BY exclusion_id,revision DESC),
 mask AS (SELECT r.starts_at,r.ends_at FROM latest c JOIN almanac_maintenance_exclusion_revisions r ON r.exclusion_id=c.exclusion_id AND r.revision=c.exclusion_revision WHERE c.included AND NOT r.tombstone)
 SELECT json_build_object('good',coalesce(sum(o.good_count),0)::text,'total',coalesce(sum(o.total_count),0)::text,'observed',count(*))
 FROM almanac_sli_observations o WHERE o.sli_id=${sqlID(item.sli)} AND o.measurement_revision=1 AND o.outcome='observed'
 AND o.interval_start>='${w.Start}'::timestamptz AND o.interval_start<'${w.End}'::timestamptz
 AND NOT EXISTS(SELECT 1 FROM mask WHERE starts_at<=o.interval_start AND ends_at>=o.interval_start+interval '1 minute')`));
      const whole = (value: string) => {
        if (!/^\d+(?:\.0+)?$/.test(value)) throw new Error('unexpected noninteger fixture event count');
        return BigInt(value.split('.')[0]!);
      };
      expect(whole(w.Good), `raw good ${row.id}`).toBe(whole(totals.good));
      expect(whole(w.Total), `raw total ${row.id}`).toBe(whole(totals.total));
      checks.push({evaluation: row.id, windowStart: w.Start, windowEnd: w.End, good: totals.good, total: totals.total, observed: totals.observed, checkedNumeric: true});
    }
  }
  expect(new Set(rows.filter(row => row.evidence.Input.Windows.some(w => w.Quality === 'complete')).map(row => row.policy_id)).size, 'each policy has independently checkable complete evidence').toBe(fleet.length);
  return {evaluations: rows.length, checks};
}

function conditionHandoffs(fleet: FleetItem[]) {
  const policyIDs = idsSQL(fleet.map(item => item.policy));
  return Number(psql(`SELECT count(*) FROM almanac_alert_occurrences o WHERE o.policy_id=ANY(${policyIDs}) AND EXISTS(
 SELECT 1 FROM almanac_alert_evaluations e CROSS JOIN LATERAL jsonb_array_elements(e.evidence->'Evaluation'->'Conditions') c WHERE e.policy_id=o.policy_id AND e.effective_cutoff>=o.opened_at AND c->>'ID'='fast' AND c->>'Outcome'='matching') AND EXISTS(
 SELECT 1 FROM almanac_alert_evaluations e WHERE e.policy_id=o.policy_id AND EXISTS(SELECT 1 FROM jsonb_array_elements(e.evidence->'Evaluation'->'Conditions') c WHERE c->>'ID'='fast' AND c->>'Outcome'='not_matching') AND EXISTS(SELECT 1 FROM jsonb_array_elements(e.evidence->'Evaluation'->'Conditions') c WHERE c->>'ID'='sustained' AND c->>'Outcome'='matching'))`));
}

function closedHistoryReady(fleet: FleetItem[], through: string) {
  return Number(psql(`SELECT count(*) FROM almanac_slo_current_projections p
 JOIN almanac_slo_derived_generations g ON g.id=p.generation_id AND g.slo_id=p.slo_id
 JOIN almanac_slo_exclusion_state x ON x.slo_id=p.slo_id
 WHERE p.slo_id=ANY(${idsSQL(fleet.map(item => item.slo))})
 AND g.exclusion_revision=x.revision
 AND p.payload->>'budget_history_state'='ready'
 AND p.payload #>> '{budget_history_generation,generation_id}'=g.id::text
 AND p.payload #>> '{budget_history_generation,exclusion_revision}'=x.revision::text
 AND p.payload #>> '{_budget_cache,closed_ready}'='true'
 AND (p.payload #>> '{_budget_cache,closed_through}')::timestamptz>='${through}'::timestamptz
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(p.payload->'budget_sparkline') point
 WHERE point->>'good' IS NOT NULL AND point->>'total' IS NOT NULL AND point->>'remaining_fraction' IS NOT NULL)`));
}

async function pauseEvaluation() {
  const requestedAt = new Date().toISOString();
  writeFileSync(`${control}/pause`, 'pause evaluator only');
  // This event is emitted only after the prior bounded turn has returned.
  await expect.poll(() => compose('logs', '--no-color', '--since', requestedAt, 'app')
    .includes('"msg":"alert acceptance evaluator paused"'), {timeout: 30_000, intervals: [1000]}).toBe(true);
  return {requestedAt, acknowledgedAt: new Date().toISOString()};
}

export async function runAlertEngineLive(page: Page, browser: Browser, size: number) {
  if (![100, 200].includes(size) || process.env.E2E_ALERT_ACCEPTANCE !== '1') throw new Error('live acceptance requires the tagged deployment and size100/200');
  mkdirSync(root, {recursive: true}); mkdirSync(control, {recursive: true});
  const name = unique('engine-live'), beganAt = new Date().toISOString();
  const phaseEpoch = Number(psql(`SELECT extract(epoch FROM date_trunc('minute',clock_timestamp())+interval '20 minutes')::bigint`));
  const goodQuery = `vector(9000 + 1000 * (time() >= bool ${phaseEpoch}))`;
  const main = await datasource(page, `${name}-main`), missing = await datasource(page, `${name}-missing`);
  const fleet: FleetItem[] = [], journeys: Journey[] = [], apiSamples: APISample[] = [], healthSamples: unknown[] = [], events: unknown[] = [], ingestionSamples: unknown[] = [];
  const failures: string[] = [];
  const plannedOutages: {start: number; end: number}[] = [];
  let stopped = false, missingStopped = false, background: Promise<unknown>[] = [];
  let segmentStart = ingestionMetrics();
  const segments: unknown[] = [];
  const checkpoint = () => {save('live-progress', {beganAt, size, phaseEpoch, fleet, events, failures, healthSamples, ingestionSamples, journeys, apiSamples, segments, readyToCommitLatency: {available: false, reason: 'No authoritative per-boundary prerequisite-ready timestamp is recorded; scheduled/committed delay is reported separately.'}, productionActivated: false});};
  const metricSample = () => {
    const end = ingestionMetrics();
    ingestionSamples.push({at: end.at, sinceSegmentStart: ingestionDelta(segmentStart, end)});
    return end;
  };
  try {
    const slis: {id: string; missing: boolean}[] = [];
    for (let index = 0; index < size; index++) {
      const isMissing = index >= size - size / 10;
      const result = await expectOk(api(page, 'POST', '/api/v1/slis', {name: `${name}-${index}`, display_name: `Engine SLI ${String(index).padStart(3, '0')}`,
        datasource_id: isMissing ? missing.id : main.id, measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: goodQuery, total_query: 'vector(10000)'}));
      slis.push({id: result.body.id, missing: isMissing});
    }
    await expect.poll(() => Number(psql(`SELECT count(DISTINCT sli_id) FROM almanac_sli_observations WHERE sli_id=ANY(${idsSQL(slis.map(item => item.id))}) AND outcome='observed'`)), {timeout: 5 * minute, intervals: [1000]}).toBe(size);
    const start = psql(`SELECT to_char(date_trunc('minute',clock_timestamp())-interval '7 hours','YYYY-MM-DD"T"HH24:MI:SS"Z"')`);
    for (const {id} of slis) {
      psql(`BEGIN;
UPDATE almanac_sli_measurements SET effective_from='${start}' WHERE sli_id=${sqlID(id)};
WITH proof AS (SELECT * FROM almanac_sli_observations WHERE sli_id=${sqlID(id)} AND outcome='observed' ORDER BY interval_start LIMIT 1)
INSERT INTO almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,
 execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,p.sli_id,p.measurement_revision,'observed',9000,10000,decode(repeat('ab',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),p.agent_id,p.session_generation,p.configuration_revision,p.lease_id,point,point+interval '1 minute'
FROM proof p CROSS JOIN LATERAL generate_series('${start}'::timestamptz,p.interval_start-interval '1 minute',interval '1 minute') point ON CONFLICT DO NOTHING;
${marking.replaceAll('$1', sqlID(id))}
COMMIT;`);
    }
    // Complete the canonical seed before creating consumers.
    await expect.poll(() => Number(psql(`SELECT count(*) FROM almanac_derived_dirty_ranges WHERE sli_id=ANY(${idsSQL(slis.map(item => item.id))})`)), {timeout: 5 * minute, intervals: [1000]}).toBe(0);
    const service = await expectOk(api(page, 'POST', '/api/v1/services', {name, display_name: 'Live engine acceptance'}));
    for (const [index, item] of slis.entries()) {
      const slo = await expectOk(api(page, 'POST', '/api/v1/slos', {name: `${name}-${index}`, display_name: `Engine SLO ${String(index).padStart(3, '0')}`,
        service_id: service.body.id, sli_id: item.id, target: '0.999', rolling_window_seconds: 2592000, evaluation_valid_from: start}));
      fleet.push({index, sli: item.id, slo: slo.body.id, policy: '', missing: item.missing, maintained: index % 4 < 2});
    }
    fragmentedMaintenance(fleet, psql(`SELECT to_char(date_trunc('minute',clock_timestamp())-interval '25 minutes','YYYY-MM-DD"T"HH24:MI:SS"Z"')`));
    for (const item of fleet) {
      const policy = await expectOk(api(page, 'POST', '/api/v1/alert-policies', {name: `Live engine policy ${item.index}`, slo_id: item.slo, severity: 'high', enabled: true,
        confirm_non_30_day_presets: false, conditions: [{id: 'fast', short_window_seconds: 300, long_window_seconds: 3600, threshold: '14.4'}, {id: 'sustained', short_window_seconds: 1800, long_window_seconds: 21600, threshold: '6'}]}));
      item.policy = policy.body.policy.id;
    }
    const policyIDs = idsSQL(fleet.map(item => item.policy));
    await expect.poll(() => Number(psql(`SELECT count(DISTINCT policy_id) FROM almanac_alert_occurrences WHERE policy_id=ANY(${policyIDs}) AND state='firing'`)), {timeout: 5 * minute, intervals: [1000]}).toBe(size);
    expect(Date.now(), 'fixture must produce initial breach before the fixed provider recovery phase').toBeLessThan(phaseEpoch * 1000);
    // Acknowledge every initial occurrence through the real public commands.
    for (const item of fleet) {
      const record = (await expectOk(api(page, 'GET', `/api/v1/alerts?scope=active&slo_id=${item.slo}`))).body.items[0].alert;
      item.occurrence = record.id;
      for (let attempt = 0; attempt < 3; attempt++) {
        const detail = await expectOk(api(page, 'GET', `/api/v1/alerts/${record.id}`));
        if (detail.body.state === 'acknowledged') break;
        expect(detail.body.state, 'initial occurrence remains firing until acknowledged').toBe('firing');
        const acknowledged = await api(page, 'POST', `/api/v1/alerts/${record.id}/acknowledge`, {}, {'If-Match': detail.etag!, 'Idempotency-Key': `live-ack-${record.id}`});
        if (acknowledged.status === 200) break;
        expect(acknowledged.status, 'only a confirmed concurrent version conflict may retry').toBe(409);
        expect(attempt, 'acknowledgement must converge within the bounded conflict attempts').toBeLessThan(2);
      }
    }
    // Exercise the exact final read/query paths now, before starting the60m
    // clock. Syntax, envelope, revision and raw-population defects fail early.
    save('live-initial-oracle', oracle(fleet));
    const initialContinuity = continuity(fleet);
    expect(initialContinuity.unexplained_missing).toBe(0);
    save('live-initial-continuity', { ...initialContinuity, conditionHandoffs: conditionHandoffs(fleet), health: health(fleet) });
    expect(fleet.some(item => item.missing && !item.maintained)).toBe(true);
    expect(fleet.some(item => item.missing && item.maintained)).toBe(true);
    const closedThrough = psql(`SELECT to_char(date_trunc('hour',clock_timestamp()),'YYYY-MM-DD"T"HH24:MI:SS"Z"')`);
    await expect.poll(() => closedHistoryReady(fleet, closedThrough), {timeout: 5 * minute, intervals: [1000]}).toBe(size);
    save('live-history-preflight', {at: new Date().toISOString(), closedThrough, readySLOs: size, numericSamplesRequired: true});
    stopAgent(missing.agent.container); missingStopped = true;
    const baselinePause = await pauseEvaluation();
    // Keep exactly the same definitions, masks, collectors and background SLO
    // work as the load phase. Only evaluation and concurrent readers are absent.
    const baselineStart = ingestionMetrics();
    await sleep(minute);
    const baselineEnd = ingestionMetrics(), baseline = ingestionDelta(baselineStart, baselineEnd);
    expect(baseline.accepted).toBeGreaterThan(0);
    save('ingestion-baseline', {baselineStart, baselineEnd, baseline, baselinePause,
      configuredSLIs: size, configuredSLOs: size, policies: size, collectingSLIs: size - size / 10,
      fragmentedSLOs: size / 2, evaluationPaused: true});
    rmSync(`${control}/pause`, {force: true});
    const beforeResources = resourceSnapshot();
    const image = compose('images', '--quiet', 'app').trim(), database = psql('SELECT version()');
    const liveStart = Date.now(), liveEnd = liveStart + liveDuration;
    const rollover = Math.ceil(liveStart / (60 * minute)) * 60 * minute;
    const sourcePauseAt = rollover - 5000, sourceResumeAt = rollover + 4 * minute;
    let handedOff = false, restarted = false, sourcePaused = false, sourceResumed = false, lateInserted = false;
    segmentStart = ingestionMetrics();
    save('live-profile', {beganAt, liveStart: new Date(liveStart).toISOString(), fixedLiveMinutes: 60, fixedOutageMinutes: size === 200 ? 30 : 0, catchupBudgetMinutes: 5,
      size, distinctSLIs: new Set(fleet.map(item => item.sli)).size, distinctSLOs: new Set(fleet.map(item => item.slo)).size, conditions: 2 * size,
      missingCohort: size / 10, fragmentedSLOs: size / 2, fragmentsPerSLO: 12, historicalGood: 9000, total: 10000, goodQuery, phaseEpoch,
      fixtureVersion: 'alert-engine-live-v2',
      cpu: cpus()[0]?.model, logicalCPUs: cpus().length, memoryBytes: totalmem(), postgres: database,
      image, sourceHashes: Object.fromEntries(['alert-engine.live.ts', 'alert-engine.perf.ts'].map(file => [file, createHash('sha256').update(readFileSync(new URL(file, import.meta.url))).digest('hex')])),
      apiClients: 10, apiCycleSeconds: 10, browserLanes: 1, browserCycleSeconds: 30,
      numericalWorkerProfile: 'alertacceptance-v1; actual bounds are logged by the tagged backend', productionActivated: false});
    background = [
      (async () => {for (let cycle = 0; !stopped; cycle++) {try {await browserCycle(browser, fleet, journeys, cycle);} catch {failures.push('browser_cycle_failed');} await sleep(30_000);}})(),
      (async () => {while (!stopped) {try {healthSamples.push(health(fleet)); metricSample(); checkpoint();} catch {events.push({kind: 'health_or_metrics_unavailable', at: new Date().toISOString()});} await sleep(minute);}})(),
      ...Array.from({length: 10}, async (_, user) => {
        const client = await apiRequest.newContext({baseURL: process.env.E2E_BASE_URL ?? 'https://localhost:18188', storageState: 'e2e/.auth/admin.json', ignoreHTTPSErrors: true});
        try {
          for (let cycle = 0; !stopped; cycle++) {
            const began = performance.now();
            const healthy = fleet.filter(item => !item.missing);
            const selected = healthy[(user * 10 + cycle) % healthy.length]!;
            for (const [route, path] of [['overview', '/api/v1/slos/overview?limit=20'], ['status', `/api/v1/slos/${selected.slo}/status`], ['history', `/api/v1/slos/${selected.slo}/operational?period=window&burn_window_seconds=3600`]] as const) {
              const beganAt = new Date().toISOString(), requestStart = performance.now();
              let status: number | null = null;
              try {
                const response = await client.get(path, {timeout: 5000});
                status = response.status();
                try {
                  const body = await response.json();
                  const usable = status === 200 && (route === 'overview' ? Array.isArray(body.items) && body.items.length === 20 : body.current != null);
                  apiSamples.push({user, route, beganAt, completedAt: new Date().toISOString(), milliseconds: performance.now() - requestStart, status, result: usable ? 'complete' : 'failed', evidence: route === 'status' ? calculationEvidence(body) : undefined});
                } finally {await response.dispose();}
              } catch {
                apiSamples.push({user, route, beganAt, completedAt: new Date().toISOString(), milliseconds: performance.now() - requestStart, status, result: 'failed'});
              }
            }
            if (!stopped) await sleep(Math.max(0, 10_000 - (performance.now() - began)));
          }
        } finally {await client.dispose();}
      }),
    ];
    // Every scenario runs with the same live collectors and browser lane. The
    // final rollover probe may extend past60m; the live phase is never shortened.
    while (Date.now() < Math.max(liveEnd, sourceResumeAt + minute)) {
      const now = Date.now();
      if (!handedOff && now >= liveStart + 5 * minute) {events.push({kind: 'collector-handoff', ...await handoff(page, main)}); handedOff = true;}
      if (!restarted && now >= liveStart + 20 * minute) {
        const end = metricSample(); segments.push({kind: 'pre-restart', ...ingestionDelta(segmentStart, end)});
        const restartStarted = Date.now();
        events.push({kind: 'backend-restart-start', at: new Date(restartStarted).toISOString()});
        restartApp(); segmentStart = ingestionMetrics();
        plannedOutages.push({start: restartStarted, end: Date.now()});
        events.push({kind: 'backend-restart-complete', at: new Date().toISOString()}); restarted = true;
      }
      if (!sourcePaused && now >= sourcePauseAt) {
        writeFileSync(`${control}/pause-source`, 'pause canonical/current source loop only'); sourcePaused = true;
        events.push({kind: 'source-pause', at: new Date().toISOString()});
      }
      if (sourcePaused && !lateInserted && now >= rollover + 2 * minute) {
        const late = lateMissingInterval(fleet.find(item => item.missing && !item.maintained)!);
        events.push({kind: 'late-interval', ...late});
        if (!late.inserted) failures.push('late_interval_fixture_inserted_no_missing_minute');
        lateInserted = true;
      }
      if (sourcePaused && !sourceResumed && now >= sourceResumeAt) {
        const pending = Number(psql(`SELECT count(*) FROM almanac_alert_cursors WHERE policy_id=ANY(${policyIDs}) AND pending_cutoff IS NOT NULL AND waiting_reason IN ('aggregate_pending','exclusion_calculation_pending')`));
        events.push({kind: 'source-pending-original-heads', at: new Date().toISOString(), pending, health: health(fleet)});
        if (pending === 0) failures.push('source_pause_did_not_exercise_pending_boundaries');
        rmSync(`${control}/pause-source`, {force: true}); sourceResumed = true;
        events.push({kind: 'source-resume', at: new Date().toISOString()});
      }
      const falselyResolved = Number(psql(`SELECT count(*) FROM almanac_alert_occurrences WHERE policy_id=ANY(${idsSQL(fleet.filter(item => item.missing).map(item => item.policy))}) AND state='resolved'`));
      if (falselyResolved > 0) {failures.push('missing_cohort_falsely_resolved'); break;}
      await sleep(10_000);
    }
    events.push({kind: 'live-complete', at: new Date().toISOString(), elapsedMs: Date.now() - liveStart});
    const recovered = Number(psql(`SELECT count(DISTINCT policy_id) FROM almanac_alert_occurrences WHERE policy_id=ANY(${idsSQL(fleet.filter(item => !item.missing).map(item => item.policy))}) AND state='resolved'`));
    expect(recovered, 'healthy cohort must recover from the provider phase').toBe(size - size / 10);
    const handoffs = conditionHandoffs(fleet);
    expect(handoffs, 'persisted fast→sustained handoff on one occurrence').toBeGreaterThan(0);
    const retainedAcknowledgements = Number(psql(`SELECT count(*) FROM almanac_alert_occurrences WHERE policy_id=ANY(${policyIDs}) AND payload->>'AcknowledgedBy' IS NOT NULL AND payload->>'AcknowledgedBy'<>''`));
    expect(retainedAcknowledgements, 'acknowledgement survives handoff and recovery on original occurrences').toBe(size);
    expect(Number(psql(`SELECT count(*) FROM almanac_alert_occurrences WHERE policy_id=ANY(${policyIDs})`)), 'initial occurrences retain identity throughout handoff/recovery').toBe(size);
    if (size === 200) {
      const pauseAt = new Date().toISOString();
      const observedBefore = Number(psql(`SELECT count(*) FROM almanac_sli_observations WHERE sli_id=ANY(${idsSQL(fleet.filter(item => !item.missing).map(item => item.sli))})`));
      const pause = await pauseEvaluation();
      const countBefore = Number(psql(`SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=ANY(${policyIDs})`));
      events.push({kind: 'evaluator-outage-start', at: pauseAt, ...pause, evaluations: countBefore}); checkpoint();
      await sleep(outageDuration);
      const countAfter = Number(psql(`SELECT count(*) FROM almanac_alert_evaluations WHERE policy_id=ANY(${policyIDs})`));
      const observedAfter = Number(psql(`SELECT count(*) FROM almanac_sli_observations WHERE sli_id=ANY(${idsSQL(fleet.filter(item => !item.missing).map(item => item.sli))})`));
      expect(observedAfter - observedBefore, 'live collection continues throughout evaluator outage').toBeGreaterThanOrEqual((size - size / 10) * 29);
      const resumeAt = Date.now(); rmSync(`${control}/pause`, {force: true});
      events.push({kind: 'evaluator-outage-end', at: new Date(resumeAt).toISOString(), evaluations: countAfter, observationsAdded: observedAfter - observedBefore, health: health(fleet)});
      // A turn already executing when the marker was created can finish; no
      // new claims should run during the stable30m pause interval.
      expect(countAfter, 'stable evaluator pause must not commit new evaluations').toBe(countBefore);
      await expect.poll(() => Number(psql(`SELECT count(*) FROM almanac_alert_cursors WHERE policy_id=ANY(${policyIDs}) AND next_scheduled_at<=date_trunc('minute',clock_timestamp())-interval '1 minute'`)), {timeout: catchupBudget, intervals: [1000]}).toBe(0);
      events.push({kind: 'catchup-complete', at: new Date().toISOString(), elapsedMs: Date.now() - resumeAt, health: health(fleet)});
    }
    const endMetrics = metricSample(); segments.push({kind: 'final', ...ingestionDelta(segmentStart, endMetrics)});
    // End network lanes before synchronous final evidence extraction; raw oracle
    // Docker calls must not inflate an in-flight client's measured duration.
    stopped = true; await Promise.allSettled(background);
    const checked = continuity(fleet), raw = oracle(fleet);
    save('live-oracle', raw); save('live-continuity', checked);
    const apiDistributions = Object.fromEntries(['overview', 'status', 'history'].map(route => {
      const values = apiSamples.filter(sample => sample.route === route).map(sample => sample.milliseconds).sort((a, b) => a - b);
      return [route, {count: values.length, failed: apiSamples.filter(sample => sample.route === route && sample.result !== 'complete').length, p50: values[Math.ceil(values.length * 0.5) - 1], p95: values[Math.ceil(values.length * 0.95) - 1], max: values.at(-1)}];
    }));
    const duringRestart = (sample: {beganAt: string; completedAt: string}) => plannedOutages.some(outage => Date.parse(sample.beganAt) <= outage.end && Date.parse(sample.completedAt) >= outage.start);
    save('live-api', {samples: apiSamples, distributions: apiDistributions, plannedOutages});
    const distributions = JSON.parse(psql(`SELECT json_build_object('boundary_to_commit_seconds',json_build_object(
 'p50',percentile_cont(0.5) WITHIN GROUP(ORDER BY extract(epoch FROM committed_at-scheduled_at)),
 'p95',percentile_cont(0.95) WITHIN GROUP(ORDER BY extract(epoch FROM committed_at-scheduled_at)),
 'p99',percentile_cont(0.99) WITHIN GROUP(ORDER BY extract(epoch FROM committed_at-scheduled_at)),
 'max',max(extract(epoch FROM committed_at-scheduled_at)))) FROM almanac_alert_evaluations WHERE policy_id=ANY(${policyIDs})`));
    save('live-result', {completedAt: new Date().toISOString(), size, recovered, handoffs, continuity: checked, distributions, apiDistributions,
      readyToCommitLatency: {available: false, reason: 'Authoritative prerequisite-ready timestamps are not recorded.'},
      resources: resourceDelta(beforeResources, resourceSnapshot()), failures, plannedOutages, productionActivated: false, acceptanceComplete: false,
      remainingGates: ['ready-to-commit instrumentation and reviewed numerical targets', 'deterministic24h/beyond-horizon fixtures remain separate PostgreSQL tests']});
    for (const [route, distribution] of Object.entries(apiDistributions)) {
      expect(distribution.count, `${route} overlapping API calls`).toBeGreaterThanOrEqual(600);
      expect(distribution.p95, `${route} API p95, including interrupted calls`).toBeLessThanOrEqual(route === 'history' ? 500 : 200);
    }
    expect(checked.unexplained_missing).toBe(0); expect(checked.out_of_order_commits).toBe(0); expect(checked.exhausted).toBe(0);
    expect(failures).toEqual([]);
    expect(apiSamples.filter(sample => sample.result !== 'complete' && !duringRestart(sample)), 'API failures outside the deliberately restarted backend').toEqual([]);
    expect(journeys.filter(item => item.result !== 'ready' && !duringRestart(item)), 'browser failures outside deliberate backend restart').toEqual([]);
    for (const route of ['slo.list', 'slo.detail', 'sli.list', 'sli.detail']) {
      const timings = journeys.filter(item => item.route === route && item.milliseconds !== null).map(item => item.milliseconds!).sort((a, b) => a - b);
      expect(timings.length, `${route} samples overlapping live/catchup`).toBeGreaterThanOrEqual(60);
      expect(timings.at(-1), `${route} complete usable journey max`).toBeLessThan(1000);
    }
    if (baseline.p95LowerSeconds === 0) throw new Error('ingestion ratio cannot be certified from baseline histogram lower bound');
    for (const segment of segments as ReturnType<typeof ingestionDelta>[]) {
      expect(segment.accepted).toBeGreaterThan(0); expect(segment.p95UpperSeconds).not.toBeNull();
      expect(segment.p95UpperSeconds!).toBeLessThanOrEqual(0.250);
      expect(segment.p95UpperSeconds!).toBeLessThanOrEqual(1.25 * baseline.p95LowerSeconds);
    }
  } finally {
    stopped = true;
    rmSync(`${control}/pause`, {force: true}); rmSync(`${control}/pause-source`, {force: true});
    await Promise.allSettled(background);
    if (!missingStopped) stopAgent(missing.agent.container);
    checkpoint();
  }
}
