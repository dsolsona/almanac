import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { cpus, totalmem } from 'node:os';
import { expect, test, request as apiRequest, type Page } from '@playwright/test';
import { compose, psql } from '../e2e/support/stack.ts';
import { resourceSnapshot, ingestionBetween } from './resources.ts';
import { ingestionMetrics, ingestionDelta } from './alert-calculation.metrics.ts';

// F4-T02: SQL prototype is installed only in the disposable almanac-e2e database.
const root = '../.local/alert-calculation';
const columns = 'ordinal integer,good text,total text,observed bigint,known_empty bigint,zero_traffic bigint,first_observation timestamptz,last_observation timestamptz,latest_receipt timestamptz,prepared boolean,exhausted boolean';
const parameters = 'uuid,bigint,timestamptz[],timestamptz[],timestamptz[],timestamptz[],timestamptz[],timestamptz[],timestamptz[],timestamptz';

function asynchronousSQL(sql: string, label: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const startedAt = new Date().toISOString();
    let inputError: string | undefined;
    const child = execFile('docker', ['compose', '-p', 'almanac-e2e', '-f', '../compose.yaml', '-f', '../compose.e2e.yaml',
      'exec', '-T', 'postgres', 'psql', '--no-psqlrc', '--quiet', '--tuples-only', '--no-align', '-v', 'ON_ERROR_STOP=1', '-U', 'almanac', '-d', 'almanac', '-f', '-'],
    { encoding: 'utf8', timeout: 180_000, maxBuffer: 32 * 1024 * 1024 }, (error, stdout, stderr) => {
      writeFileSync(`${root}/${label}.jsonl`, stdout);
      writeFileSync(`${root}/${label}.stderr`, stderr);
      writeFileSync(`${root}/${label}.process.json`, JSON.stringify({ startedAt, finishedAt: new Date().toISOString(),
        code: error?.code, signal: error?.signal, killed: error?.killed, inputError }, null, 2));
      if (error || inputError) reject(new Error(`experiment SQL failed (${label}): ${error?.message ?? inputError}; ${stderr.slice(-3000)}`)); else resolve(stdout);
    });
    // A deadline may terminate psql while a large backfill input is still queued.
    // Preserve that failure through the worker outcome instead of an unhandled EPIPE.
    child.stdin!.on('error', error => { inputError = error.message; });
    child.stdin!.end(sql);
  });
}

async function rendered(page: Page, route: string, seen: number) {
  const handle = await page.waitForFunction(({ route, seen }) => {
    const events = (window as unknown as { alertJourneys: { journey: string; duration: number; result: string }[] }).alertJourneys.filter(e => e.journey === route);
    return events.length > seen ? events.at(-1) : null;
  }, { route, seen }, { timeout: 15_000 });
  const event = await handle.jsonValue() as { duration: number; result: string };
  expect(event.result).toBe('ready');
  return event.duration;
}

const count = (page: Page, route: string) => page.evaluate(route =>
  (window as unknown as { alertJourneys: { journey: string }[] }).alertJourneys.filter(e => e.journey === route).length, route);

