import { expect, test } from '@playwright/test';
import { alertFixture, alertID } from './support/alert-fixtures.ts';

test('alert UI design evidence @matrix', async ({ page }, info) => {
  await alertFixture(page);
  for (const [name, path] of [['policies', '/alerts/policies'], ['alerts', '/alerts'], ['detail', `/alerts/${alertID}`]]) {
    await page.goto(path!);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Checkout availability', exact: true })).toBeVisible();
    if (name === 'detail') await expect(page.locator('.timeline-chart__plot')).toHaveAttribute('data-chart-ready', 'true');
    if (name === 'policies') {
      const table = page.getByRole('region', { name: 'Alert policies', exact: true });
      await expect(table.locator('.ui-resource-table')).toBeVisible();
      await expect(table.getByRole('navigation', { name: 'Policy pages' })).toBeVisible();
      if (page.viewportSize()!.width > 860) {
        const nameCell = table.locator('tbody td').first();
        const heading = table.getByRole('columnheader', { name: 'Policy', exact: true });
        expect(Math.abs((await nameCell.boundingBox())!.x - (await heading.boundingBox())!.x)).toBeLessThan(1);
        await expect(heading).toHaveCSS('text-align', 'left');
        await expect(table.getByRole('columnheader', { name: 'Status', exact: true })).toHaveCSS('text-align', 'center');
      } else {
        await expect(table.locator('td[data-label="SLO"]')).toBeVisible();
        expect(await table.locator('td[data-label="SLO"]').evaluate(element => getComputedStyle(element, '::before').content)).toBe('"SLO"');
      }
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: `../.local/alert-ui-evidence/${process.env.ALERT_UI_PHASE ?? 'after'}/${name}-${info.project.name}.png`, fullPage: true });
  }
});


