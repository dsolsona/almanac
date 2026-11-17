import {expect,test} from '@playwright/test';
import {alertFixture,alertID,policyID,alertSlo} from './support/alert-fixtures.ts';

test('selected alert list, evidence and compact creation remain usable @matrix',async({page},info)=>{
 const errors:string[]=[];page.on('pageerror',error=>errors.push(error.message));
 const fixture=await alertFixture(page);
 await page.goto('/alerts');
 await expect(page.getByRole('heading',{name:'Alerts',exact:true})).toBeVisible();
 await expect(page.getByRole('link',{name:'Checkout burn',exact:true})).toBeVisible();
 await expect(page.getByRole('radiogroup',{name:'Alert view'})).toBeVisible();
 await expect(page.getByText('Both ≥ 14.4× burn')).toBeVisible();
 if(page.viewportSize()!.width<=1100){await expect(page.locator('.alert-mobile-summary')).toContainText('Firing');await expect(page.locator('.alert-mobile-summary')).toContainText('14.4×');}
 await page.screenshot({path:`../.local/alerts-public/list-${info.project.name}.png`,fullPage:true});
 await page.getByRole('button',{name:'Group by SLO',exact:true}).click();
 await expect(page.locator('.alert-group')).toContainText('1 alert · 1 firing');
 await page.locator('.alert-table tbody tr:not(.alert-group) td').nth(4).click();
 await expect(page).toHaveURL(`/alerts/${alertID}`);
 await expect(page.getByRole('heading',{name:'Checkout burn',exact:true})).toBeVisible();
 await expect(page.getByRole('link',{name:alertSlo.display_name,exact:true})).toHaveAttribute('href',`/slos/${alertSlo.id}`);
 await page.getByText('Evidence details',{exact:true}).click();
 await expect(page.getByText('144 bad of 10,000')).toHaveCount(2);
 await expect(page.getByText('1.44% ÷ 0.1%')).toHaveCount(2);
 const chart=page.locator('.timeline-chart__plot').first();
 await expect(chart.locator('svg')).toBeVisible();
 const inspection=page.getByRole('group',{name:/^Inspect Burn rate/});
 await inspection.focus();await page.keyboard.press('End');await expect(page.getByRole('tooltip')).toContainText('14.4');await page.keyboard.press('Escape');
 await page.screenshot({path:`../.local/alerts-public/detail-${info.project.name}.png`,fullPage:true});
 await page.getByRole('button',{name:'Acknowledge',exact:true}).click();
 await expect(page.getByText('Acknowledged by')).toBeVisible();
 await expect(page.getByRole('button',{name:'Acknowledge',exact:true})).toHaveCount(0);
 await page.reload();await expect(page.getByText('Acknowledged by')).toBeVisible();
 await page.getByRole('link',{name:'← Alerts'}).click();
 await page.getByRole('link',{name:'New alert policy',exact:true}).click();
 await page.getByLabel('Alert policy name').fill('Checkout error budget');
 await page.getByRole('combobox',{name:'SLO',exact:true}).fill('Checkout');
 await page.getByRole('option',{name:new RegExp(alertSlo.display_name)}).click();
 await expect(page.getByRole('radio',{name:'Medium',exact:true})).toBeChecked();
 await page.screenshot({path:`../.local/alerts-public/create-${info.project.name}.png`,fullPage:true});
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.getByRole('button',{name:'Create alert policy',exact:true}).click();
 await expect(page).toHaveURL('/alerts/policies');
 await expect(page.getByRole('status')).toHaveText('Alert policy saved.');
 await expect(page.getByRole('link',{name:'Checkout error budget',exact:true})).toBeVisible();
 await page.getByRole('link',{name:'Checkout error budget',exact:true}).click();
 await expect(page.getByLabel('Alert policy name')).toHaveValue('Checkout error budget');
 expect(fixture.created().count).toBe(1);
 const ack=fixture.requests.find(request=>request.path.endsWith('/acknowledge'))!;
 expect(ack.etag).toBe(`"alert-occurrence:${alertID}:1"`);expect(ack.key).toBeTruthy();expect(errors).toEqual([]);
});

test('uncertain creation reconciles its original command without issuing another create',async({page})=>{
 const fixture=await alertFixture(page,{uncertainCreate:true});
 await page.goto('/alerts/policies/new');
 await page.getByLabel('Alert policy name').fill('Uncertain policy');
 await page.getByRole('combobox',{name:'SLO'}).fill('Checkout');
 await page.getByRole('option',{name:new RegExp(alertSlo.display_name)}).click();
 await page.getByRole('button',{name:'Create alert policy',exact:true}).click();
 await expect(page.getByRole('heading',{name:'Command outcome needs confirmation'})).toBeVisible();
 await page.getByRole('link',{name:'Settings',exact:true}).click();
 await expect(page.getByRole('heading',{name:'Alert command needs review'})).toBeVisible();
 await page.getByRole('link',{name:'Review alert command'}).click();
 await expect(page.getByRole('heading',{name:'Command outcome needs confirmation'})).toBeVisible();
 await page.getByRole('button',{name:'Check command result'}).click();
 await expect(page).toHaveURL('/alerts/policies');
 expect(fixture.created().count).toBe(1);
 const lookup=fixture.requests.find(request=>request.path.startsWith('/api/v1/alert-commands'))!;
 expect(new URL(lookup.path,'http://fixture').searchParams.get('idempotency_key')).toBe(fixture.created().key);
});

