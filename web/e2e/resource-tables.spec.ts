import {expect,test} from '@playwright/test';
import {api,expectOk,unique} from './support/api.ts';
import {startAgent} from './support/stack.ts';

test('resource lists share the services table, sort all results, and open selectable rows',async({page},testInfo)=>{
 test.setTimeout(120_000);
 const prefix=unique('tables');
 const registered=await expectOk(api(page,'POST','/api/v1/agents',{name:`collector-${prefix}`,display_name:'Table collector'}));
 startAgent(registered.body.token);
 const agentID=registered.body.agent.id;
 await expect.poll(async()=>(await api(page,'GET',`/api/v1/agents/${agentID}`)).body.runtime?.reachability,{timeout:30_000}).toBe('connected');
 const names=Array.from({length:11},(_,i)=>`${prefix}-${String(i).padStart(2,'0')}`);
 const sources:string[]=[];
 for(let i=10;i>=0;i--){
  await expectOk(api(page,'POST','/api/v1/agents',{name:names[i],display_name:names[i]}));
  const source=await expectOk(api(page,'POST','/api/v1/datasources',{
   name:names[i],display_name:names[i],provider:'prometheus',configuration_format:'prometheus-http-v1',endpoint:'http://victoriametrics:8428',
   tls:{insecure_skip_verify:false,ca_pem:null},authentication:{mode:'none',username:null,password_secret:{action:'clear'},token_secret:{action:'clear'}},polling_interval_seconds:i===10?120:15+i,agent_id:agentID,
  }));
  sources[i]=source.body.id;
  await expectOk(api(page,'POST','/api/v1/slis',{name:names[i],display_name:names[i],datasource_id:source.body.id,measurement_kind:'event_counts',component_mode:'interval_count',good_query:'vector(99)',total_query:'vector(100)'}));
 }
 await expectOk(api(page,'POST','/api/v1/services',{name:prefix,display_name:prefix}));
 for(const resource of ['agents','datasources','slis']){
  const started=performance.now();
  await page.goto(`/${resource}`,{waitUntil:'commit'});
  await expect(page.locator('.ui-resource-table tbody tr').first()).toBeVisible();
  const firstVisitMs=performance.now()-started;
  console.info('Resource first-visit timing:',JSON.stringify({resource,milliseconds:firstVisitMs}));
  await testInfo.attach(`${resource}-first-visit`,{body:JSON.stringify({milliseconds:firstVisitMs}),contentType:'application/json'});
  expect(firstVisitMs).toBeLessThan(1000);
  const search=page.getByRole('searchbox');
  await search.fill(prefix+'-');
  const sortLabel=resource==='agents'?'Agent':resource==='datasources'?'Datasource':'SLI';
  const sortStarted=performance.now();
  await page.getByRole('button',{name:sortLabel,exact:true}).click();
  const rows=page.locator('.ui-resource-table tbody tr');
  await expect(rows.first().locator('td').first().locator('a').first()).toHaveText(names[0]!);
  const sortedUsableMs=performance.now()-sortStarted;
  expect(sortedUsableMs).toBeLessThan(1000);
  await testInfo.attach(`${resource}-global-sort`,{body:JSON.stringify({milliseconds:sortedUsableMs}),contentType:'application/json'});
  const pager=page.getByRole('navigation',{name:resource==='agents'?'Agent result pages':resource==='datasources'?'Datasource result pages':'SLI result pages'});
  await pager.getByRole('combobox').selectOption('10');
  await expect(rows).toHaveCount(10);
  await pager.getByRole('button',{name:'Next ›',exact:true}).click();
  await expect(rows.first().locator('td').first().locator('a').first()).toHaveText(names[10]!);
  await page.getByRole('button',{name:sortLabel,exact:true}).click();
  await expect(rows.first().locator('td').first().locator('a').first()).toHaveText(names[10]!);
  // Every visible column supports both directions, including numeric cadence/counts.
  const labels=await page.locator('.ui-resource-table thead button').allTextContents();
  for(const label of labels){
   const name=label.replace(/[↕↑↓]/g,'');
   await page.getByRole('button',{name,exact:true}).click();
   await expect(page.locator('.ui-resource-table th[aria-sort="ascending"]')).toContainText(name);
   await page.getByRole('button',{name,exact:true}).click();
   await expect(page.locator('.ui-resource-table th[aria-sort="descending"]')).toContainText(name);
  }
  if(resource==='datasources'){
   await page.getByRole('button',{name:'Cadence',exact:true}).click();
   await expect(rows.first().locator('[data-label="Cadence"]')).toHaveText('15 s');
   await page.getByRole('button',{name:'Cadence',exact:true}).click();
   await expect(rows.first().locator('[data-label="Cadence"]')).toHaveText('120 s');
  }
  const first=rows.first(),cell=first.locator('td').nth(1),before=page.url();
  await cell.click({modifiers:['Control']});
  expect(page.url()).toBe(before);
  const selectable=cell.locator('.ui-status-chip');
  const selectedText=(await selectable.textContent())!.trim();
  // Measure the rendered text edges; the badge's shape and gap are not text.
  const text=await selectable.evaluate(node=>{
   const box=node.getBoundingClientRect(),shape=node.querySelector('.ui-status-chip__shape')!.getBoundingClientRect(),style=getComputedStyle(node);
   return {left:shape.right+parseFloat(style.columnGap),right:box.right-parseFloat(style.paddingRight)-parseFloat(style.borderRightWidth),y:box.y+box.height/2};
  });
  await page.mouse.move(text.right-1,text.y);
  await page.mouse.down();
  await page.mouse.move(text.left+1,text.y,{steps:12});
  await page.mouse.up();
  expect(await page.evaluate(()=>getSelection()?.toString().trim())).toBe(selectedText);
  expect(page.url()).toBe(before);
  await page.getByRole('heading',{level:1}).click();
  const href=await first.locator('td').first().locator('a').first().getAttribute('href');
  await cell.click();
  await expect(page).toHaveURL(new RegExp(href!.replace(/[?]/g,'\\?')+'$'));
  await page.goBack();
  await expect(page.locator('.ui-resource-table tbody tr').first()).toBeVisible();
  const nameLink=rows.first().locator('td').first().locator('a').first();
  const keyboardHref=await nameLink.getAttribute('href');
  await nameLink.focus(); await page.keyboard.press('Enter');
  await expect(page).toHaveURL(new RegExp(keyboardHref!.replace(/[?]/g,'\\?')+'$'));
  await page.goBack();
  await expect(rows.first()).toBeVisible();
  if(resource==='slis'){
   const sourceLink=rows.first().locator('a[href^="/datasources/"]');
   const sourceHref=await sourceLink.getAttribute('href');
   await sourceLink.click();
   await expect(page).toHaveURL(new RegExp(sourceHref!+'$'));
   await page.goBack();
  }
 }
 // Compare the actual pages with the retained services table in both themes and widths.
 for(const theme of ['light','dark'])for(const width of [1440,1024,360]){
  await page.emulateMedia({colorScheme:theme as 'light'|'dark'});
  await page.evaluate(theme=>localStorage.setItem('almanac-theme',theme),theme);
  await page.setViewportSize({width,height:1000});
  let referenceFont='';
  for(const resource of ['services','agents','datasources','slis']){
   await page.goto(`/${resource}`);
   const table=page.locator(resource==='services'?'.services-table':'.ui-resource-table');
   await expect(table.locator('tbody tr').first()).toBeVisible();
   await expect(page.locator('html')).toHaveCSS('color-scheme',theme);
   expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
   const font=await table.locator('tbody tr td a').first().evaluate(node=>getComputedStyle(node).font);
   if(resource==='services') referenceFont=font; else expect(font).toBe(referenceFont);
   if(width===360 && resource!=='services'){
    await page.getByRole('combobox',{name:'Sort by',exact:true}).selectOption('name');
    await expect(page.getByRole('button',{name:'Ascending',exact:true})).toBeVisible();
    const label=table.locator('tbody tr').first().locator('td').nth(1);
    await expect(label).toHaveCSS('text-align','left');
    expect(await label.evaluate(node=>getComputedStyle(node,'::before').content)).toBe(`"${await label.getAttribute('data-label')}"`);
   }
   await testInfo.attach(`${resource}-${theme}-${width}`,{body:await page.screenshot({fullPage:true}),contentType:'image/png'});
  }
 }
});
