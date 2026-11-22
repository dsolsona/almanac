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
    const panel = page.locator('details').filter({ has: page.locator('summary', { hasText: 'Historical backfill' }) });
    await page.getByRole('radio', { name: '30d', exact: true }).click();
    await page.getByRole('button', { name: 'Review backfill', exact: true }).click();
    await expect(panel).toHaveAttribute('open', '');
    await expect(panel).toContainText('Datasource retention is 7 days.');
    await page.getByRole('radio', { name: '1h', exact: true }).click();
    await page.getByRole('button', { name: 'Exact values ↓' }).click();
    const recoveredRow = page.getByRole('region', { name: 'Observation history' }).getByRole('row')
      .filter({ has: page.locator(`td[title^="${from.slice(0, 19)}"]`) });
    await expect(recoveredRow).toContainText('Missing');
    await panel.getByRole('button', { name: 'Custom range' }).click();
    const dialog = page.getByRole('dialog', { name: 'Custom backfill range' });
    async function setBound(label: 'Start' | 'End', value: string, capture = false) {
      const button = dialog.getByRole('button', { name: new RegExp(`^${label} date`) });
      await button.click();
      const picker = page.getByRole('dialog', { name: `Choose ${label} date (UTC)`, exact: true });
      const selected = new Date(value);
      const month = new Intl.DateTimeFormat('en', { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(selected);
      const grid = picker.getByRole('grid');
      const currentMonth = await grid.getAttribute('aria-label');
      if (currentMonth !== month) {
        const direction = Date.parse(`1 ${currentMonth} UTC`) > selected.getTime() ? 'Previous' : 'Next';
        await picker.getByRole('button', { name: `${direction} month` }).click();
      }
      await expect(grid).toHaveAccessibleName(month);
      if (capture) {
        const path = info.outputPath('earlier-history-calendar-light.png');
        await picker.screenshot({ path });
        await info.attach('Shared calendar, desktop light', { path, contentType: 'image/png' });
      }
      const day = new Intl.DateTimeFormat('en', { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric', timeZone: 'UTC' }).format(selected);
      await grid.getByRole('button', { name: new RegExp(`^${day}(,|$)`) }).click();
      await expect(button).toBeFocused();
      await dialog.getByLabel(`${label} time (UTC)`).fill(value.slice(11, 16));
    }
    const startDate = dialog.getByRole('button', {name: /^Start date/});
    await startDate.click();
    const startCalendar = page.getByRole('dialog', {name: 'Choose Start date (UTC)'});
    await expect(startCalendar).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(startCalendar).toBeHidden();
    await expect(startDate).toBeFocused();
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
    await setBound('Start', from);
    await setBound('End', to);
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
    await setBound('Start', from);
    await setBound('End', to);
    await dialog.getByRole('button', { name: 'Preview range' }).click();
    await expect(dialog.getByRole('status')).toContainText('Intervals to recover');
    await expect(dialog.getByRole('status')).toContainText('1 chunk');
    await dialog.getByText('Recoverable gaps', {exact: true}).click();
    await expect(dialog.getByRole('list', {name: 'Recoverable gap ranges'})).toContainText('1 interval');
    const light = info.outputPath('backfill-preview-desktop-light.png');
    await dialog.screenshot({ path: light });
    await info.attach('Backfill range preview, desktop light', { path: light, contentType: 'image/png' });
    await dialog.getByRole('button', { name: 'Queue backfill' }).click();
    const jobHistory = panel.locator('details.sli-backfill-history');
    const finishedJobs = jobHistory.getByRole('list', {name: 'Backfill job history'});
    await expect(jobHistory.locator('summary')).toBeVisible({timeout: 60_000});
    await expect(finishedJobs).toBeHidden();
    await jobHistory.locator('summary').click();
    await expect(finishedJobs).toContainText('1 / 1 chunks committed');
    await expect(recoveredRow).toContainText('99.00%');

    const row = psql(`SELECT good_count::int,total_count::int FROM public.almanac_sli_observations
      WHERE sli_id='${id}' AND interval_start='${from}' AND backfill_chunk_id IS NOT NULL;`);
    expect(row).toBe('99|100');
    await expect.poll(async () => {
      const current = (await api(page, 'GET', `/api/v1/slos/${slo.body.id}/status`)).body.current;
      return [Number(current?.performance?.remaining_fraction), Number(current?.performance?.total) >= 200];
    }, { timeout: 60_000 }).toEqual([-1, true]);

    const earlier = new Date(Date.parse(from) - 120_000).toISOString();
    async function previewEarlierHistory(capture = false) {
      await panel.getByRole('button', { name: 'Custom range' }).click();
      await setBound('Start', earlier, capture);
      await setBound('End', from);
      const include = dialog.getByRole('switch', { name: 'Include earlier history' });
      await expect(include).not.toBeChecked();
      await include.focus();
      await page.keyboard.press('Space');
      await expect(include).toBeChecked();
      await dialog.getByRole('button', { name: 'Preview range' }).click();
      await expect(dialog.getByRole('status')).toContainText('Earlier history');
      const queries = dialog.getByText('Original queries · Checkout metrics', { exact: true });
      await expect(dialog.getByText('vector(99)', { exact: true })).toBeHidden();
      await queries.click();
      await expect(dialog.getByText('vector(99)', { exact: true })).toBeVisible();
      await queries.click();
      await expect(dialog.getByRole('button', { name: /^Start date/ })).toHaveCount(0);
      await expect(dialog.getByRole('status')).toContainText('Intervals to recover 2');
      await expect(dialog.getByRole('button', { name: 'Queue backfill' })).toBeEnabled();
      await expect(dialog.getByRole('button', { name: 'Queue backfill' })).toBeInViewport();
    }

    await previewEarlierHistory(true);
    const desktop = info.outputPath('earlier-history-desktop-light.png');
    await dialog.screenshot({ path: desktop });
    await info.attach('Earlier history preview, desktop light', { path: desktop, contentType: 'image/png' });
    await dialog.getByRole('button', { name: 'Edit range' }).click();
    await expect(dialog.getByRole('button', { name: /^Start date/ })).toBeFocused();
    await expect(dialog.getByLabel('Start time (UTC)')).toHaveValue(earlier.slice(11, 16));
    await expect(dialog.getByRole('switch', { name: 'Include earlier history' })).toBeChecked();

    await page.evaluate(() => localStorage.setItem('almanac-theme', 'dark'));
    await page.setViewportSize({ width: 360, height: 800 });
    await page.reload();
    await panel.locator(':scope > summary').click();
    await previewEarlierHistory();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    const mobile = info.outputPath('earlier-history-mobile-dark.png');
    await dialog.screenshot({ path: mobile });
    await info.attach('Earlier history preview, mobile dark', { path: mobile, contentType: 'image/png' });
    await dialog.getByRole('button', { name: 'Queue backfill' }).click();
    await jobHistory.locator('summary').click();
    await expect(finishedJobs).toContainText('2 / 2 intervals recovered', {timeout: 60_000});
    const collectedEarlier = psql(`SELECT count(*) FROM public.almanac_sli_observations
      WHERE sli_id='${id}' AND interval_start >= '${earlier}' AND interval_start < '${from}'
        AND backfill_chunk_id IS NOT NULL AND good_count=99 AND total_count=100;`);
    expect(collectedEarlier).toBe('2');
    const latest = await expectOk(api(page, 'GET', `/api/v1/slis/${id}`));
    expect(Date.parse(latest.body.effective_from)).toBe(Date.parse(earlier));
    await page.getByRole('radio', { name: '30d', exact: true }).click();
    await expect(page.getByText('Shaded area is outside available measurement history.', { exact: false })).toBeVisible();
    const chart = page.getByRole('img', { name: /Good-to-total ratio/ });
    const measurements = await expectOk(api(page, 'GET', `/api/v1/slis/${id}/measurements?period=30d`));
    const buckets = measurements.body.buckets;
    const chartStart = new Date(Date.parse(buckets.at(-1).end) - 30 * 86_400_000);
    await expect(chart.getByText(chartStart.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', timeZone: 'UTC' }), { exact: true })).toBeVisible();
    const chartImage = info.outputPath('earlier-history-30d-mobile-dark.png');
    await expect(chart.locator('.chart-point')).toBeVisible();
    const chartPanel = page.locator('.sli-measurements-panel .sli-measurements-body');
    await chartPanel.evaluate(element => element.scrollIntoView({block: 'center'}));
    await chartPanel.screenshot({ path: chartImage });
    await info.attach('30-day chart with earlier history, mobile dark', { path: chartImage, contentType: 'image/png' });
    const objective = await expectOk(api(page, 'GET', `/api/v1/slos/${slo.body.id}`));
    expect(Date.parse(objective.body.evaluation_valid_from)).toBe(Date.parse(from));
  } finally {
    stopAgent(agent);
  }
});
