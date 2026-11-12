const {chromium}=require('/workspace/web/node_modules/playwright');
const assert=require('node:assert/strict');
(async()=>{
const start=Date.now(),browser=await chromium.launch(),page=await browser.newPage(),errors=[];let checks=0;
page.setDefaultTimeout(5000);page.on('pageerror',e=>errors.push(e.message));
const url='file:///workspace/docs/design/mocks/alert-options/index.html';
const expect=(condition,message)=>{assert(condition,message);checks++};
const open=async(key)=>page.locator(`[data-filter="${key}"]`).click();
const apply=async()=>page.getByRole('button',{name:'Apply',exact:true}).click();
const set=async(key,value)=>{await open(key==='severity'?'alert':key);await page.locator('#filter-'+key)[key==='alert'?'fill':'selectOption'](value);await apply()};
for(const theme of ['light','dark'])for(const width of [1440,1024,360]){
 await page.setViewportSize({width,height:1000});await page.goto(`${url}?theme=${theme}#a/list`);await page.evaluate(()=>document.fonts.ready);await page.getByRole('button',{name:'Reset demo',exact:true}).click();await page.evaluate(()=>document.getElementById('announcement').textContent='');
 expect(await page.locator('[data-open]').count()===4);expect(await page.locator('.stats,.column-filters').count()===0);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 if(width===1440){expect(await page.locator('.table-scroll').evaluate(e=>e.scrollWidth<=e.clientWidth),'desktop table fits');expect((await page.locator('[data-open]').first().boundingBox()).y<330,'alerts visible high in viewport');}
 await page.screenshot({path:`/workspace/.local/alert-mock-review/a-compact-${theme}-${width}.png`,fullPage:true});
 await page.getByRole('button',{name:'Group by SLO',exact:true}).click();expect(await page.locator('.slo-group').count()===3);expect(await page.locator('.slo-group').first().locator('[data-open]').count()===2);
 await page.screenshot({path:`/workspace/.local/alert-mock-review/a-compact-grouped-${theme}-${width}.png`,fullPage:true});
 await open('all');expect(await page.locator('#filter-panel').isVisible());expect(await page.evaluate(()=>document.activeElement.id)==='filter-alert');
 expect(await page.locator('#filter-panel').evaluate(e=>{const r=e.getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&r.bottom<=innerHeight}));
 if(width===360)await page.screenshot({path:`/workspace/.local/alert-mock-review/a-compact-filters-${theme}-${width}.png`,fullPage:true});
 await page.keyboard.press('Escape');expect(!await page.locator('#filter-panel').isVisible());expect(await page.evaluate(()=>document.activeElement.dataset.filter)==='all','Escape returns focus');
}
await page.setViewportSize({width:1440,height:1000});await page.getByRole('button',{name:'Reset demo',exact:true}).click();
await open('state');await page.screenshot({path:'/workspace/.local/alert-mock-review/a-compact-filter-dark-1440.png',fullPage:true});await page.getByRole('heading',{name:'Alerts',exact:true}).click();expect(!await page.locator('#filter-panel').isVisible());
await set('alert','sustained');expect(await page.locator('[data-open]').count()===2);await set('slo','Checkout availability');expect(await page.locator('[data-open]').getAttribute('data-open')==='checkout-sustained');
await page.getByRole('button',{name:'Group by SLO',exact:true}).click();await set('state','ack');expect(await page.locator('[data-open]').count()===0);expect(await page.getByRole('heading',{name:'No matching alerts'}).isVisible());
await page.getByRole('button',{name:'Clear all',exact:true}).click();expect(await page.locator('[data-open]').count()===4);expect(await page.locator('#group-by').getAttribute('aria-pressed')==='true');
await set('evidence','stale');expect(await page.locator('[data-open]').getAttribute('data-open')==='search');await page.getByRole('button',{name:'Remove Evidence filter'}).click();
await set('severity','Medium');expect(await page.locator('[data-open]').getAttribute('data-open')==='checkout-sustained');await page.getByRole('button',{name:'Clear all',exact:true}).click();
await set('age','30');expect(await page.locator('[data-open]').count()===2);await page.getByRole('button',{name:'Clear all',exact:true}).click();
await page.getByLabel('Search alerts',{exact:true}).fill('Checkout');expect(await page.locator('[data-open]').count()===2);
await page.getByRole('button',{name:'Acknowledge Checkout fast burn',exact:true}).click();expect(page.url().endsWith('a/list'));expect(await page.locator('[data-open=checkout] .badge').textContent()==='Acknowledged');expect(await page.locator('[data-open=checkout-sustained] .badge').textContent()==='Firing');
await page.locator('[data-open=checkout-sustained] .alert-slo').click();await page.waitForURL(/detail\/checkout-sustained$/);await page.locator('.verdict').waitFor();expect(await page.locator('h1').textContent()==='Checkout sustained burn');
await page.getByRole('link',{name:'← Alerts',exact:true}).click();await page.locator('.alert-table').waitFor();await page.getByRole('button',{name:'Reset demo',exact:true}).click();
await page.locator('[data-open=checkout] .alert-age').click({modifiers:['Control']});expect(page.url().endsWith('a/list'));
await set('slo','Checkout availability');await set('alert','fast');await set('state','firing');await page.getByRole('button',{name:'Acknowledge Checkout fast burn',exact:true}).click();expect(await page.locator('[data-open]').count()===0);expect(await page.evaluate(()=>document.activeElement.dataset.filter)==='state');
await set('state','ack');await page.locator('[data-open=checkout] .alert-title').focus();await page.keyboard.press('Enter');await page.waitForURL(/detail\/checkout$/);expect(true);
assert.deepEqual(errors,[]);await browser.close();console.log(JSON.stringify({checks,durationSeconds:(Date.now()-start)/1000,errors}));
})().catch(e=>{console.error(e);process.exit(1)});
