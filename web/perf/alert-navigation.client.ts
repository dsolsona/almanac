import type { Browser, APIRequestContext, Page } from '@playwright/test';
import {navigationTransport} from './alert-navigation.transport.ts';

export type NavigationStart = {
  type: 'start'; runID: string; baseURL: string;
  fleet: { slis: {id: string; good: number}[]; slos: {id: string}[] };
  storageState: Exclude<Parameters<Browser['newContext']>[0], undefined>['storageState'] & object;
};
export type Outcome = {status: 'fulfilled'} | {status: 'rejected'; code: 'lane_failed'};
export const publicOutcome = (result: PromiseSettledResult<unknown>): Outcome =>
  result.status === 'fulfilled' ? {status: 'fulfilled'} : {status: 'rejected', code: 'lane_failed'};
export const continueAPICycles = (completed: number, browserDone: boolean, stop: boolean) =>
  completed < 10 || !browserDone || !stop;
export async function runAPICycles(cycle: (index: number) => Promise<void>, state: {browserDone: boolean; stop: boolean}, signal: AbortSignal) {
  for (let index = 0; continueAPICycles(index, state.browserDone, state.stop); index++) {
    signal.throwIfAborted();
    const began = performance.now();
    await cycle(index);
    if (!continueAPICycles(index + 1, state.browserDone, state.stop)) return;
    signal.throwIfAborted();
    await new Promise<void>((resolve, reject) => {
      const cancel = () => {clearTimeout(timer); signal.removeEventListener('abort',cancel); reject(new Error('cancelled'));};
      const timer = setTimeout(() => {signal.removeEventListener('abort',cancel); resolve();},Math.max(0,10000-(performance.now()-began)));
      signal.addEventListener('abort',cancel,{once:true});
    });
  }
}
export function navigationState() {
  return {stop: false, browserDone: false, browserVersion: '', cleanupComplete:false, apiCycles: Array<number>(10).fill(0),
    timings: {'list-cold': [], 'list-warm': [], 'detail-cold': [], 'detail-warm': []} as Record<string, number[]>,
    journeySamples: [] as {name:string;run:number;milliseconds:number;clientCompletedAt:number;traceID:string|null}[],
    apiSamples: [] as {user: number; cycle: number; name: string; beganAt: number; finishedAt: number; milliseconds: number; status: number | null}[],
    outcomes: [] as Outcome[]};
}
export type NavigationState = ReturnType<typeof navigationState>;

async function rendered(page: Page, route: string, seen: number, expect: typeof import('@playwright/test').expect,
  state: NavigationState, name: string, run: number) {
  const handle = await page.waitForFunction(({ route, seen }) => {
    const events = (window as unknown as { alertJourneys: { journey: string; duration: number; result: string; traceId:string; clientCompletedAt:number }[] }).alertJourneys.filter(e => e.journey === route);
    return events.length > seen ? events.at(-1) : null;
  }, { route, seen }, { timeout: 15_000 });
  const event = await handle.jsonValue() as { duration: number; result: string; traceId: string; clientCompletedAt:number };
  expect(event.result).toBe('ready');
  expect(Number.isFinite(event.duration) && event.duration >= 0 && Number.isFinite(event.clientCompletedAt)).toBe(true);
  state.journeySamples.push({name,run,milliseconds:event.duration,clientCompletedAt:event.clientCompletedAt,
    traceID:typeof event.traceId === 'string' && /^[0-9a-f]{32}$/.test(event.traceId) ? event.traceId : null});
  return event.duration;
}

const count = (page: Page, route: string) => page.evaluate(route =>
  (window as unknown as { alertJourneys: { journey: string }[] }).alertJourneys.filter(e => e.journey === route).length, route);

