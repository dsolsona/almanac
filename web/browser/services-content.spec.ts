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