test('200-SLI query contention across rollover and three-day backfill', async ({ browser }) => {
  test.setTimeout(5 * 60_000);
  mkdirSync(root, { recursive: true });
  const fleet = JSON.parse(readFileSync('perf/.alert-fleet.json', 'utf8')) as { slis: { id: string; good: number }[]; slos: { id: string }[] };
  expect(fleet.slis).toHaveLength(200);
  if (fleet.slis.some(s => !/^[0-9a-f-]{36}$/.test(s.id))) throw new Error('invalid fixture UUID');
  const candidate = readFileSync('../internal/storage/testdata/alert_window_fallback.sql', 'utf8');
  const generated = readFileSync('../internal/storage/dbgen/retained_window_batch.sql.go', 'utf8');
  const baseline = generated.match(/const readRetainedWindowBatch = `([\s\S]*?)`/)?.[1];
  if (!baseline) throw new Error('cannot read current generated baseline query');
  psql(`CREATE FUNCTION public.alert_experiment_baseline(${parameters}) RETURNS TABLE(${columns}) LANGUAGE sql STABLE AS $query$${baseline}$query$;
CREATE FUNCTION public.alert_experiment_candidate(${parameters},integer,integer) RETURNS TABLE(${columns},dirty_hours bigint,raw_rows bigint) LANGUAGE sql STABLE AS $query$${candidate}$query$;
CREATE FUNCTION public.alert_experiment_compare(id uuid,cut timestamptz,candidate_first boolean) RETURNS jsonb LANGUAGE plpgsql AS $fn$
DECLARE duration interval; start_at timestamptz; fs timestamptz; fe timestamptz; began timestamptz;
ends timestamptz[]='{}'; starts timestamptz[]='{}'; finishes timestamptz[]='{}'; a timestamptz[]='{}'; b timestamptz[]='{}'; c timestamptz[]='{}'; d timestamptz[]='{}';
base jsonb; proposed jsonb; reference jsonb; base_ms numeric; proposed_ms numeric;
BEGIN
 FOREACH duration IN ARRAY ARRAY[interval '5 minutes',interval '1 hour',interval '30 minutes',interval '6 hours'] LOOP
  start_at=cut-duration;fs=date_trunc('hour',start_at);IF fs<start_at THEN fs=fs+interval '1 hour';END IF;
  fe=date_trunc('hour',cut);IF fs>fe THEN fs=cut;fe=cut;END IF;
  ends=array_append(ends,cut);starts=array_append(starts,fs);finishes=array_append(finishes,fe);
  a=array_append(a,start_at);b=array_append(b,fs);c=array_append(c,fe);d=array_append(d,cut);
 END LOOP;
 IF candidate_first THEN
  began=clock_timestamp();SELECT jsonb_agg(to_jsonb(r)) INTO proposed FROM public.alert_experiment_candidate(id,1,ends,starts,finishes,a,b,c,d,cut+interval '10 seconds',8,7200) r;proposed_ms=extract(epoch FROM clock_timestamp()-began)*1000;
 END IF;
 began=clock_timestamp();SELECT jsonb_agg(to_jsonb(r)) INTO base FROM public.alert_experiment_baseline(id,1,ends,starts,finishes,a,b,c,d,cut+interval '10 seconds') r;base_ms=extract(epoch FROM clock_timestamp()-began)*1000;
 IF NOT candidate_first THEN
  began=clock_timestamp();SELECT jsonb_agg(to_jsonb(r)) INTO proposed FROM public.alert_experiment_candidate(id,1,ends,starts,finishes,a,b,c,d,cut+interval '10 seconds',8,7200) r;proposed_ms=extract(epoch FROM clock_timestamp()-began)*1000;
 END IF;
 SELECT jsonb_agg(to_jsonb(r)) INTO reference FROM (
  SELECT n AS ordinal,coalesce(sum(o.good_count) FILTER(WHERE o.outcome='observed'),0)::text AS good,
   coalesce(sum(o.total_count) FILTER(WHERE o.outcome='observed'),0)::text AS total,
   count(*) FILTER(WHERE o.outcome='observed') AS observed,count(*) FILTER(WHERE o.outcome='complete_empty') AS known_empty,
   count(*) FILTER(WHERE o.outcome='observed' AND o.total_count=0) AS zero_traffic,
   min(o.interval_start) FILTER(WHERE o.outcome='observed') AS first_observation,
   max(o.interval_start+interval '1 minute') FILTER(WHERE o.outcome='observed') AS last_observation,max(o.recorded_at) AS latest_receipt
  FROM generate_subscripts(ends,1) n LEFT JOIN public.almanac_sli_observations o
   ON o.sli_id=id AND o.measurement_revision=1 AND o.interval_start>=a[n] AND o.interval_start<ends[n]
   AND o.interval_start+interval '1 minute'<=ends[n] GROUP BY n ORDER BY n
 ) r;
 RETURN jsonb_build_object('sli',id,'cutoff',cut,'baseline_ms',base_ms,'candidate_ms',proposed_ms,'baseline',base,'candidate',proposed,'reference',reference,'at',clock_timestamp());
END $fn$;`);

  const ids = fleet.slis.map(s => `'${s.id}'`).join(',');
  const goods = fleet.slis.map(s => `('${s.id}'::uuid,${s.good})`).join(',');
  psql(`CREATE UNLOGGED TABLE public.alert_experiment_proofs AS
SELECT p.*,g.good FROM (SELECT DISTINCT ON (sli_id) sli_id,measurement_revision,agent_id,session_generation,configuration_revision,lease_id
FROM public.almanac_sli_observations WHERE sli_id=ANY(ARRAY[${ids}]::uuid[]) ORDER BY sli_id,interval_start DESC) p
JOIN (VALUES ${goods}) g(sli_id,good) USING(sli_id);`);
  const cutoff = psql("SELECT to_char(date_trunc('hour',now())-interval '2 hours','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"')");
  const initial = psql(`SELECT to_char(min(interval_start),'YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM public.almanac_sli_observations WHERE sli_id=ANY(ARRAY[${ids}]::uuid[])`);
  const marker = readFileSync('../internal/storage/testdata/mark_observation_fixture.sql', 'utf8');
  const insert = (sli: string, from: string, to: string, holes = false) => `BEGIN; SET LOCAL statement_timeout='5s'; SET LOCAL lock_timeout='250ms';
INSERT INTO public.almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,p.sli_id,p.measurement_revision,'observed',p.good,10000,decode(repeat('ab',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),p.agent_id,p.session_generation,p.configuration_revision,p.lease_id,point,clock_timestamp()
FROM public.alert_experiment_proofs p CROSS JOIN generate_series(${from},${to}-interval '1 minute',interval '1 minute') point
WHERE p.sli_id='${sli}' ${holes ? 'AND extract(minute FROM point)=17' : ''} ON CONFLICT DO NOTHING;
${marker.replaceAll('$1', `'${sli}'::uuid`)} COMMIT;`;
  // Fill genuine late holes first, then flood the dirty queue with three older days.
  const backfill = fleet.slis.map(s => insert(s.id, `'${initial}'::timestamptz`, `date_trunc('minute',now())-interval '1 hour'`, true));
  for (let hour = 72; hour > 0; hour -= 12) {
    for (const sli of fleet.slis) backfill.push(insert(sli.id, `'${initial}'::timestamptz-interval '${hour} hours'`, `'${initial}'::timestamptz-interval '${hour - 12} hours'`));
  }
  const baselineStart = ingestionMetrics();
  let baselineEnd = baselineStart;
  await expect.poll(() => {
    baselineEnd = ingestionMetrics();
    const sample = ingestionDelta(baselineStart, baselineEnd);
    return sample.elapsedSeconds >= 60 && sample.accepted >= 20;
  }, { timeout: 90_000, intervals: [5_000] }).toBe(true);
  const ingestionOnly = ingestionDelta(baselineStart, baselineEnd);
  writeFileSync(`${root}/ingestion-baseline.json`, JSON.stringify({ baselineStart, baselineEnd, ingestionOnly }, null, 2));
  const before = resourceSnapshot();
  const profile = { baseRevision: '0d028cd',
    applicationImage: compose('images', '--quiet', 'app').trim(),
    sourceHashes: Object.fromEntries(['../internal/storage/testdata/alert_window_fallback.sql',
      'perf/alert-calculation.perf.ts', 'perf/alert-calculation.metrics.ts', 'perf/alert-calculation.seed.ts', 'playwright.alert-calculation.config.ts', '../compose.e2e.yaml', '../internal/storage/query/agent_access.sql', '../internal/storage/query/slis.sql', '../internal/storage/derived_current.go', '../internal/storage/query/derived_projections.sql', '../api/openapi.yaml', '../web/src/slos/SloDetailPanels.tsx']
      .map(path => [path, createHash('sha256').update(readFileSync(path)).digest('hex')])),
    cpu: cpus()[0]?.model, logicalCPUs: cpus().length, memoryBytes: totalmem(),
    database: psql("SELECT version()"), slis: 200, initialDays: 1, backfillDays: 3,
    maxDirtyHours: 8, maxRawVisits: 7200, statementTimeoutMs: 1000, workers: 2,
    offeredBatchesPerSecond: 8, rounds: 4, cutoff, apiUsers: 10, apiMinimumCyclesPerUser: 10, apiCycleIntervalMs: 10000, browserUsers: 1, browserCycles: 20, backfillBatchMaxRows: 720, backfillBatchSLIs: 1, backfillBatches: backfill.length,
    note: 'Controlled historical measurement cutoffs cross a UTC hour; wall-clock engine scheduling is outside this query experiment. Each paired query also runs an independent raw-population reference in the same snapshot; its extra work is part of the contention load.' };
  writeFileSync(`${root}/profile.json`, JSON.stringify(profile, null, 2));
  const queryWorkers = [0, 1].map(worker => {
    const statements: string[] = [];
    for (let round = 0; round < 4; round++) {
      for (let index = worker; index < 200; index += 2) {
        statements.push(`BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY; SET LOCAL statement_timeout='1s'; SET LOCAL lock_timeout='250ms'; SET LOCAL jit=off;
SELECT public.alert_experiment_compare('${fleet.slis[index]!.id}','${cutoff}'::timestamptz+interval '${58 + round} minutes',${(index + round) % 2 === 0}); COMMIT; SELECT pg_sleep(0.25);`);
      }
    }
    return asynchronousSQL(statements.join('\n'), `queries-${worker}`).then(output => {
      return output.split('\n').filter(line => line.startsWith('{')).map(line => JSON.parse(line));
    });
  });
  const backfillWork = asynchronousSQL(backfill.map((batch, index) => `${batch}\nSELECT json_build_object('completedBatch',${index},'at',clock_timestamp());`).join('\n'), 'backfill');
  const timings: Record<string, number[]> = { 'list-cold': [], 'list-warm': [], 'detail-cold': [], 'detail-warm': [] };
  const apiSamples: { name: string; beganAt: number; finishedAt: number; milliseconds: number; status: number }[] = [];
  let workloadRunning = true;
  // Ten concurrent API users exercise server admission independently of browser
  // rendering. SPEC-0019 measures browser journeys separately on a capable client;
  // ten simultaneous renderers on this two-vCPU server confounded the prior run.
  const apiWorkers = Array.from({ length: 10 }, async (_, user) => {
    const client = await apiRequest.newContext({ baseURL: process.env.E2E_BASE_URL ?? 'https://localhost:18188',
      storageState: 'e2e/.auth/admin.json', ignoreHTTPSErrors: true });
    try {
      for (let cycle = 0; cycle < 10 || workloadRunning; cycle++) {
        const cycleStarted = performance.now();
        const id = fleet.slos[(user * 10 + cycle) % fleet.slos.length]!.id;
        for (const [name, path] of [['overview', '/api/v1/slos/overview?limit=20'],
          ['status', `/api/v1/slos/${id}/status`], ['history', `/api/v1/slos/${id}/operational?period=window&burn_window_seconds=3600`]] as const) {
          const began = performance.now(), beganAt = Date.now();
          const response = await client.get(path);
          const body = await response.json();
          apiSamples.push({ name, beganAt, finishedAt: Date.now(), milliseconds: performance.now() - began, status: response.status() });
          expect(response.status(), `${name} HTTP status`).toBe(200);
          if (name === 'overview') expect(body.items).toHaveLength(20);
          else expect(body.current).toBeDefined();
          await response.dispose();
        }
        if (cycle < 9 || workloadRunning) await new Promise(resolve => setTimeout(resolve, Math.max(0, 10000 - (performance.now() - cycleStarted))));
      }
    } finally { await client.dispose(); }
  });
  // Observe failures immediately while the other lanes finish collecting evidence.
  const apiResults = Promise.allSettled(apiWorkers);
  const pageWorkers = Array.from({ length: 1 }, async () => {
    for (let run = 0; run < 20; run++) {
      const context = await browser.newContext({ storageState: 'e2e/.auth/admin.json', ignoreHTTPSErrors: true });
      try {
        await context.addInitScript(() => {
          (window as unknown as { alertJourneys: unknown[] }).alertJourneys = [];
          window.addEventListener('almanac:journey', event => (window as unknown as { alertJourneys: unknown[] }).alertJourneys.push((event as CustomEvent).detail));
        });
        const page = await context.newPage();
        await page.goto('/slos?page_size=20');
        timings['list-cold']!.push(await rendered(page, 'slo.list', 0));
        const listRows = page.locator('.app-slo-row');
        await expect(listRows).toHaveCount(20);
        for (let rowIndex = 0; rowIndex < 20; rowIndex++) {
          const row = listRows.nth(rowIndex);
          const name = await row.locator('a[href^="/slos/"]').first().innerText();
          const index = Number(name.match(/Performance SLO (\d+)/)?.[1]);
          expect(Number.isInteger(index)).toBe(true);
          const expected = `${Math.round((1 - (1 - fleet.slis[index]!.good / 10000) / 0.001) * 100)}%`;
          await expect(row.locator('.app-slo-budget b')).toHaveText(expected);
        }
        const detailLink = page.locator('.app-slo-row a[href^="/slos/"]').first();
        const detailPath = await detailLink.getAttribute('href');
        await detailLink.click();
        timings['detail-warm']!.push(await rendered(page, 'slo.detail', 0));
        const seen = await count(page, 'slo.list');
        await page.getByRole('navigation', { name: 'Primary navigation' }).getByRole('link', { name: 'SLOs', exact: true }).click();
        timings['list-warm']!.push(await rendered(page, 'slo.list', seen));
        const coldContext = await browser.newContext({ storageState: 'e2e/.auth/admin.json', ignoreHTTPSErrors: true });
        try {
          await coldContext.addInitScript(() => {
            (window as unknown as { alertJourneys: unknown[] }).alertJourneys = [];
            window.addEventListener('almanac:journey', event => (window as unknown as { alertJourneys: unknown[] }).alertJourneys.push((event as CustomEvent).detail));
          });
          const coldPage = await coldContext.newPage();
          await coldPage.goto(detailPath!);
          timings['detail-cold']!.push(await rendered(coldPage, 'slo.detail', 0));
        } finally { await coldContext.close(); }
      } finally {
        await context.close();
      }
    }
  });
  // Collect every lane's outcome before asserting; never discard failed-work evidence.
  const workOutcomes = await Promise.allSettled([...queryWorkers, backfillWork, ...pageWorkers]);
  const after = resourceSnapshot();
  const metricsAfter = ingestionMetrics();
  workloadRunning = false;
  const apiOutcomes = await apiResults;
  const settled = [...workOutcomes, ...apiOutcomes];
  const apiTimings: Record<string, number[]> = { overview: [], status: [], history: [] };
  for (const sample of apiSamples) {
    if (sample.beganAt >= Date.parse(before.at) && sample.beganAt <= Date.parse(after.at)) apiTimings[sample.name]!.push(sample.milliseconds);
  }
  const ingestionUnderLoad = ingestionDelta(baselineEnd, metricsAfter);
  // ADR-0043: observations become due after settling, deterministic dispatch and
  // one cadence's completion allowance, rounded to snapshot ticks. Counting only
  // intervals starting after the phase start incorrectly includes not-yet-due work.
  const dueEnd = (at: string, id: string) => {
    const phaseMicros = Number(createHash('sha256').update(id).digest().readBigUInt64BE() % 60_000_000n);
    const tick = Math.floor(Date.parse(at) / 60_000) * 60_000;
    return new Date(Math.floor((tick - 120_000 - phaseMicros / 1000) / 60_000) * 60_000).toISOString();
  };
  const dueRanges = fleet.slis.map(s => `('${s.id}'::uuid,'${dueEnd(before.at, s.id)}'::timestamptz,'${dueEnd(after.at, s.id)}'::timestamptz)`).join(',');
  const due = JSON.parse(psql(`WITH ranges(sli_id,from_at,to_at) AS (VALUES ${dueRanges}), expected AS (
    SELECT r.sli_id,point FROM ranges r CROSS JOIN LATERAL generate_series(r.from_at,r.to_at-interval '1 minute',interval '1 minute') point)
    SELECT json_build_object('intervals',count(*),'missing',count(*) FILTER(WHERE o.interval_start IS NULL),
    'slisWithMissing',count(DISTINCT e.sli_id) FILTER(WHERE o.interval_start IS NULL),
    'nonObserved',count(*) FILTER(WHERE o.outcome<>'observed')) FROM expected e LEFT JOIN public.almanac_sli_observations o
    ON o.sli_id=e.sli_id AND o.measurement_revision=1 AND o.interval_start=e.point AND o.recorded_at<='${after.at}'`));
  const live = JSON.parse(psql(`SELECT json_build_object('slis',count(DISTINCT sli_id),'intervals',count(*),
    'maxReceiptDelaySeconds',max(extract(epoch FROM recorded_at-interval_start-interval '1 minute')))
    FROM public.almanac_sli_observations WHERE recorded_at>='${before.at}' AND recorded_at<='${after.at}'
    AND interval_start>=date_trunc('minute','${before.at}'::timestamptz) AND sli_id=ANY(ARRAY[${ids}]::uuid[])`));
  const evidence = { profile, before, after, ingestionOnly, ingestionUnderLoad, metricsAfter, ingestedDuringLoad: ingestionBetween(before.at, after.at), due, live, timings, apiTimings, apiSamples,
    outcomes: settled.map(r => r.status === 'fulfilled' ? { status: r.status } : { status: r.status, error: String(r.reason) }) };
  writeFileSync(`${root}/results.json`, JSON.stringify(evidence, null, 2));
  const queries = [0,1].flatMap(worker => readFileSync(`${root}/queries-${worker}.jsonl`, 'utf8').split('\n')
    .filter(line => line.startsWith('{')).map(line => JSON.parse(line))) as { baseline_ms: number; candidate_ms: number; baseline: { prepared: boolean }[]; candidate: { ordinal: number; prepared: boolean; dirty_hours: number; raw_rows: number; good: string; total: string; observed: number; [key: string]: unknown }[]; reference: { good: string; total: string; [key: string]: unknown }[]; sli: string }[];
  const summary = { queryCount: queries.length, pendingBaseline: queries.filter(q => q.baseline.some(r => !r.prepared)).length,
    pendingCandidate: queries.filter(q => q.candidate.some(r => !r.prepared)).length,
    fallbackBatches: queries.filter(q => q.candidate.some(r => r.dirty_hours > 0)).length,
    maxRawRows: Math.max(...queries.flatMap(q => q.candidate.map(r => r.raw_rows))),
    baselineMs: queries.map(q => q.baseline_ms), candidateMs: queries.map(q => q.candidate_ms) };
  writeFileSync(`${root}/summary.json`, JSON.stringify(summary, null, 2));
  for (const outcome of settled) if (outcome.status === 'rejected') throw outcome.reason;
  expect(queries).toHaveLength(800);
  expect(ingestionUnderLoad.accepted).toBeGreaterThanOrEqual(20);
  expect(ingestionUnderLoad.unsuccessful).toBe(0);
  expect(ingestionUnderLoad.p95UpperSeconds).not.toBeNull();
  expect(ingestionUnderLoad.p95UpperSeconds!).toBeLessThanOrEqual(0.250);
  expect(ingestionUnderLoad.p95UpperSeconds!).toBeLessThanOrEqual(1.25 * ingestionOnly.p95LowerSeconds);
  expect(due.intervals, 'measurement must cover due work for the fleet').toBeGreaterThanOrEqual(200);
  expect(due.missing, 'every due live interval must arrive within the canonical allowance').toBe(0);
  expect(due.nonObserved).toBe(0);
  expect(summary.fallbackBatches, 'contention must actually exercise dirty interiors').toBeGreaterThan(0);
  for (const q of queries) for (const row of q.candidate) if (row.prepared) {
    const raw = q.reference[row.ordinal - 1]!;
    expect(Number(row.good)).toBe(Number(raw.good));
    expect(Number(row.total)).toBe(Number(raw.total));
    for (const key of ['observed','known_empty','zero_traffic','first_observation','last_observation','latest_receipt']) expect(row[key], key).toEqual(raw[key]);
    expect(row.raw_rows).toBeLessThanOrEqual(7200);
  }
  for (const [name, values] of Object.entries(apiTimings)) {
    expect(values.length, `${name} API samples within contention phase`).toBeGreaterThanOrEqual(100);
    const sorted = [...values].sort((a, b) => a - b);
    // Client round-trip includes server time, so this is conservative evidence
    // for the server limit. Traces distinguish server cost when it fails.
    expect(sorted[Math.ceil(sorted.length * 0.95) - 1], `${name} API p95`).toBeLessThanOrEqual(name === 'history' ? 500 : 200);
  }
  for (const [name, values] of Object.entries(timings)) {
    expect(values, name).toHaveLength(20);
    const sorted = [...values].sort((a, b) => a - b);
    expect(sorted.at(-1), `${name} maximum`).toBeLessThan(1000);
  }
});
