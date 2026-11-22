import { expect, test } from '@playwright/test';
import { api, expectOk, unique } from './support/api.ts';
import { psql, startAgent, stopAgent } from './support/stack.ts';

test('request historical collection and see the recalculated SLO', async ({ page }, info) => {
  test.setTimeout(240_000);
  const name = unique('historical-collection');
  const service = await expectOk(api(page, 'POST', '/api/v1/services', { name, display_name: 'Checkout' }));
  const registration = await expectOk(api(page, 'POST', '/api/v1/agents', { name, display_name: 'Historical collector' }));
  const agent = startAgent(registration.body.token);
  try {
    await expect.poll(async () => (await api(page, 'GET', `/api/v1/agents/${registration.body.agent.id}`)).body.runtime?.reachability, { timeout: 30_000 }).toBe('connected');
    const source = await expectOk(api(page, 'POST', '/api/v1/datasources', {
      name, display_name: 'Checkout metrics', provider: 'prometheus', configuration_format: 'prometheus-http-v1',
      endpoint: 'http://victoriametrics:8428', tls: { insecure_skip_verify: false, ca_pem: null },
      authentication: { mode: 'none', username: null, password_secret: { action: 'clear' }, token_secret: { action: 'clear' } },
      polling_interval_seconds: 60, agent_id: registration.body.agent.id,
    }));
    await page.goto(`/datasources/${source.body.id}`);
    const retention = page.getByLabel('Historical retention (days)', { exact: true });
    await retention.fill('7');
    await page.getByRole('button', { name: 'Save settings' }).click();
    await expect.poll(async () => (await api(page, 'GET', `/api/v1/datasources/${source.body.id}`)).body.historical_retention_days).toBe(7);
    const sli = await expectOk(api(page, 'POST', '/api/v1/slis', {
      name, display_name: 'Successful checkout requests', datasource_id: source.body.id, measurement_kind: 'event_counts',
      component_mode: 'interval_count', good_query: 'vector(99)', total_query: 'vector(100)',
    }));
    const id = String(sli.body.id);
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    const timeOfFirst = () => psql(`SELECT to_char(min(interval_start) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')
      FROM almanac_sli_observations WHERE sli_id='${id}' AND outcome='observed';`);
    await expect.poll(timeOfFirst, { timeout: 120_000, intervals: [1000, 3000] }).toMatch(/Z$/);
    const first = Date.parse(timeOfFirst());
    const from = new Date(first - 60_000).toISOString();
    const to = new Date(first).toISOString();
    // Establish a valid pre-existing definition in the isolated stack. The
    // missing interval is inside the delivery allowance for automatic repair.
    psql(`UPDATE public.almanac_sli_measurements SET effective_from='${from}' WHERE sli_id='${id}';`);
    const slo = await expectOk(api(page, 'POST', '/api/v1/slos', {
      name, display_name: 'Historical availability', service_id: service.body.id, sli_id: id,
      target: '0.995', rolling_window_seconds: 86400, evaluation_valid_from: from,
    }));

    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`/slis/${id}`);
    const panel = page.locator('#sli-backfills');
    await page.getByRole('radio', { name: '30d', exact: true }).click();
    await page.getByRole('button', { name: 'Review backfill', exact: true }).click();
    await expect(panel).toHaveAttribute('open', '');
    await expect(panel).toContainText('Datasource retention is 7 days.');
    await panel.getByRole('button', { name: 'Custom range' }).click();
    const dialog = page.getByRole('dialog', { name: 'Custom backfill range' });
    const monthLabel = new Intl.DateTimeFormat('en', { month: 'long', year: 'numeric', timeZone: 'UTC' });
    const dayLabel = new Intl.DateTimeFormat('en', { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric', timeZone: 'UTC' });
    async function chooseDate(label: string, value: string) {
      await dialog.getByRole('button', { name: new RegExp(label) }).click();
      const calendar = page.getByRole('dialog', { name: `Choose ${label} (UTC)` });
      const date = new Date(value);
      const month = calendar.getByRole('grid', { name: monthLabel.format(date) });
      if (!await month.isVisible()) await calendar.getByRole('button', { name: 'Previous month' }).click();
      await month.getByRole('button', { name: dayLabel.format(date) }).click();
      await expect(calendar).toBeHidden();
    }
    const startDate = dialog.getByRole('button', { name: /Start date/ });
    await startDate.click();
    await expect(page.getByRole('dialog', { name: 'Choose Start date (UTC)' })).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(startDate).toBeFocused();
    await chooseDate('Start date', from);
    await chooseDate('End date', to);
    const backfillRoute = `**/api/v1/slis/${id}/backfills`;
    let intercepted!: () => void;
    let release!: () => void;
    const requestStarted = new Promise<void>(resolve => { intercepted = resolve; });
    const unblockRoute = new Promise<void>(resolve => { release = resolve; });
    await page.route(backfillRoute, async route => {
      if (route.request().method() !== 'POST' || route.request().postDataJSON().start !== from) {
        await route.fallback();
        return;
      }
      intercepted();
      await unblockRoute;
      await route.abort().catch(() => {});
    });
    await dialog.getByLabel('Start time (UTC)').fill(from.slice(11, 19));
    await dialog.getByLabel('End time (UTC)').fill(to.slice(11, 19));
    await dialog.getByRole('button', { name: 'Preview range' }).click();
    try {
      await requestStarted;
      await page.keyboard.press('Escape');
      await expect(dialog).toHaveCount(0);
      await expect(panel.getByRole('button', { name: 'Custom range' })).toBeFocused();
    } finally {
      release();
      await page.unroute(backfillRoute);
    }
    await panel.getByRole('button', { name: 'Custom range' }).click();
    await dialog.getByRole('button', { name: 'Preview range' }).click();
    await expect(dialog.getByRole('status')).toContainText('1 chunk');
    await expect(dialog.getByRole('status')).toContainText('Intervals to recover');
    const light = info.outputPath('backfill-preview-desktop-light.png');
    await dialog.screenshot({ path: light });
    await info.attach('Backfill range preview, desktop light', { path: light, contentType: 'image/png' });
    await dialog.getByRole('button', { name: 'Queue backfill' }).click();
    const history = panel.locator('.sli-backfill-history');
    const historySummary = history.locator('summary');
    const finishedJobs = history.getByRole('list', { name: 'Backfill job history' });
    await expect(historySummary).toBeVisible({ timeout: 60_000 });
    await expect(finishedJobs).toBeHidden();
    await historySummary.click();
    await expect(finishedJobs).toContainText('1 / 1 chunks committed', { timeout: 60_000 });

    const row = psql(`SELECT good_count::int,total_count::int FROM public.almanac_sli_observations
      WHERE sli_id='${id}' AND interval_start='${from}' AND backfill_chunk_id IS NOT NULL;`);
    expect(row).toBe('99|100');
    await expect.poll(async () => {
      const current = (await api(page, 'GET', `/api/v1/slos/${slo.body.id}/status`)).body.current;
      return [Number(current?.performance?.remaining_fraction), Number(current?.performance?.total) >= 200];
    }, { timeout: 60_000 }).toEqual([-1, true]);

    await page.evaluate(() => localStorage.setItem('almanac-theme', 'dark'));
    await page.setViewportSize({ width: 360, height: 800 });
    await page.reload();
    await panel.locator(':scope > summary').click();
    await historySummary.click();
    await expect(finishedJobs).toContainText('1 / 1 chunks committed');
    const mobile = info.outputPath('backfill-completed-mobile-dark.png');
    await panel.screenshot({ path: mobile });
    await info.attach('Backfill committed, mobile dark', { path: mobile, contentType: 'image/png' });
  } finally {
    stopAgent(agent);
  }
});