test('viewer can inspect alerts and policies without mutation controls',async({page})=>{
 await alertFixture(page,{role:'viewer'});await page.goto('/alerts');
 await expect(page.getByRole('link',{name:'Checkout burn',exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'Acknowledge',exact:true})).toHaveCount(0);
 await expect(page.getByRole('link',{name:'New alert policy'})).toHaveCount(0);
 await page.goto(`/alerts/policies/${policyID}`);
 await expect(page.getByRole('heading',{name:'Policy is read only'})).toBeVisible();
 await expect(page.getByRole('button',{name:'Save changes'})).toHaveCount(0);
});

test('a conflicting update preserves the draft until explicit reload',async({page})=>{
 await alertFixture(page,{conflictUpdate:true});await page.goto(`/alerts/policies/${policyID}`);
 await page.getByLabel('Alert policy name').fill('My unsaved rename');
 await page.getByRole('button',{name:'Save changes',exact:true}).click();
 await expect(page.getByRole('alert')).toContainText('This item changed.');
 await expect(page.getByLabel('Alert policy name')).toHaveValue('My unsaved rename');
 await page.getByRole('button',{name:'Reload saved policy and discard edits'}).click();
 await expect(page.getByLabel('Alert policy name')).toHaveValue('Checkout burn');
});
test('an expired receipt offers deliberate current-resource reconciliation',async({page})=>{
 await alertFixture(page,{uncertainCreate:true,expiredReceipt:true});await page.goto('/alerts/policies/new');
 await page.getByLabel('Alert policy name').fill('Expired policy');await page.getByRole('combobox',{name:'SLO'}).fill('Checkout');await page.getByRole('option',{name:new RegExp(alertSlo.display_name)}).click();
 await page.getByRole('button',{name:'Create alert policy',exact:true}).click();await page.getByRole('button',{name:'Check command result'}).click();
 await expect(page.getByRole('alert')).toContainText('expired');
 await expect(page.getByRole('button',{name:'Retry same command'})).toHaveCount(0);
 await page.getByRole('button',{name:'Read current resource'}).click();
 await expect(page.getByText(/A name search cannot prove/)).toBeVisible();
 await page.getByRole('button',{name:'I have reviewed the current state; release command'}).click();
 await expect(page.getByRole('heading',{name:'Command outcome needs confirmation'})).toHaveCount(0);
 await expect(page.getByRole('button',{name:'Create alert policy',exact:true})).toBeEnabled();
});

test('alert readiness waits for usable rows and detail chart paint',async({page})=>{
 let release!:()=>void;const delayed=new Promise<void>(resolve=>{release=resolve;});
 await page.addInitScript(()=>{Object.assign(window,{alertJourneys:[]});addEventListener('almanac:journey',event=>{const detail=(event as CustomEvent).detail;(window as unknown as {alertJourneys:unknown[]}).alertJourneys.push({...detail,rows:document.querySelectorAll('.alert-table tbody tr:not(.alert-group) .alert-name').length,chart:document.querySelectorAll('.timeline-chart__plot svg').length});});});
 await alertFixture(page,{tracing:true,listDelay:delayed});await page.goto('/alerts');
 await expect(page.getByRole('heading',{name:'Alerts',exact:true})).toBeVisible();
 expect(await page.evaluate(()=>(window as unknown as {alertJourneys:unknown[]}).alertJourneys)).toEqual([]);
 release();await page.waitForFunction(()=>(window as unknown as {alertJourneys:{journey:string}[]}).alertJourneys.some(value=>value.journey==='alert.list'));
 expect(await page.evaluate(()=>(window as unknown as {alertJourneys:{rows:number;result:string}[]}).alertJourneys[0])).toMatchObject({rows:1,result:'ready'});
 await page.getByRole('link',{name:'Checkout burn',exact:true}).click();
 await page.waitForFunction(()=>(window as unknown as {alertJourneys:{journey:string}[]}).alertJourneys.some(value=>value.journey==='alert.detail'));
 const journey=await page.evaluate(()=>(window as unknown as {alertJourneys:{journey:string;chart:number;result:string}[]}).alertJourneys.find(value=>value.journey==='alert.detail'));
 expect(journey).toMatchObject({result:'ready',chart:1});
});