// Same lane order and row oracle as alert-calculation.perf.ts. No SQL, Docker or metrics dependencies.
export async function runNavigationClient(start: NavigationStart, state: NavigationState, signal: AbortSignal,
  browserDone: (outcome: Outcome) => void): Promise<void> {
  const {chromium, request, expect: baseExpect, devices} = await import('@playwright/test');
  const expect = baseExpect.configure({timeout: 10_000});
  signal.throwIfAborted();
  const transport = navigationTransport(start.baseURL);
  const browser = await chromium.launch(transport.launch);
  state.browserVersion = browser.version();
  const clients = new Set<APIRequestContext>();
  const close = async () => { state.cleanupComplete=(await Promise.allSettled([browser.close(), ...[...clients].map(c => c.dispose())])).every(r=>r.status==='fulfilled'); };
  const abort = () => { void close(); };
  signal.addEventListener('abort', abort, {once: true});
  const contextOptions = {...devices['Desktop Chrome'], ...transport.context, storageState: start.storageState};
  const {timings, apiSamples} = state, {fleet} = start;
  try {
    signal.throwIfAborted();
    const apiWorkers = Array.from({length: 10}, async (_, user) => {
      const client = await request.newContext({...transport.api, storageState: start.storageState});
      clients.add(client);
      try {
        await runAPICycles(async cycle => {
          signal.throwIfAborted();
          const id = fleet.slos[(user * 10 + cycle) % fleet.slos.length]!.id;
          for (const [name, path] of [['overview', '/api/v1/slos/overview?limit=20'],
            ['status', `/api/v1/slos/${id}/status`], ['history', `/api/v1/slos/${id}/operational?period=window&burn_window_seconds=3600`]] as const) {
            signal.throwIfAborted();
            const began = performance.now(), beganAt = Date.now();
            let response: Awaited<ReturnType<APIRequestContext['get']>> | undefined;
            let recorded = false;
            try {
              response = await client.get(path);
              const body = await response.json() as {items?:unknown;current?:unknown};
              apiSamples.push({user, cycle, name, beganAt, finishedAt: Date.now(), milliseconds: performance.now() - began, status: response.status()});
              recorded = true;
              expect(response.status()).toBe(200);
              if (name === 'overview') expect(body.items).toHaveLength(20);
              else expect(body.current).toBeDefined();
            } finally {
              if (!recorded) apiSamples.push({user, cycle, name, beganAt, finishedAt: Date.now(), milliseconds: performance.now() - began, status: response?.status() ?? null});
              await response?.dispose();
            }
          }
          state.apiCycles[user] = cycle + 1;
        },state,signal);
      } finally { await client.dispose(); clients.delete(client); }
    });
    const apiResults = Promise.allSettled(apiWorkers);
    const pages = (async () => {
    for (let run = 0; run < 20; run++) {
      signal.throwIfAborted();
      const context = await browser.newContext(contextOptions);
      try {
        await context.addInitScript(() => {
          (window as unknown as { alertJourneys: unknown[] }).alertJourneys = [];
          window.addEventListener('almanac:journey', event => (window as unknown as { alertJourneys: unknown[] }).alertJourneys.push({...((event as CustomEvent).detail),clientCompletedAt:Date.now()}));
        });
        const page = await context.newPage();
        await page.goto('/slos?page_size=20');
        timings['list-cold']!.push(await rendered(page, 'slo.list', 0, expect,state,'list-cold',run));
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
        timings['detail-warm']!.push(await rendered(page, 'slo.detail', 0, expect,state,'detail-warm',run));
        const seen = await count(page, 'slo.list');
        await page.getByRole('navigation', { name: 'Primary navigation' }).getByRole('link', { name: 'SLOs', exact: true }).click();
        timings['list-warm']!.push(await rendered(page, 'slo.list', seen, expect,state,'list-warm',run));
        const coldContext = await browser.newContext(contextOptions);
        try {
          await coldContext.addInitScript(() => {
            (window as unknown as { alertJourneys: unknown[] }).alertJourneys = [];
            window.addEventListener('almanac:journey', event => (window as unknown as { alertJourneys: unknown[] }).alertJourneys.push({...((event as CustomEvent).detail),clientCompletedAt:Date.now()}));
          });
          const coldPage = await coldContext.newPage();
          await coldPage.goto(detailPath!);
          timings['detail-cold']!.push(await rendered(coldPage, 'slo.detail', 0, expect,state,'detail-cold',run));
        } finally { await coldContext.close(); }
      } finally {
        await context.close();
      }
    }
    })();
    const pageResult = Promise.allSettled([pages]).then(([result]) => {
      const outcome = publicOutcome(result!);
      state.browserDone = true;
      state.outcomes.push(outcome);
      browserDone(outcome);
    });
    await Promise.all([pageResult, apiResults.then(results => {state.outcomes.push(...results.map(publicOutcome));})]);
  } finally {
    signal.removeEventListener('abort', abort);
    await close();
  }
}

// All timings are preserved before evaluating limits; this is not full capacity evidence.
export function navigationFailures(state: NavigationState): string[] {
  const failures: string[] = [];
  if (!state.cleanupComplete) failures.push('cleanup_incomplete');
  if (state.outcomes.length !== 11 || state.outcomes.some(r => r.status !== 'fulfilled')) failures.push('incomplete_lanes');
  if (state.apiCycles.some(c => c < 10)) failures.push('incomplete_api_cycles');
  for (const [name, values] of Object.entries(state.timings)) {
    if (values.length !== 20) failures.push(`${name}_count`);
    if (values.some(v => !Number.isFinite(v) || v < 0 || v >= 1000)) failures.push(`${name}_latency`);
  }
  for (const name of ['overview', 'status', 'history']) {
    const values = state.apiSamples.filter(s => s.name === name);
    if (values.length < 100) failures.push(`${name}_count`);
    if (values.some(s => s.status !== 200)) failures.push(`${name}_status`);
    const sorted = values.map(s => s.milliseconds).sort((a,b) => a-b);
    if (sorted.length && sorted[Math.ceil(sorted.length * .95)-1]! > (name === 'history' ? 500 : 200)) failures.push(`${name}_latency`);
  }
  return failures;
}
