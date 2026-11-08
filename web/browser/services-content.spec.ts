import {expect,test} from '@playwright/test';
import {services,servicePage} from './support/service-fixtures.ts';
test('maximum length service content remains readable and keeps actions reachable @matrix',async({page})=>{
 const service={...services[0]!,display_name:'X'.repeat(128),owner:'O'.repeat(200),description:'D'.repeat(4000)};
 await page.route('**/api/**',route=>route.fulfill({status:503,json:{code:'fixture_only'}}));
 await page.route('**/api/v1/auth/context',route=>route.fulfill({json:{csrf_token:'csrf',session:{user_id:service.id,login:'Editor',role:'editor',restricted:false}}}));
 await page.route('**/api/v1/services**',route=>route.fulfill({json:{...servicePage([service]),items:[service],total:1,next_cursor:''}}));
 await page.goto('/services');await page.locator('.services-table tbody tr').waitFor();
 const panel=await page.locator('.service-panel').boundingBox(),last=await page.locator('.services-table tbody tr td').last().boundingBox();
 expect(last!.x+last!.width).toBeLessThanOrEqual(panel!.x+panel!.width);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await expect(page.getByRole('link',{name:service.display_name,exact:true})).toBeVisible();
 await page.goto('/services/dependencies');await page.locator('.map-node').waitFor({state:'attached'});
 const title=await page.locator('.map-node text:not(.subtitle)').textContent();expect(title!.length).toBeLessThanOrEqual(27);
});

test('the Services graph keeps SLO colors when selected and explains unknown results @matrix',async({page})=>{
 const records=[services.find(service=>service.name==='checkout')!,services.find(service=>service.name==='payments')!,services.find(service=>service.name==='search')!,services.find(service=>service.name==='notifications')!].map(service=>({...service,owner:''}));
 await page.route('**/api/**',route=>route.fulfill({status:503,json:{code:'fixture_only'}}));
 await page.route('**/api/v1/auth/context',route=>route.fulfill({json:{csrf_token:'csrf',session:{user_id:records[0]!.id,login:'Editor',role:'editor',restricted:false}}}));
 await page.route('**/api/v1/services**',route=>route.fulfill({json:{...servicePage(records),items:records,total:4,next_cursor:''}}));
 await page.goto('/services');
 await page.getByRole('link',{name:'Graph',exact:true}).click();
 await expect(page.getByRole('link',{name:'Graph',exact:true})).toHaveAttribute('aria-current','page');
 const legend=page.getByRole('group',{name:'Service SLO status'});
 for(const label of ['On track','Breached','Unknown','No SLOs'])await expect(legend.getByText(label,{exact:true})).toBeVisible();
 await expect(legend.getByTitle('Unknown includes stale, incomplete or unavailable SLO results.')).toBeVisible();
 await expect(page.locator('.map-node')).toHaveCount(4);
 await expect(page.getByText('Unassigned',{exact:true})).toHaveCount(0);
 if(page.viewportSize()!.width>860){
  const breached=page.getByRole('button',{name:'Select Checkout, Breached, 3 SLOs',exact:true});
  const stroke=await breached.locator('.map-node-core').evaluate(node=>getComputedStyle(node).stroke);
  await page.getByRole('button',{name:'Select Payments, On track, 2 SLOs',exact:true}).focus();await page.keyboard.press('Enter');
  await expect(page.locator('.graph-details')).toContainText('Payments');
  await expect(breached.locator('.map-node-core')).toHaveCSS('stroke',stroke);
  const tokens=['--status-breached','--status-healthy','--status-stale','--border-strong'];
  for(const [index,service]of records.entries()){
   const node=page.locator(`.map-node[data-id="${service.id}"] .map-node-core`);
   const color=await page.evaluate(token=>{const probe=document.createElement('span');probe.style.color=`var(${token})`;document.body.append(probe);const color=getComputedStyle(probe).color;probe.remove();return color;},tokens[index]!);
   await expect(node).toHaveCSS('stroke',color);
  }
  await page.screenshot({path:`../.local/services-parity/graph-status-${page.viewportSize()!.width}-${test.info().project.name}.png`,fullPage:true});
 }else await expect(page.locator('.graph-mobile')).toBeVisible();
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
});
