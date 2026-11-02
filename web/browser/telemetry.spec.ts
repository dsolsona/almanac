import { expect, test } from '@playwright/test';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { randomUUID } from 'node:crypto';
import { resolve } from 'node:path';

test('completed roots survive navigation away and back', async ({ page }) => {
  const delivered: { name: string; traceId: string }[] = [];
  const shapes: unknown[] = [];
  const completed: { traceId: string; recording: boolean; result: string }[] = [];
  const diagnostics: unknown[] = [];
  await page.exposeFunction('recordJourney', (value: typeof completed[number]) => completed.push(value));
  await page.exposeFunction('recordDelivery', (value: unknown) => diagnostics.push(value));
  await page.addInitScript(() => {
    addEventListener('almanac:journey', (event) => void (window as unknown as { recordJourney(value: unknown): void }).recordJourney((event as CustomEvent).detail));
    addEventListener('almanac:telemetry-delivery', (event) => void (window as unknown as { recordDelivery(value: unknown): void }).recordDelivery((event as CustomEvent).detail));
  });
  await page.route('**/api/v1/telemetry/traces', async route => {
    const body = route.request().postDataJSON();
    shapes.push(body);
    for (const resource of body.resourceSpans) for (const scope of resource.scopeSpans) for (const span of scope.spans) delivered.push({ name: span.name, traceId: span.traceId });
    await route.fulfill({ status: 202, contentType: 'application/json', body: '{}' });
  });
  await page.route('**/api/v1/slis', route => route.fulfill({ status: 200, contentType: 'application/json', headers: { 'X-Almanac-Tracing': '1' }, body: '{}' }));
  await page.goto('/design-system');
  const complete = async () => {
    const before=completed.filter(value=>value.result==='ready').length;
    await page.evaluate(async () => {
    // Vite serves the same module instance already installed by main.tsx.
    const modulePath = '/src/telemetry/client.ts';
    const client = await import(/* @vite-ignore */ modulePath);
    client.telemetrySession({ csrf_token: 'fixture-only', session: {} });
    history.pushState({}, '', '/slis');
    const originalNow = Date.now;
    const before = originalNow();
    Date.now = () => before + 31 * 86400 * 1000;
    try {
      const response = await fetch('/api/v1/slis');
      client.traceJSON(response, await response.text());
      client.traceChartRender(() => undefined);
    } finally { Date.now = originalNow; }
    client.journeyContentReady('sli.list');
    });
    await expect.poll(()=>completed.filter(value=>value.result==='ready').length).toBe(before+1);
    await page.evaluate(async()=>{const modulePath='/src/telemetry/client.ts';await (await import(/* @vite-ignore */ modulePath)).flushTelemetry();});
  };
  await complete();
  await page.evaluate(() => history.replaceState({}, '', '/design-system'));
  await page.goto('/design-system?second-document=1');
  await page.goBack();
  await complete();
  await page.evaluate(() => history.replaceState({}, '', '/design-system'));
  await page.goto('/design-system?third-document=1');
  await page.goBack();
  await complete();
  const roots = completed.filter(value => value.result === 'ready');
  const directory = resolve('../.local/browser-telemetry');
  await mkdir(directory, { recursive: true });
  await writeFile(resolve(directory, 'sdk-shapes.json'), JSON.stringify(shapes), { mode: 0o600 });
  for (const body of shapes as { resourceSpans: { scopeSpans: { spans: { name: string; startTimeUnixNano: string; endTimeUnixNano: string }[] }[] }[] }[]) {
    for (const resource of body.resourceSpans) for (const scope of resource.scopeSpans) for (const span of scope.spans) {
      expect(Math.abs(Number(span.startTimeUnixNano) / 1e6 - Date.now()), `${span.name} start follows the document clock`).toBeLessThan(60_000);
      expect(Math.abs(Number(span.endTimeUnixNano) / 1e6 - Date.now()), `${span.name} end follows the document clock`).toBeLessThan(60_000);
    }
  }
  await test.info().attach('telemetry-lifecycle.json', { body: JSON.stringify({ completed, delivered, diagnostics, shapes }), contentType: 'application/json' });
  expect(roots.length).toBeGreaterThanOrEqual(3);
  for (const root of roots) {
    expect(root.recording).toBe(true);
    expect(delivered.some(span => span.name === 'ui.sli.list' && span.traceId === root.traceId)).toBe(true);
  }
});

