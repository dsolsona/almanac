const {resolve}=require('node:path');
const evidence=resolve(__dirname,'../../.local');
const {chromium}=require('playwright');
const {mkdirSync}=require('node:fs');
const assert=require('node:assert/strict');
(async()=>{const started=Date.now(),browser=await chromium.launch(),page=await browser.newPage();page.setDefaultTimeout(7000);const base='http://127.0.0.1:5199/maintenance-preview.html';const errors=[],failed=[];page.on('pageerror',e=>errors.push(e.message));page.on('response',r=>{if(r.status()>=400)failed.push(r.status()+' '+r.url())});let checks=0;const check=(v,m)=>{assert(v,m);checks++};mkdirSync(`${evidence}/maintenance-review-v2`,{recursive:true});
for(const theme of ['light','dark'])for(const width of [1440,1024,360]){await page.setViewportSize({width,height:1000});for(const route of ['list','new','detail','slo','service','alerts']){await page.goto(`${base}?theme=${theme}#${route}`);await page.locator('h1').waitFor();await page.evaluate(()=>document.fonts.ready);check(await page.locator('h1').count()===1,route+' heading');check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),route+' viewport '+width);if(width!==1024)await page.screenshot({path:`${evidence}/maintenance-review-v2/${route}-${theme}-${width}.png`,fullPage:true});}}
await page.setViewportSize({width:1440,height:1000});await page.goto(base+'#new');const picker=page.getByRole('combobox',{name:'Services',exact:true});await picker.focus();check(await page.locator('#maintenance-scope-listbox [role=option]').count()===8,'bounded 50-service result list');check(await page.locator('input[type=checkbox]').count()===0,'no checkbox catalogue');await picker.fill('Store experience');check(await page.locator('#maintenance-scope-listbox [role=option]').count()===2,'owner search');await page.locator('#maintenance-scope-listbox [role=option]').filter({hasText:'Checkout'}).click();check(await page.getByRole('button',{name:'Remove Checkout',exact:true}).count()===1,'selected token');await picker.fill('Payments');await page.keyboard.press('ArrowDown');await page.keyboard.press('Enter');check(await page.getByRole('button',{name:'Remove Payments',exact:true}).count()===1,'keyboard selection');await page.screenshot({path:`${evidence}/maintenance-review-v2/selected-services.png`,fullPage:true});await picker.fill('Workflow');check(await page.locator('#maintenance-scope-listbox [role=option]').count()===1,'50th service discoverable');await page.screenshot({path:`${evidence}/maintenance-review-v2/search-last-service.png`,fullPage:true});await page.keyboard.press('Escape');await page.getByRole('button',{name:'View SLOs',exact:true}).click();check(await page.getByRole('dialog',{name:'Affected SLOs',exact:true}).isVisible(),'impact preview');check(await page.locator('.scope-results li').count()===5,'5 affected SLOs');await page.screenshot({path:`${evidence}/maintenance-review-v2/affected-slos.png`,fullPage:true});await page.keyboard.press('Escape');check(await page.getByRole('button',{name:'View SLOs',exact:true}).evaluate(el=>el===document.activeElement),'focus return');await page.getByRole('button',{name:'Remove Payments',exact:true}).click();check(await page.getByRole('button',{name:'Remove Payments',exact:true}).count()===0,'remove token');await page.getByRole('radio',{name:'All services',exact:true}).click();check((await page.locator('.all-scope').textContent()).includes('50 services'),'all-service summary');await page.getByRole('button',{name:'View SLOs',exact:true}).click();check(await page.locator('.scope-results li').count()===10,'impact pagination bounded');await page.getByRole('button',{name:'Next ›',exact:true}).click();check((await page.locator('.scope-dialog-pager').textContent()).includes('11–20'),'next page');await page.getByRole('searchbox',{name:'Find an affected SLO',exact:true}).fill('Workflow');check(await page.locator('.scope-results li').count()<=4,'impact search');await page.keyboard.press('Escape');await page.getByRole('button',{name:'Schedule maintenance',exact:true}).click();check(await page.getByText('Give this maintenance a title.',{exact:true}).isVisible(),'title required');await page.getByLabel('Title',{exact:true}).fill('Database fleet upgrade');await page.getByLabel('Reason',{exact:true}).fill('Upgrade the database fleet and verify replicas.');await page.getByRole('radio',{name:'30 min',exact:true}).click();check((await page.locator('.utc-preview').textContent()).includes('02:30'),'duration updates end');await page.getByRole('button',{name:'Start date',exact:false}).click();check(await page.getByRole('dialog',{name:'Choose Start date',exact:true}).isVisible(),'real calendar');await page.keyboard.press('Escape');await page.getByRole('button',{name:'Schedule maintenance',exact:true}).click();await page.waitForURL(/#detail$/);check(await page.locator('h1').textContent()==='Database fleet upgrade','saved window');check(await page.locator('.detail-scope tbody tr').count()===10,'all-service detail bounded');await page.getByRole('button',{name:'Edit maintenance',exact:true}).click();check(await page.getByLabel('Title',{exact:true}).inputValue()==='Database fleet upgrade','edit populated');await page.getByLabel('Title',{exact:true}).fill('Unsaved');await page.getByRole('button',{name:'Cancel',exact:true}).click();check(await page.getByRole('dialog',{name:'Discard maintenance changes?',exact:true}).isVisible(),'discard confirmation');await page.getByRole('button',{name:'Keep editing',exact:true}).click();check(await page.getByLabel('Title',{exact:true}).inputValue()==='Unsaved','draft preserved');
await page.goto(base+'#slo');check(await page.locator('.slo-history-chart').count()===3,'production charts');check(await page.locator('.maintenance-chart-band').count()===3,'aligned bands');await page.getByRole('button',{name:'Inspect maintenance on budget graph',exact:true}).click();check(await page.getByRole('dialog',{name:'Checkout database upgrade',exact:true}).isVisible(),'annotation details');await page.screenshot({path:`${evidence}/maintenance-review-v2/slo-annotation.png`,fullPage:true});await page.keyboard.press('Escape');await page.getByRole('radio',{name:'Including maintenance',exact:true}).click();check((await page.locator('.slo-detail-metrics').textContent()).includes('−98%'),'raw comparison');await page.locator('.slo-history-chart.burn').focus();await page.keyboard.press('Home');check(await page.getByRole('tooltip').isVisible(),'real point inspection');await page.keyboard.press('Escape');await page.getByRole('button',{name:'Review prototype · demo',exact:true}).click();await page.getByLabel('Scenario',{exact:true}).selectOption('excluded');await page.getByRole('button',{name:'Back to prototype',exact:true}).click();check((await page.locator('.slo-detail-metrics').textContent()).includes('Excluded — maintenance'),'excluded state');
// Keep separate creates distinct and use objective counts even when fewer services are covered.
await page.goto(base+'?case=records#new');
for (const title of ['Fleet window one', 'Fleet window two']) {
  await page.getByLabel('Title',{exact:true}).fill(title);
  await page.getByLabel('Reason',{exact:true}).fill('Verify independent maintenance windows.');
  await page.getByRole('radio',{name:'Specific SLOs',exact:true}).click();
  const scope=page.getByRole('combobox',{name:'SLOs',exact:true});
  for (const name of ['Checkout availability','Checkout latency','Checkout success','Payments availability','Payments latency','Catalog availability','Catalog latency','Catalog success','Identity availability','Identity latency','Search availability']) {
    await scope.fill(name);
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Enter');
  }
  await page.getByRole('button',{name:'Schedule maintenance',exact:true}).click();
  await page.waitForURL(/#detail$/);
  const pager=page.getByRole('navigation',{name:'Affected SLO pagination'});
  check((await pager.textContent()).includes('1–10 of 11'),'objective pager counts objectives');
  await pager.getByRole('button',{name:'Next ›',exact:true}).click();
  check(await page.locator('.detail-scope tbody tr').count()===1,'last objective remains accessible');
  await page.getByRole('link',{name:'Maintenance',exact:true}).last().click();
  await page.getByRole('button',{name:'＋ Schedule maintenance',exact:true}).waitFor();
  if (title==='Fleet window one') await page.getByRole('button',{name:'＋ Schedule maintenance',exact:true}).click();
}
for (const title of ['Fleet window one','Fleet window two']) {
  check(await page.getByRole('link',{name:title,exact:true}).isVisible(),'separate window '+title);
}
for (const eventList of [false,true]) {
  await page.getByRole('link',{name:'Search index migration',exact:true}).click();
  await page.getByRole('link',{name:'SLOs',exact:true}).click();
  if (eventList) await page.getByText('Timeline events',{exact:false}).click();
  const link=page.getByRole('link',{name:'Checkout database upgrade',exact:true});
  await (eventList ? link.last() : link.first()).click();
  await page.waitForURL(/#detail$/);
  await page.getByRole('heading',{name:'Checkout database upgrade',exact:true}).waitFor();
  check(await page.getByRole('heading',{name:'Checkout database upgrade',exact:true}).isVisible(),'SLO link selects database window');
  await page.getByRole('link',{name:'Maintenance',exact:true}).last().click();
}
await page.goto(base+'?scenario=pending#slo');
const pendingBand=page.locator('.budget .maintenance-chart-band');
const bandBounds=await pendingBand.boundingBox();
await page.mouse.move(bandBounds.x+bandBounds.width/2,bandBounds.y+bandBounds.height/2);
check((await page.getByRole('tooltip').textContent()).includes('Recalculation pending'),'pending tooltip states unavailable calculation');
check(bandBounds.width<100,'pending keeps the one-hour band');
await page.goto(base+'?case=end#detail');
await page.getByRole('button',{name:'End early…',exact:true}).click();
await page.getByLabel('Reason for this change').fill('Work completed');
await page.getByRole('button',{name:'End maintenance',exact:true}).click();
check((await page.locator('.maintenance-facts').textContent()).includes('02:30'),'early end changes effective bounds');
await page.getByRole('link',{name:'Services',exact:true}).click();
await page.getByRole('button',{name:'View maintenance',exact:true}).click();
await page.waitForURL(/#detail$/);
check((await page.locator('.maintenance-state').textContent()).includes('ended early'),'service link preserves lifecycle revision');
await page.setViewportSize({width:360,height:900});await page.goto(base+'#list');await page.getByRole('button',{name:'Menu',exact:true}).click();check(await page.locator('.app-sidebar.is-open').count()===1,'production mobile drawer');await page.getByRole('link',{name:'Maintenance',exact:true}).click();check(await page.locator('.app-sidebar.is-open').count()===0,'maintenance closes mobile drawer');check(errors.length===0,errors.join('\n'));check(failed.length===0,failed.join('\n'));await browser.close();console.log(JSON.stringify({checks,duration_ms:Date.now()-started,errors,failed,screenshots:'.local/maintenance-review-v2'},null,2));})().catch(e=>{console.error(e);process.exit(1)});
