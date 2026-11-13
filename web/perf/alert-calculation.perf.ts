import { execFile } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { cpus, totalmem } from 'node:os';
import { expect, test, type Page } from '@playwright/test';
import { psql } from '../e2e/support/stack.ts';
import { resourceSnapshot, ingestionBetween } from './resources.ts';

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
  const marking = fleet.slis.map(s => marker.replaceAll('$1', `'${s.id}'::uuid`)).join('\n');
  const insert = (from: string, to: string) => `BEGIN; SET LOCAL statement_timeout='5s';
INSERT INTO public.almanac_sli_observations(interval_start,sli_id,measurement_revision,outcome,good_count,total_count,result_digest,missing_components,execution_id,delivery_id,agent_id,session_generation,configuration_revision,lease_id,agent_created_at,recorded_at)
SELECT point,p.sli_id,p.measurement_revision,'observed',p.good,10000,decode(repeat('ab',32),'hex'),'{}',gen_random_uuid(),gen_random_uuid(),p.agent_id,p.session_generation,p.configuration_revision,p.lease_id,point,clock_timestamp()
FROM public.alert_experiment_proofs p CROSS JOIN generate_series(${from},${to}-interval '1 minute',interval '1 minute') point ON CONFLICT DO NOTHING;
${marking} COMMIT;`;
  // Fill genuine late holes first, then flood the dirty queue with three older days.
  const backfill = [insert(`'${initial}'::timestamptz`, `date_trunc('minute',now())-interval '1 hour'`)];
  for (let hour = 72; hour > 0; hour--) {
    backfill.push(insert(`'${initial}'::timestamptz-interval '${hour} hours'`, `'${initial}'::timestamptz-interval '${hour - 1} hours'`));
  }
  const before = resourceSnapshot();
  const profile = { baseRevision: 'c276c553fab3a3854a84e42196aa98dcaa7e6d9e',
    sourceHashes: Object.fromEntries(['../internal/storage/testdata/alert_window_fallback.sql',
      'perf/alert-calculation.perf.ts', 'perf/alert-calculation.seed.ts', 'playwright.alert-calculation.config.ts']
      .map(path => [path, createHash('sha256').update(readFileSync(path)).digest('hex')])),
    cpu: cpus()[0]?.model, logicalCPUs: cpus().length, memoryBytes: totalmem(),
    database: psql("SELECT version()"), slis: 200, initialDays: 1, backfillDays: 3,
    maxDirtyHours: 8, maxRawVisits: 7200, statementTimeoutMs: 1000, workers: 2,
    offeredBatchesPerSecond: 8, rounds: 4, cutoff, browserUsers: 10,
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
  const pageWorkers = Array.from({ length: 10 }, async () => {
    for (let run = 0; run < 10; run++) {
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
      } finally { await context.close(); }
    }
  });
  // Collect every lane's outcome before asserting; never discard failed-work evidence.
  const settled = await Promise.allSettled([...queryWorkers, backfillWork, ...pageWorkers]);
  const after = resourceSnapshot();
  const live = JSON.parse(psql(`SELECT json_build_object('slis',count(DISTINCT sli_id),'intervals',count(*),
    'maxReceiptDelaySeconds',max(extract(epoch FROM recorded_at-interval_start-interval '1 minute')))
    FROM public.almanac_sli_observations WHERE recorded_at>='${before.at}' AND recorded_at<='${after.at}'
    AND interval_start>=date_trunc('minute','${before.at}'::timestamptz) AND sli_id=ANY(ARRAY[${ids}]::uuid[])`));
  const evidence = { profile, before, after, ingestedDuringLoad: ingestionBetween(before.at, after.at), live, timings,
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
  expect(live.slis, 'real live collection must continue for the whole fleet').toBe(200);
  expect(summary.fallbackBatches, 'contention must actually exercise dirty interiors').toBeGreaterThan(0);
  for (const q of queries) for (const row of q.candidate) if (row.prepared) {
    const raw = q.reference[row.ordinal - 1]!;
    expect(Number(row.good)).toBe(Number(raw.good));
    expect(Number(row.total)).toBe(Number(raw.total));
    for (const key of ['observed','known_empty','zero_traffic','first_observation','last_observation','latest_receipt']) expect(row[key], key).toEqual(raw[key]);
    expect(row.raw_rows).toBeLessThanOrEqual(7200);
  }
  for (const [name, values] of Object.entries(timings)) {
    expect(values, name).toHaveLength(100);
    const sorted = [...values].sort((a, b) => a - b);
    expect(sorted.at(-1), `${name} maximum`).toBeLessThan(1000);
  }
});