for (const interrupted of [false, true]) test(`production UI roots survive route changes and document restoration${interrupted ? ' after interrupted export' : ''}`, async ({ page }) => {
  const delivered: { name: string; traceId: string }[] = [];
  const completed: { traceId: string; journey: string; recording: boolean; result: string }[] = [];
  const diagnostics: unknown[] = [];
  const errors: string[] = [];
  let outage = interrupted;
  let interruptedAttempts = 0;
  const html = (await readFile('dist/index.html', 'utf8')).replaceAll('"/assets/', '"/dist/assets/');
  await page.exposeFunction('recordJourney', (value: typeof completed[number]) => completed.push(value));
  await page.exposeFunction('recordDelivery', (value: unknown) => diagnostics.push(value));
  page.on('pageerror', error => errors.push(error.name));
  await page.addInitScript(() => {
    addEventListener('almanac:journey', event => void (window as unknown as { recordJourney(value: unknown): void }).recordJourney((event as CustomEvent).detail));
    addEventListener('almanac:telemetry-delivery', event => void (window as unknown as { recordDelivery(value: unknown): void }).recordDelivery((event as CustomEvent).detail));
  });
  await page.route('**/*', async route => {
    const request = route.request();
    if (request.isNavigationRequest()) { await route.fulfill({ contentType: 'text/html', body: html }); return; }
    const path = new URL(request.url()).pathname;
    if (path === '/api/v1/telemetry/traces') {
      if (outage) { interruptedAttempts++; await route.abort('failed'); return; }
      const body = request.postDataJSON();
      for (const resource of body.resourceSpans) for (const scope of resource.scopeSpans) for (const span of scope.spans) delivered.push({ name: span.name, traceId: span.traceId });
      await route.fulfill({ status: 202, contentType: 'application/json', body: '{}' }); return;
    }
    if (path.startsWith('/api/')) {
      const body = path === '/api/v1/auth/context' ? { csrf_token: 'fixture-only', session: { user_id: '00000000-0000-4000-8000-000000000001', login: 'fixture-owner', role: 'administrator', restricted: false } } : { items: [], next_cursor: '' };
      await route.fulfill({ contentType: 'application/json', headers: { 'X-Almanac-Tracing': '1' }, body: JSON.stringify(body) }); return;
    }
    await route.continue();
  });
  try {
    await page.goto('/slis');
    await expect.poll(() => completed.filter(value => value.journey === 'sli.list' && value.result === 'ready').length).toBe(1);
    if (interrupted) {
      await expect.poll(() => interruptedAttempts).toBeGreaterThan(0);
      outage = false;
      await page.reload();
      await expect.poll(() => completed.filter(value => value.journey === 'sli.list' && value.result === 'ready').length).toBe(2);
    }
    if (await page.getByRole('button', { name: 'Menu', exact: true }).isVisible()) await page.getByRole('button', { name: 'Menu', exact: true }).click();
    await page.getByRole('link', { name: 'Agents', exact: true }).click();
    await expect.poll(() => completed.filter(value => value.journey === 'management' && value.result === 'ready').length).toBe(1);
    if (await page.getByRole('button', { name: 'Menu', exact: true }).isVisible()) await page.getByRole('button', { name: 'Menu', exact: true }).click();
    await page.getByRole('link', { name: 'Datasources', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'No datasources yet' })).toBeVisible();
    await expect.poll(() => completed.filter(value => value.journey === 'management' && value.result === 'ready').length).toBe(2);
    if (await page.getByRole('button', { name: 'Menu', exact: true }).isVisible()) await page.getByRole('button', { name: 'Menu', exact: true }).click();
    await page.getByRole('link', { name: 'SLIs', exact: true }).click();
    await expect.poll(() => completed.filter(value => value.journey === 'sli.list' && value.result === 'ready').length).toBe(interrupted ? 3 : 2);
    await page.goto('/agents');
    await page.goBack();
    await expect.poll(() => completed.filter(value => value.journey === 'sli.list' && value.result === 'ready').length).toBeGreaterThanOrEqual(interrupted ? 4 : 3);
    await page.evaluate(() => new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error('flush timed out')), 2500);
      addEventListener('almanac:telemetry-flushed', () => { clearTimeout(timeout); resolve(); }, { once: true });
      dispatchEvent(new Event('almanac:telemetry-flush'));
    }));
    for (const root of completed.filter(value => ['sli.list', 'management'].includes(value.journey) && value.result === 'ready')) {
      expect(root.recording).toBe(true);
      expect(delivered.some(span => span.name === 'ui.' + root.journey && span.traceId === root.traceId)).toBe(true);
    }
  } finally {
    const directory = resolve('../.local/browser-telemetry');
    await mkdir(directory, { recursive: true });
    await writeFile(resolve(directory, `production-${randomUUID()}.json`), JSON.stringify({ completed, delivered, diagnostics, errors }), { mode: 0o600, flag: 'wx' });
  }
});
