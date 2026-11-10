import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { expect, test, type Browser, type Page } from '@playwright/test';
import { ingestionBetween, pauseAgent, relationSizes, resourceDelta, resourceSnapshot, writeResources } from './resources.ts';

// Page speed of the four main pages (SPEC-0019, ADR-0025, ADR-0033) on the seeded fleet. The time is the
// UI's own journey measurement: from navigation start until the page's content (and, on SLO detail,
// its charts) has painted, reported by the `almanac:journey` browser event. Budget: < 1,000 ms at p95.
const BUDGET_MS = 1_000;
const RUNS = Number(process.env.PERF_RUNS ?? 10);
type Fleet = { days: number; size: number; slis: { id: string }[]; slos: { id: string; expectedBudget: string }[]; agentContainer: string };
// Written by perf/seed.setup.ts, which runs first; read when the tests run, not when they are collected.
let fleet: Fleet;
test.beforeAll(() => { fleet = JSON.parse(readFileSync('perf/.fleet.json', 'utf8')) as Fleet; });
const results: Record<string, { cold: number[]; warm: number[] }> = {};
const record = (route: string, kind: 'cold' | 'warm', ms: number) => { (results[route] ??= { cold: [], warm: [] })[kind].push(Math.round(ms)); };

async function instrument(page: Page) {
  await page.addInitScript(() => {
    (window as unknown as { journeys: unknown[] }).journeys = [];
    window.addEventListener('almanac:journey', (event) => (window as unknown as { journeys: unknown[] }).journeys.push((event as CustomEvent).detail));
  });
}

// Waits for the next completed journey of a route and returns its duration; any result but `ready` fails.
async function journey(page: Page, route: string, seen: number): Promise<number> {
  const handle = await page.waitForFunction(({ route, seen }) => {
    const done = (window as unknown as { journeys: { journey: string; result: string; duration: number }[] }).journeys.filter((item) => item.journey === route);
    return done.length > seen ? done[done.length - 1] : null;
  }, { route, seen }, { timeout: 15_000 });
  const detail = await handle.jsonValue() as { result: string; duration: number };
  expect(detail.result, `${route} journey result`).toBe('ready');
  return detail.duration;
}

const count = (page: Page, route: string) => page.evaluate((route) => (window as unknown as { journeys: { journey: string }[] }).journeys.filter((item) => item.journey === route).length, route);

async function coldLoad(browser: Browser, path: string, route: string) {
  const context = await browser.newContext({ storageState: 'e2e/.auth/admin.json', ignoreHTTPSErrors: true });
  const page = await context.newPage();
  await instrument(page);
  await page.goto(path);
  record(route, 'cold', await journey(page, route, 0));
  await context.close();
}

const pages = () => [
  { route: 'slo.list', path: '/slos' },
  { route: 'slo.detail', path: `/slos/${fleet.slos[0]!.id}` },
  { route: 'sli.list', path: '/slis' },
  { route: 'sli.detail', path: `/slis/${fleet.slis[0]!.id}` },
];

test('the SLO list shows the exact seeded budgets', async ({ page }) => {
  await page.goto('/slos?page_size=20');
  for (const [index, slo] of fleet.slos.entries()) {
    const row = page.locator('.app-slo-row').filter({ hasText: `Performance SLO ${String(index).padStart(2, '0')}` });
    await expect(row.locator('.app-slo-budget b'), `budget of SLO ${index}`).toHaveText(slo.expectedBudget);
  }
});

test('cold loads of each page', async ({ browser }) => {
  test.setTimeout(10 * 60_000);
  for (let run = 0; run < RUNS; run += 1) {
    for (const { route, path } of pages()) await coldLoad(browser, path, route);
  }
});

test('in-app navigation between the pages', async ({ page }) => {
  test.setTimeout(10 * 60_000);
  await instrument(page);
  await page.goto('/slos');
  await journey(page, 'slo.list', 0);
  for (let run = 0; run < RUNS; run += 1) {
    let seen = await count(page, 'slo.detail');
    await page.getByRole('link', { name: 'Performance SLO 00', exact: true }).click();
    record('slo.detail', 'warm', await journey(page, 'slo.detail', seen));
    seen = await count(page, 'sli.list');
    await page.getByRole('navigation', { name: 'Primary navigation' }).getByRole('link', { name: 'SLIs' }).click();
    record('sli.list', 'warm', await journey(page, 'sli.list', seen));
    seen = await count(page, 'sli.detail');
    await page.getByRole('link', { name: 'Performance SLI 00', exact: true }).click();
    record('sli.detail', 'warm', await journey(page, 'sli.detail', seen));
    seen = await count(page, 'slo.list');
    await page.getByRole('navigation', { name: 'Primary navigation' }).getByRole('link', { name: 'SLOs' }).click();
    record('slo.list', 'warm', await journey(page, 'slo.list', seen));
  }
});

