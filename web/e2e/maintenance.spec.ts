import { expect, test } from '@playwright/test';
import { api, expectOk, unique } from './support/api.ts';
import { startAgent, stopAgent } from './support/stack.ts';

test('schedule and inspect explicit SLO maintenance in the deployed app', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 1440, height: 1120 });
  const name = unique('maintenance-journey');
  const displayName = 'Checkout database upgrade';
  const service = await expectOk(api(page, 'POST', '/api/v1/services', { name, display_name: 'Checkout' }));
  const registration = await expectOk(api(page, 'POST', '/api/v1/agents', { name, display_name: 'Maintenance collector' }));
  const agent = startAgent(registration.body.token);
  try {
    await expect.poll(async () => (await api(page, 'GET', `/api/v1/agents/${registration.body.agent.id}`)).body.runtime?.reachability, { timeout: 30_000 }).toBe('connected');
    const source = await expectOk(api(page, 'POST', '/api/v1/datasources', {
      name, display_name: name, provider: 'prometheus', configuration_format: 'prometheus-http-v1',
      endpoint: 'http://victoriametrics:8428', tls: { insecure_skip_verify: false, ca_pem: null },
      authentication: { mode: 'none', username: null, password_secret: { action: 'clear' }, token_secret: { action: 'clear' } },
      polling_interval_seconds: 60, agent_id: registration.body.agent.id,
    }));
    const sli = await expectOk(api(page, 'POST', '/api/v1/slis', {
      name, display_name: name, datasource_id: source.body.id, measurement_kind: 'event_counts', component_mode: 'interval_count',
      good_query: 'vector(99)', total_query: 'vector(100)',
    }));
    const slo = await expectOk(api(page, 'POST', '/api/v1/slos', {
      name, display_name: 'Checkout availability', service_id: service.body.id, sli_id: sli.body.id, target: '0.999', rolling_window_seconds: 2592000,
    }));

    await page.getByRole('link', { name: 'Maintenance', exact: true }).click();
    await expect(page).toHaveURL(/\/maintenance$/);
    await page.getByRole('link', { name: 'Schedule maintenance', exact: true }).click();
    await expect(page.locator('form > section > header > h2')).toHaveText(['Context', 'Scope', 'Schedule']);
    await page.getByRole('textbox', { name: 'Name', exact: true }).fill(displayName);
    await page.getByRole('textbox', { name: 'Reason', exact: true }).fill('Apply the scheduled database update.');
    const picker = page.getByRole('combobox', { name: 'Affected SLOs', exact: true });
    await picker.fill('Checkout availability');
    await expect(page.getByRole('option', { name: /^Checkout availability/ })).toBeVisible();
    await picker.press('ArrowDown');
    await picker.press('Enter');
    const chip = page.getByRole('button', { name: 'Remove Checkout availability', exact: true });
    await expect(chip).toBeVisible();
    await expect(picker).toBeFocused();
    expect((await chip.boundingBox())!.y).toBeGreaterThan((await picker.boundingBox())!.y);

    const start = new Date(Date.now() + 86400_000);
    start.setUTCHours(12, 0, 0, 0);
    const calendarName = new Intl.DateTimeFormat('en', { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric', timeZone: 'UTC' }).format(start);
    const calendar = page.getByRole('button', { name: /^Start date/ });
    await calendar.click();
    const day = page.getByRole('button', { name: calendarName, exact: true });
    if (start.getUTCDate() === 1) await page.getByRole('button', { name: 'Next month' }).click();
    await day.click();
    await page.getByLabel('Start time (UTC)', { exact: true }).fill('12:00');
    const preset = page.getByRole('radio', { name: '2 hours', exact: true });
    await page.getByRole('radio', { name: 'Custom…', exact: true }).click();
    const minutes = page.getByRole('textbox', { name: 'Custom duration', exact: true });
    await expect(minutes).toBeFocused();
    await minutes.fill('75');
    expect(Math.abs((await preset.boundingBox())!.y - (await minutes.boundingBox())!.y)).toBeLessThan(4);
    await page.evaluate(() => window.scrollTo(0, 0));
    await page.screenshot({ path: testInfo.outputPath('maintenance-schedule-light.png'), fullPage: true });
    await testInfo.attach('maintenance-schedule-light', { path: testInfo.outputPath('maintenance-schedule-light.png'), contentType: 'image/png' });

    await page.getByRole('button', { name: 'Schedule maintenance', exact: true }).click();
    await expect(page.getByRole('heading', { name: displayName, exact: true, level: 1 })).toBeVisible();
    const pathname = new URL(page.url()).pathname;
    let saved = await expectOk(api(page, 'GET', `/api/v1${pathname}`));
    expect(saved.body.slo_ids).toEqual([slo.body.id]);
    expect(Date.parse(saved.body.start)).toBe(start.getTime());
    expect(Date.parse(saved.body.end) - Date.parse(saved.body.start)).toBe(75 * 60_000);
    await expect(page.getByRole('link', { name: 'Checkout availability', exact: true })).toHaveAttribute('href', `/slos/${slo.body.id}`);
    await expect(page.getByText('Scheduled', { exact: true })).toBeVisible();
    await page.getByRole('link', { name: 'Edit schedule', exact: true }).click();
    await expect(page.getByRole('heading', { name: 'Edit maintenance schedule', exact: true })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Remove Checkout availability', exact: true })).toBeVisible();
    await page.getByRole('textbox', { name: 'Reason', exact: true }).fill('Apply the approved database update.');
    await page.getByRole('button', { name: 'Save schedule', exact: true }).click();
    await expect(page.getByRole('heading', { name: displayName, exact: true, level: 1 })).toBeVisible();
    const previous = saved;
    saved = await expectOk(api(page, 'GET', `/api/v1${pathname}`));
    expect(saved.body.revision).toBe('2');
    expect(saved.body.reason).toBe('Apply the approved database update.');
    expect(saved.body.start).toBe(previous.body.start);
    expect(saved.body.end).toBe(previous.body.end);
    expect(saved.body.slo_ids).toEqual(previous.body.slo_ids);

    const replacement = await expectOk(api(page, 'POST', '/api/v1/slos', {
      name: `${name}-replacement`, display_name: 'Checkout latency', service_id: service.body.id, sli_id: sli.body.id,
      target: '0.99', rolling_window_seconds: 2592000,
    }));
    await page.getByRole('link', { name: 'Edit schedule', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Remove Checkout availability', exact: true })).toBeVisible();
    const draftReason = 'Deploy the original database update.';
    const currentReason = 'Maintenance now covers Checkout latency.';
    await page.getByRole('textbox', { name: 'Reason', exact: true }).fill(draftReason);
    await expectOk(api(page, 'PUT', `/api/v1${pathname}`, {
      title: displayName, reason: currentReason, start: saved.body.start, end: saved.body.end, slo_ids: [replacement.body.id],
    }, { 'If-Match': saved.etag! }));
    const save = page.getByRole('button', { name: 'Save schedule', exact: true });
    await save.click();
    await page.getByRole('button', { name: 'Read latest revision', exact: true }).click();
    const current = page.getByRole('region', { name: 'Current revision 3', exact: true });
    await expect(current.getByText(currentReason, { exact: true })).toBeVisible();
    await expect(current.getByRole('listitem')).toHaveText(['Checkout latency · Checkout', 'Checkout availability · Checkout']);
    await expect(save).toBeDisabled();
    await expect(page.getByRole('textbox', { name: 'Reason', exact: true })).toHaveValue(draftReason);
    await page.screenshot({ path: testInfo.outputPath('maintenance-edit-conflict.png'), fullPage: true });
    await testInfo.attach('maintenance-edit-conflict', { path: testInfo.outputPath('maintenance-edit-conflict.png'), contentType: 'image/png' });
    await page.getByRole('button', { name: 'Keep my draft', exact: true }).press('Enter');
    await expect(save).toBeFocused();
    await save.press('Enter');
    await expect(page.getByRole('heading', { name: displayName, exact: true, level: 1 })).toBeVisible();
    saved = await expectOk(api(page, 'GET', `/api/v1${pathname}`));
    expect(saved.body.revision).toBe('4');
    expect(saved.body.reason).toBe(draftReason);
    expect(saved.body.slo_ids).toEqual([slo.body.id]);

    await page.screenshot({ path: testInfo.outputPath('maintenance-detail-light.png'), fullPage: true });
    await testInfo.attach('maintenance-detail-light', { path: testInfo.outputPath('maintenance-detail-light.png'), contentType: 'image/png' });

    await page.evaluate(() => localStorage.setItem('almanac-theme', 'dark'));
    await page.setViewportSize({ width: 360, height: 820 });
    await page.goto('/maintenance');
    const scheduledLink = page.getByRole('link', { name: displayName, exact: true }).and(page.locator(`a[href="${pathname}"]`));
    await expect(scheduledLink).toBeVisible();
    await expect(page.getByRole('combobox', { name: 'Per page', exact: true })).toHaveValue('10');
    await expect(page.locator('html')).toHaveCSS('color-scheme', 'dark');
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBe(360);
    await page.screenshot({ path: testInfo.outputPath('maintenance-list-dark-mobile.png'), fullPage: true });
    await testInfo.attach('maintenance-list-dark-mobile', { path: testInfo.outputPath('maintenance-list-dark-mobile.png'), contentType: 'image/png' });

    const seeded = { reason: 'Exercise pagination in the isolated fixture.', slo_ids: saved.body.slo_ids, start: saved.body.start, end: saved.body.end };
    for (let index = 1; index <= 10; index++) {
      await expectOk(api(page, 'POST', '/api/v1/maintenance', { ...seeded, title: `Pagination window ${index}` }));
    }
    await page.getByRole('button', { name: 'Refresh maintenance', exact: true }).click();
    const rows = page.locator('.ui-resource-table tbody tr');
    await expect(rows).toHaveCount(10);
    await page.getByRole('button', { name: 'Next ›', exact: true }).click();
    await expect(page.getByText('Page 2', { exact: true })).toBeVisible();
    await expect(rows.first()).toBeVisible();
    await expect.poll(() => rows.count()).toBeLessThan(10);
    await page.getByRole('combobox', { name: 'Per page', exact: true }).selectOption('20');
    await expect.poll(() => rows.count()).toBeGreaterThanOrEqual(11);
    await expect.poll(() => rows.count()).toBeLessThanOrEqual(20);

    const historicalStart = new Date(Math.floor(Date.now() / 60_000) * 60_000 - 60 * 60_000);
    const historicalEnd = new Date(historicalStart.getTime() + 2 * 60 * 60_000);
    const active = await expectOk(api(page, 'POST', '/api/v1/maintenance', {
      title: 'Checkout load balancer update',
      reason: 'Switch traffic after the upgrade.',
      start: historicalStart.toISOString(),
      end: historicalEnd.toISOString(),
      slo_ids: [slo.body.id],
    }));
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto(`/slos/${slo.body.id}?range=7d`);
    await expect(page.locator('.slo-maintenance-band')).toHaveCount(3);
    await page.locator('summary').filter({ hasText: 'Maintenance periods' }).click();
    const link = page.getByRole('link', { name: 'Checkout load balancer update', exact: true });
    await expect(link).toHaveAttribute('href', `/maintenance/${active.body.id}`);
    await page.screenshot({ path: testInfo.outputPath('slo-maintenance-timeline-dark.png'), fullPage: true });
    await testInfo.attach('slo-maintenance-timeline-dark', { path: testInfo.outputPath('slo-maintenance-timeline-dark.png'), contentType: 'image/png' });
    await link.focus();
    await link.press('Enter');
    await expect(page.getByRole('heading', { name: 'Checkout load balancer update', exact: true, level: 1 })).toBeVisible();

    const shortEnd = new Date(historicalEnd.getTime() - 20 * 60_000);
    await page.getByRole('button', { name: 'End maintenance early', exact: true }).click();
    let dialog = page.getByRole('dialog', { name: 'End this maintenance early?' });
    const reason = dialog.getByLabel('Reason for this change', { exact: true });
    await reason.fill('Traffic switch finished.');
    const date = dialog.getByRole('button', { name: /^New end date/ });
    await date.click();
    const calendarDialog = page.getByRole('dialog', { name: /Choose New end date/ });
    await expect(calendarDialog).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(calendarDialog).toBeHidden();
    await expect(date).toBeFocused();
    await expect(reason).toHaveValue('Traffic switch finished.');
    await date.click();
    await expect(calendarDialog).toBeVisible();
    const endMonth = new Intl.DateTimeFormat('en', { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(shortEnd);
    if (!await calendarDialog.getByRole('grid', { name: endMonth, exact: true }).count()) {
      await calendarDialog.getByRole('button', { name: 'Next month' }).click();
    }
    const endDay = new Intl.DateTimeFormat('en', { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric', timeZone: 'UTC' }).format(shortEnd);
    await calendarDialog.getByRole('button', { name: endDay }).click();
    await dialog.getByLabel('New end time (UTC)', { exact: true }).fill(shortEnd.toISOString().slice(11, 16));
    await dialog.getByRole('button', { name: 'End maintenance', exact: true }).click();
    await expect(dialog).toHaveCount(0);
    const shortened = await expectOk(api(page, 'GET', `/api/v1/maintenance/${active.body.id}`));
    expect(shortened.body.revision).toBe('2');
    expect(Date.parse(shortened.body.start)).toBe(historicalStart.getTime());
    expect(Date.parse(shortened.body.end)).toBe(shortEnd.getTime());

    await page.getByText('Change historical SLO calculations', { exact: true }).click();
    await page.getByRole('button', { name: 'Include maintenance events…', exact: true }).click();
    dialog = page.getByRole('dialog', { name: 'Include maintenance events in SLO calculations?' });
    await expect(dialog).toContainText('Other maintenance exclusions still apply.');
    await dialog.getByLabel('Reason for this change', { exact: true }).fill('No loss of service occurred.');
    const correctionShot = testInfo.outputPath('maintenance-historical-correction-dark.png');
    await page.screenshot({ path: correctionShot });
    await testInfo.attach('maintenance-historical-correction-dark', { path: correctionShot, contentType: 'image/png' });
    await dialog.getByRole('button', { name: 'Include maintenance events', exact: true }).click();
    await expect(page.getByText('Withdrawn', { exact: true })).toBeVisible();
    const withdrawn = await expectOk(api(page, 'GET', `/api/v1/maintenance/${active.body.id}`));
    expect(withdrawn.body.operation).toBe('withdraw');
    expect(withdrawn.body.revision).toBe('3');
    await page.getByRole('link', { name: 'Previous revision', exact: true }).click();
    await expect(page.getByText(/Revision 2 ·/)).toBeVisible();

    await page.goto(pathname);
    await page.getByRole('button', { name: 'Cancel scheduled maintenance', exact: true }).click();
    dialog = page.getByRole('dialog', { name: 'Cancel this maintenance window?' });
    await dialog.getByLabel('Reason for this change', { exact: true }).fill('Postpone to a new date.');
    await expectOk(api(page, 'PUT', `/api/v1${pathname}`, { ...seeded, title: displayName, reason: 'Newer revision.' }, { 'If-Match': saved.etag! }));
    await dialog.getByRole('button', { name: 'Cancel maintenance', exact: true }).click();
    await expect(dialog.getByRole('alert')).toContainText('The window changed');
    await dialog.getByRole('button', { name: 'Read latest revision', exact: true }).click();
    await expect(dialog.getByLabel('Reason for this change', { exact: true })).toHaveValue('Postpone to a new date.');
    await dialog.getByRole('button', { name: 'Cancel maintenance', exact: true }).click();
    await expect(page.getByText('Cancelled', { exact: true })).toBeVisible();


  } finally {
    stopAgent(agent);
  }
});
