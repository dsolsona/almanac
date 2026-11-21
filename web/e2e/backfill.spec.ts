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
    const disclosure = panel.locator('summary');
    await disclosure.focus();
    await page.keyboard.press('Enter');
    await panel.getByLabel('Range', { exact: true }).selectOption('custom');
    await panel.getByLabel('Start (UTC)').fill(from.slice(0, 16));
    await panel.getByLabel('End (UTC)').fill(to.slice(0, 16));
    await panel.getByRole('button', { name: 'Preview range' }).click();
    await expect(panel.getByRole('status')).toContainText('1 chunk');
    const light = info.outputPath('backfill-preview-desktop-light.png');
    await panel.screenshot({ path: light });
    await info.attach('Backfill range preview, desktop light', { path: light, contentType: 'image/png' });
    await panel.getByRole('button', { name: 'Request backfill' }).click();
    await expect(panel.getByRole('list', { name: 'Backfill jobs' })).toContainText('1 / 1 chunks committed', { timeout: 60_000 });

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
    await panel.locator('summary').click();
    await expect(panel.getByRole('list', { name: 'Backfill jobs' })).toContainText('1 / 1 chunks committed');
    const mobile = info.outputPath('backfill-completed-mobile-dark.png');
    await panel.screenshot({ path: mobile });
    await info.attach('Backfill committed, mobile dark', { path: mobile, contentType: 'image/png' });
  } finally {
    stopAgent(agent);
  }
});