test('alert navigation stays below the heading and preserves history', async ({ page }) => {
  await alertFixture(page);
  await page.goto('/alerts');
  const views = page.getByRole('radiogroup', { name: 'Alert view' });
  expect((await views.boundingBox())!.y).toBeGreaterThan((await page.getByRole('heading', { level: 1 }).boundingBox())!.y);
  await page.getByRole('radio', { name: 'Active', exact: true }).focus();
  await page.keyboard.press('ArrowRight');
  await expect(page).toHaveURL('/alerts?view=history');
  await expect(page.getByRole('heading', { name: 'Alert history', exact: true })).toBeVisible();
  await page.goBack();
  await expect(page.getByRole('radio', { name: 'Active', exact: true })).toBeChecked();
  await page.getByRole('link', { name: 'Policies', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Alert policies', exact: true })).toBeVisible();
  await page.getByRole('link', { name: 'View alerts', exact: true }).click();
  await expect(page).toHaveURL('/alerts');
});

test('sampled chart explains its limits beside the plot and keeps exact evidence', async ({ page }, info) => {
  await alertFixture(page, { sampledSeries: true });
  await page.goto(`/alerts/${alertID}`);
  await expect(page.locator('.alert-detail-chart-note')).toContainText('brief peaks may not appear');
  await expect(page.locator('.alert-list-notice')).toHaveCount(0);
  await expect(page.getByRole('region', { name: 'Current alert state' })).toContainText('Open 2 min');
  await expect(page.getByRole('region', { name: 'Current alert state' })).not.toContainText('Last evaluated:');
  await page.getByRole('button', { name: 'Acknowledge', exact: true }).click();
  await expect(page.getByRole('region', { name: 'Current alert state' })).toContainText('Acknowledged by You');
  await expect(page.getByText(/Someone is handling|A condition matched/)).toHaveCount(0);
  await page.screenshot({ path: '../.local/alert-ui-evidence/after/sampled-acknowledged.png', fullPage: true });
  await info.attach('sampled-acknowledged', { body: await page.screenshot({ fullPage: true }), contentType: 'image/png' });
});


test('a save completing on the policies list refreshes its rows', async ({ page }) => {
  let finish!: () => void;
  const createDelay = new Promise<void>(resolve => { finish = resolve; });
  await alertFixture(page, { createDelay });
  await page.goto('/alerts/policies/new');
  await page.getByLabel('Alert policy name').fill('Delayed policy');
  await page.getByRole('combobox', { name: 'SLO', exact: true }).fill('Checkout');
  await page.getByRole('option', { name: /Checkout availability/ }).click();
  await page.getByRole('button', { name: 'Create alert policy', exact: true }).click();
  await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Policies', exact: true }).click();
  await expect(page.getByRole('link', { name: 'Checkout burn', exact: true })).toBeVisible();
  finish();
  await expect(page.getByRole('link', { name: 'Delayed policy', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'Checkout burn', exact: true })).toHaveCount(0);
});

test('policy tables share the existing services presentation @matrix', async ({ page }, info) => {
  const { servicePage } = await import('./support/service-fixtures.ts');
  await alertFixture(page);
  await page.route('**/api/v1/services?*', route => route.fulfill({ json: servicePage() }));
  await page.goto('/services');
  await expect(page.getByRole('heading', { name: 'Services', exact: true })).toBeVisible();
  const reference = page.locator('.services-table');
  await expect(reference).toBeVisible();
  const referenceStyle = await reference.locator('tbody td').first().evaluate(element => {
    const style = getComputedStyle(element);
    return { fontSize: style.fontSize, lineHeight: style.lineHeight, paddingLeft: style.paddingLeft };
  });
  await page.evaluate(() => document.fonts.ready);
  await page.screenshot({ path: `../.local/alert-ui-evidence/after/services-reference-${info.project.name}.png`, fullPage: true });
  await page.goto('/alerts/policies');
  const policy = page.getByRole('link', { name: 'Checkout burn', exact: true }).locator('..');
  await expect(policy).toBeVisible();
  const policyStyle = await policy.evaluate(element => {
    const style = getComputedStyle(element);
    return { fontSize: style.fontSize, lineHeight: style.lineHeight, paddingLeft: style.paddingLeft };
  });
  expect(policyStyle).toEqual(referenceStyle);
});


test('save feedback floats without moving the policies toolbar', async ({ page }) => {
  await alertFixture(page);
  await page.goto('/alerts/policies');
  const search=page.getByRole('searchbox',{name:'Search policies'});
  const before=(await search.boundingBox())!.y;
  await page.getByRole('link',{name:'Checkout burn',exact:true}).click();
  await page.getByRole('button',{name:'Save changes',exact:true}).click();
  await expect(page).toHaveURL('/alerts/policies');
  await expect(page.getByRole('status').filter({hasText:'Alert policy saved.'})).toBeVisible();
  await expect(page.locator('.ui-toast')).toHaveCSS('position','fixed');
  expect((await search.boundingBox())!.y).toBe(before);
  await page.screenshot({path:'../.local/alert-ui-evidence/refined/save-toast.png',fullPage:true});
  await page.getByRole('button',{name:'Dismiss notification'}).click();
  await expect(page.getByText('Alert policy saved.',{exact:true})).toHaveCount(0);
});

test('success toast supports keyboard dismissal, repeated saves and reading time @matrix', async ({ page }, info) => {
  await page.goto('/design-system#operational');
  const show=page.getByRole('button',{name:'Show success toast',exact:true});
  await expect(show).toBeVisible();
  await page.clock.install({time:new Date('2026-11-17T12:00:00Z')});
  await page.clock.pauseAt(new Date('2026-11-17T12:00:01Z'));
  await show.click();
  const toast=page.locator('.ui-toast[data-open=true]');
  await expect(toast).toBeVisible();
  const rect=(await toast.boundingBox())!;
  expect(rect.x).toBeGreaterThanOrEqual(0);expect(rect.x+rect.width).toBeLessThanOrEqual(page.viewportSize()!.width);
  await toast.hover();await page.clock.fastForward(6000);await expect(toast).toBeVisible();
  const dismiss=toast.getByRole('button',{name:'Dismiss notification'});
  await dismiss.focus();await page.mouse.move(0,0);await page.clock.fastForward(6000);await expect(toast).toBeVisible();
  await page.screenshot({path:`../.local/alert-ui-evidence/refined/toast-${info.project.name}.png`,fullPage:false});
  await dismiss.press('Enter');await expect(toast).toHaveCount(0);
  await show.click();await expect(toast).toBeVisible();await page.mouse.move(0,0);
  await page.clock.fastForward(4000);await show.click();await page.mouse.move(0,0);
  await page.clock.fastForward(4999);await expect(toast).toBeVisible();
  await page.clock.fastForward(1);await expect(toast).toHaveCount(0);
});
