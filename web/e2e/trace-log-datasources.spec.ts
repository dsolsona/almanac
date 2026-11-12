import { expect, test } from '@playwright/test';
import { randomBytes } from 'node:crypto';
import { mkdir } from 'node:fs/promises';
import { api, expectOk, unique } from './support/api.ts';
import { startAgent, stopAgent } from './support/stack.ts';

const sources = [
  { id: 'victoriatraces', label: 'VictoriaTraces', language: 'LogsQL', endpoint: 'http://victoriatraces:10428' },
  { id: 'tempo', label: 'Tempo', language: 'TraceQL', endpoint: 'http://tempo:3200' },
  { id: 'loki', label: 'Loki', language: 'LogQL', endpoint: 'http://loki:3100' },
];

for (const source of sources) {
  test(`${source.label} datasource collects native counts and feeds an SLO`, async ({ page }) => {
    test.setTimeout(240_000);
    const journey = unique(source.id);
    const registered = await expectOk(api(page, 'POST', '/api/v1/agents', { name: journey, display_name: 'Trace and log collector' }));
    const agentID = registered.body.agent.id;
    const container = startAgent(registered.body.token);
    try {
      await expect.poll(async () => (await expectOk(api(page, 'GET', `/api/v1/agents/${agentID}`))).body.runtime?.reachability, { timeout: 30_000 }).toBe('connected');
      const base = Math.floor(Date.now() / 15_000) * 15_000;
      const times: { time: bigint; good: boolean }[] = [];
      // Nine good events and one bad per complete interval, on both boundaries.
      // Include a settled past and future intervals so collection timing cannot change the oracle.
      for (let offset = -240_000; offset <= 180_000; offset += 15_000) {
        for (let i = 0; i < 10; i++) times.push({ time: BigInt(base + offset + i * 1000) * 1_000_000n, good: i < 9 });
      }
      if (source.id === 'loki') {
        const response = await fetch('http://localhost:13101/loki/api/v1/push', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ streams: [{ stream: { journey }, values: times.map(({ time, good }) => [String(time), good ? 'good' : 'bad']) }] }) });
        expect(response.status).toBe(204);
      } else {
        const spans = times.map(({ time, good }) => ({ traceId: randomBytes(16).toString('hex'), spanId: randomBytes(8).toString('hex'), name: `${journey}-${good ? 'good' : 'bad'}`, kind: 2, startTimeUnixNano: String(time), endTimeUnixNano: String(time + 1_000_000n), status: { code: good ? 1 : 2 } }));
        const target = source.id === 'tempo' ? 'http://localhost:14319/v1/traces' : `${process.env.E2E_TRACES_URL ?? 'http://localhost:19528'}/insert/opentelemetry/v1/traces`;
        const response = await fetch(target, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ resourceSpans: [{ resource: { attributes: [{ key: 'service.name', value: { stringValue: journey } }] }, scopeSpans: [{ scope: { name: 'almanac-e2e' }, spans }] }] }) });
        expect(response.ok).toBe(true);
      }
      const good = source.id === 'loki' ? `sum(count_over_time({journey="${journey}"} |= "good" [$interval]))` : source.id === 'tempo' ? `{ name = "${journey}-good" } | count_over_time()` : `name:="${journey}-good" | stats count() as events`;
      const total = source.id === 'loki' ? `sum(count_over_time({journey="${journey}"}[$interval]))` : source.id === 'tempo' ? `{ name =~ "${journey}-.*" } | count_over_time()` : `(name:="${journey}-good" or name:="${journey}-bad") | stats count() as events`;
      // Wait for native source indexing before asking the product to collect.
      const end = new Date(base - 60_000);
      const start = new Date(end.getTime() - 15_000);
      await expect.poll(async () => {
        const params = new URLSearchParams();
        let endpoint = '';
        if (source.id === 'tempo') {
          endpoint = 'http://localhost:13201/api/metrics/query';
          params.set('q', total); params.set('start', start.toISOString()); params.set('end', end.toISOString());
        } else if (source.id === 'victoriatraces') {
          endpoint = `${process.env.E2E_TRACES_URL ?? 'http://localhost:19528'}/select/logsql/stats_query`;
          params.set('query', total); params.set('start', start.toISOString()); params.set('end', end.toISOString()); params.set('time', end.toISOString());
        } else {
          endpoint = 'http://localhost:13101/loki/api/v1/query';
          params.set('query', total.replaceAll('$interval', '15s')); params.set('time', String(BigInt(end.getTime()) * 1_000_000n - 1n));
        }
        const response = await fetch(`${endpoint}?${params}`);
        if (!response.ok) return -1;
        const body = await response.json();
        return source.id === 'tempo' ? Number(body.series?.[0]?.value ?? 0) : Number(body.data?.result?.[0]?.value?.[1] ?? -1);
      }, { timeout: 30_000, intervals: [500, 1000] }).toBe(10);
      const navigationStarted = performance.now();
      await page.goto('/datasources/new');
      await expect(page.getByRole('radio', { name: source.label, exact: true })).toBeVisible();
      expect(performance.now() - navigationStarted).toBeLessThan(1000);
      const providerRadio = page.getByRole('radio', { name: source.label, exact: true });
      await providerRadio.check();
      await providerRadio.focus();
      await page.keyboard.press('ArrowRight');
      await page.keyboard.press('ArrowLeft');
      await expect(providerRadio).toBeChecked();
      await page.getByLabel('Display name', { exact: true }).fill(`${source.label} request events`);
      await page.getByLabel(`${source.label} endpoint`, { exact: true }).fill('invalid-endpoint');
      await page.getByRole('button', { name: 'Create datasource', exact: true }).click();
      await expect(page.getByRole('alert')).toContainText('HTTP or HTTPS URL');
      await page.getByLabel(`${source.label} endpoint`, { exact: true }).fill(source.endpoint);
      if (await page.getByRole('button', { name: 'Change Assigned agent' }).count()) await page.getByRole('button', { name: 'Change Assigned agent' }).click();
      // The generic Change control belongs to the selected summary.
      const agentInput = page.getByLabel('Assigned agent', { exact: true });
      if (!await agentInput.count()) await page.getByRole('region', { name: 'Agent', exact: true }).getByRole('button', { name: 'Change' }).click();
      await agentInput.fill('Trace and log');
      await page.locator(`#datasource-agent-option-${agentID}`).click();
      await page.getByRole('radio', { name: '15 s', exact: true }).click();
      await page.getByRole('button', { name: 'Test connection', exact: true }).click();
      await expect(page.locator('.diagnostic-result').last()).toContainText('Succeeded', { timeout: 30_000 });
      await expect(page.getByRole('alert')).toHaveCount(0);
      const renders = '../.local/e2e/datasource-renders';
      await mkdir(renders, { recursive: true });
      for (const [theme, width] of [['light', 1440], ['dark', 1440], ['dark', 360]] as const) {
        await page.setViewportSize({ width, height: 1100 });
        await page.evaluate((theme) => { document.documentElement.classList.toggle('dark', theme === 'dark'); }, theme);
        await expect(page.getByLabel(`${source.label} endpoint`)).toBeVisible();
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
        await page.evaluate(() => window.scrollTo(0, 0));
        await page.screenshot({ path: `${renders}/${source.id}-${theme}-${width}.png`, fullPage: true });
      }
      await page.setViewportSize({ width: 1440, height: 1100 });
      await page.getByRole('button', { name: 'Create datasource', exact: true }).click();
      await expect(page.getByRole('heading', { name: `${source.label} request events`, exact: true })).toBeVisible();
      const datasourceID = new URL(page.url()).pathname.split('/').pop()!;
      await expect(page.getByRole('button', { name: 'Run preview', exact: true })).toBeEnabled({ timeout: 30_000 });
      await page.getByLabel(`${source.language} query`, { exact: true }).fill(total);
      await page.getByRole('button', { name: 'Run preview', exact: true }).click();
      await expect(page.locator('.diagnostic-result').last()).toContainText('Succeeded', { timeout: 30_000 });
      await expect(page.locator('.diagnostic-result').last()).toContainText('10');
      await page.goto(`/slis/new?datasource=${datasourceID}`);
      const sliName = `${source.label} success ${journey}`;
      await page.getByLabel('Display name', { exact: true }).fill(sliName);
      await expect(page.locator('.query-editor__language').first()).toHaveText(source.language);
      await page.getByLabel('Good events', { exact: true }).fill(good);
      await page.getByLabel('Total events', { exact: true }).fill(total);
      await page.getByRole('button', { name: 'Create SLI', exact: true }).click();
      await expect(page.getByRole('heading', { name: sliName, exact: true })).toBeVisible();
      const sliID = new URL(page.url()).pathname.split('/').pop()!;
      await expect(async () => { await page.reload(); await expect(page.locator('.sli-measurement-summary > div:first-child p')).toContainText('9 / 10 good', { timeout: 2000 }); }).toPass({ timeout: 90_000, intervals: [2000] });
      // The real source observations flow through existing occurrence math.
      const service = await expectOk(api(page, 'POST', '/api/v1/services', { name: journey, display_name: journey, description: '', labels: {} }));
      const history = await expectOk(api(page, 'GET', `/api/v1/slis/${sliID}/observations?limit=20`));
      const validFrom = history.body.items.find((item: { outcome: string }) => item.outcome === 'observed').interval_start;
      const slo = await expectOk(api(page, 'POST', '/api/v1/slos', { name: journey, display_name: journey, sli_id: sliID, service_id: service.body.id, target: '0.95', rolling_window_seconds: 604800, evaluation_valid_from: validFrom }));
      await page.goto(`/slos/${slo.body.id}`);
      await expect(page.getByRole('heading', { name: journey, exact: true })).toBeVisible();
      await expect(async () => { await page.reload(); await expect(page.locator('.app-budget-hero')).toContainText('-100%', { timeout: 2000 }); }).toPass({ timeout: 60_000, intervals: [2000] });
      const status = await expectOk(api(page, 'GET', `/api/v1/slos/${slo.body.id}/status`));
      expect(status.body.current.performance.attainment).toBe('0.900000000000');
      expect(status.body.current.performance.burn).toBe('2.000000000000');
    } finally { stopAgent(container); }
  });
}