test('record storage, WAL and statement-counter availability', async () => {
  test.setTimeout(3 * 60_000);
  const evidence = JSON.parse(readFileSync('../.local/perf/resources.json', 'utf8')) as { start: ReturnType<typeof resourceSnapshot> };
  const end = resourceSnapshot();
  const sizes = relationSizes();
  expect(sizes.filter((row) => row.kind === 'canonical').length).toBe(1);
  expect(sizes.every((row) => Number.isSafeInteger(row.total_bytes) && row.total_bytes >= 0 && row.index_bytes >= 0)).toBe(true);
  const windows = [];
  let paused = false;
  try {
    // Measure ordinary ingestion before pausing, avoiding a reconnect/catch-up burst in its rate.
    for (const phase of ['ingesting', 'idle'] as const) {
      if (phase === 'idle') {
        pauseAgent(fleet.agentContainer, true);
        paused = true;
      }
      const start = resourceSnapshot();
      // A fixed interval is the measurement itself, not a readiness wait or a retry.
      await new Promise((resolve) => setTimeout(resolve, 60_000));
      const end = resourceSnapshot();
      const delta = resourceDelta(start, end);
      const acceptedRows = ingestionBetween(start.at, end.at);
      const window = { phase, ...delta, observations_accepted: acceptedRows,
        wal_bytes_per_accepted_row: acceptedRows > 0 ? delta.wal_bytes / acceptedRows : null,
        statements_per_accepted_row: acceptedRows > 0 && delta.statements !== null ? delta.statements / acceptedRows : null,
        statements_per_minute_per_slo: delta.statements_per_minute === null ? null : delta.statements_per_minute / fleet.size };
      windows.push(window);
      expect(window.seconds).toBeGreaterThanOrEqual(60);
      if (window.statements !== null) expect(window.statements).toBeGreaterThan(0);
      else expect(start.statement_counter_unavailable_reason ?? end.statement_counter_unavailable_reason).toBeTruthy();
      if (phase === 'idle') expect(window.observations_accepted).toBe(0);
      else expect(window.observations_accepted).toBeGreaterThan(0);
    }
  } finally {
    try {
      writeResources({ ...evidence, seed_and_page_timings: resourceDelta(evidence.start, end), sizes, windows,
        seed_wal_scope: 'setup, synthetic bulk history, preparation and page timings; not comparable to steady ingestion',
        window_scope: 'idle means no agent ingestion; background work may still be catching up',
        accepted_row_scope: 'rows recorded_at in [start,end); WAL covers fixture PostgreSQL, statements cover its app DB, including background work; zero-row ratios are null',
        runtime_metrics: 'unavailable: metrics listener is disabled in the normal Compose stack',
        statement_scope: 'all fixture-database statements, including sampler calls; null when counters are unavailable' });
    } finally {
      if (paused) pauseAgent(fleet.agentContainer, false);
    }
  }
});

test.afterAll(() => {
  const percentile = (values: number[], p: number) => [...values].sort((a, b) => a - b)[Math.min(values.length - 1, Math.ceil((p / 100) * values.length) - 1)] ?? NaN;
  const summary = Object.entries(results).flatMap(([route, kinds]) => (['cold', 'warm'] as const).filter((kind) => kinds[kind].length).map((kind) => ({
    page: route, load: kind, runs: kinds[kind].length, median_ms: percentile(kinds[kind], 50), p95_ms: percentile(kinds[kind], 95), max_ms: Math.max(...kinds[kind]),
  })));
  mkdirSync('../.local/perf', { recursive: true });
  writeFileSync('../.local/perf/results.json', JSON.stringify({ budget_ms: BUDGET_MS, fleet: { size: fleet?.size, days: fleet?.days }, summary, samples: results }, null, 2));
  console.table(summary);
  const over = summary.filter((row) => row.p95_ms >= BUDGET_MS);
  if (over.length) throw new Error(`Over the ${BUDGET_MS} ms budget at p95: ${over.map((row) => `${row.page} ${row.load} ${row.p95_ms} ms`).join(', ')}`);
});
