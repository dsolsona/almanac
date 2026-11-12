import { createServer } from 'node:http';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { resolve, dirname } from 'node:path';
import { expect, test } from '@playwright/test';
import { installSloFixture, sloFixture, sloID } from './support/slo-detail-fixture.ts';
test('SLO B matches the accepted composition and supports point inspection', {tag:'@matrix'},async({browser},info)=>{
 const width=info.project.use.viewport!.width,theme=info.project.name.endsWith('dark')?'dark':'light',output=resolve('../.local/slo-parity');await mkdir(output,{recursive:true});
 const root=resolve('..'),server=createServer(async(req,res)=>{const pathname=new URL(req.url??'/','http://localhost').pathname;if(!pathname.startsWith('/docs/design/mocks/')&&!pathname.startsWith('/web/public/fonts/')){res.writeHead(404).end();return;}try{const file=resolve(root,'.'+pathname);if(!file.startsWith(root+'/'))throw Error('Outside reference');res.setHeader('Content-Type',file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.ttf')?'font/ttf':'text/html');res.end(await readFile(file));}catch{res.writeHead(404).end();}});await new Promise<void>(done=>server.listen(0,'127.0.0.1',done));const address=server.address();if(!address||typeof address==='string')throw Error('Reference unavailable');
 const context=await browser.newContext({viewport:{width,height:1000}});await context.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),theme);const actual=await context.newPage(),reference=await context.newPage(),errors:string[]=[];actual.on('pageerror',e=>errors.push(e.message));await installSloFixture(actual);
 try{await actual.goto(`/slos/${sloID}?range=7d`);await expect(actual.locator('.slo-history-chart svg')).toHaveCount(3);await reference.goto(`http://127.0.0.1:${address.port}/docs/design/mocks/slo-detail-options/index.html?theme=${theme}#b`);for(const p of [actual,reference]){await p.evaluate(()=>document.fonts.ready);await p.evaluate(()=>document.documentElement.style.setProperty('--mono','"Liberation Mono", monospace'));}
 await expect(actual.locator('.slo-detail-metrics')).toContainText('18%');await expect(actual.locator('.slo-detail-metrics')).toContainText('99.918%');await expect(actual.locator('.slo-detail-metrics')).toContainText('1,800 events left');await expect(actual.locator('#definition')).not.toHaveAttribute('open','');await expect(actual.locator('#calculation')).not.toHaveAttribute('open','');await expect(actual.getByRole('button',{name:/as table/})).toHaveCount(0);
 expect(await actual.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await actual.screenshot({path:resolve(output,`actual-${width}-${theme}.png`),fullPage:true});await reference.screenshot({path:resolve(output,`reference-${width}-${theme}.png`),fullPage:true});
 const dimensions=await Promise.all([actual.locator('#budget'),reference.locator('.panel').first()].map(el=>el.evaluate(node=>({box:node.getBoundingClientRect().toJSON(),children:[...node.querySelectorAll('svg,strong')].map(el=>({text:el.textContent,box:el.getBoundingClientRect().toJSON()}))}))));await writeFile(resolve(output,`geometry-${width}-${theme}.json`),JSON.stringify(dimensions,null,2));
 for(const kind of ['budget','burn','attainment']){
 const received=actual.locator(`.slo-history-chart.${kind} svg`),expected=reference.locator(`.inspect[data-kind="${kind}"] svg`);
 const boxes=await Promise.all([received,expected].map(el=>el.boundingBox())),origin=Math.ceil(Math.max(...boxes.map(b=>b!.y)))+80;
 for(const el of [received,expected])await el.evaluate((node,origin)=>{const box=node.getBoundingClientRect(),frame=document.createElement('div');frame.style.cssText=`display:flow-root;position:relative;width:${box.width}px;background:var(--surface);transform:translateZ(0)`;node.before(frame);frame.append(node);frame.style.top=`${origin-node.getBoundingClientRect().y}px`;},origin);
 const expectedImage=await expected.screenshot(),actualImage=await received.screenshot();
 const name=`slo-${kind}-${width}-${theme}.png`,path=info.snapshotPath(name);await mkdir(dirname(path),{recursive:true});await writeFile(path,expectedImage);await writeFile(resolve(output,`chart-${kind}-${width}-${theme}.png`),actualImage);
 expect.soft(actualImage).toMatchSnapshot(name,{maxDiffPixels:0});
 for(const el of [received,expected])await el.evaluate(node=>node.parentElement!.replaceWith(node));
 }
 if(width===360){const ticks=await actual.locator('.slo-history-chart.budget svg text').evaluateAll(nodes=>nodes.slice(-3).map(el=>el.getBoundingClientRect().toJSON()));expect(ticks[1]!.left).toBeGreaterThan(ticks[0]!.right);expect(ticks[2]!.left).toBeGreaterThan(ticks[1]!.right);}
 const graph=actual.getByRole('group',{name:/Inspect Rolling budget/});await graph.focus();await actual.keyboard.press('End');await expect(actual.getByRole('tooltip')).toContainText('18%');await expect(actual.getByRole('tooltip')).toContainText('12 Nov 2026');await actual.keyboard.press('Escape');await expect(actual.getByRole('tooltip')).toHaveCount(0);
 await actual.getByRole('button',{name:'View definition ↓'}).click();await expect(actual.locator('#definition')).toHaveAttribute('open','');await expect(actual.locator('#definition summary')).toBeFocused();await expect(actual.locator('#definition')).toContainText('43,200 / 43,200');await actual.locator('#calculation summary').click();await expect(actual.locator('.calculation-steps li')).toHaveCount(3);await expect(actual.locator('#calculation')).toContainText('10,000 − 8,200');expect(await actual.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await actual.screenshot({path:resolve(output,`disclosures-${width}-${theme}.png`),fullPage:true});const day=actual.locator('.day-strip i').first();await day.dispatchEvent('pointerdown',{pointerType:'touch'});await day.dispatchEvent('pointerleave',{pointerType:'touch'});await expect(actual.locator('.slo-day-inspection')).toBeVisible();await actual.getByRole('heading',{name:'Attainment',exact:true}).click();await expect(actual.locator('.slo-day-inspection')).toHaveCount(0);await reference.locator('#scenario').selectOption('partial');const mockBurn=reference.locator('.inspect[data-kind=burn]');await mockBurn.focus();await reference.keyboard.press('End');await expect(mockBurn.locator('.point-tooltip')).toContainText('Missing · no value');expect(errors).toEqual([]);
 }finally{await context.close();server.close();}
});

test('SLO disclosures preserve long exact decimal counts on touch screens',async({browser})=>{
 for(const theme of ['light','dark']){
 const context=await browser.newContext({viewport:{width:360,height:900},hasTouch:true,isMobile:true});await context.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),theme);const page=await context.newPage(),detail=sloFixture();
 detail.current.performance={...detail.current.performance,good:'9007199254740993.000000000001',total:'9007199254740994.000000000002',bad:'1.000000000001',allowed_budget:'9007199254740.994000000000',remaining_budget:'9007199254739.993999999999',remaining_fraction:'1.000000000000',attainment:'1.000000000000',consumed_fraction:'0.000000000000'};
 await installSloFixture(page,detail);await page.goto(`/slos/${sloID}`);await page.locator('#calculation summary').click();await expect(page.locator('#calculation')).toContainText('9,007,199,254,740,993.000000000001');
 for(const selector of ['.calculation-inputs','.calculation-equation','.calculation-equation > strong'])for(const row of await page.locator(selector).all())expect(await row.evaluate(el=>el.scrollWidth<=el.clientWidth)).toBe(true);
 const graph=page.locator('.slo-history-chart.budget');await graph.tap();await expect(page.locator('.slo-point-tooltip')).toBeVisible();expect(await page.locator('.slo-point-tooltip').evaluate(el=>getComputedStyle(el).position)).toBe('absolute');await page.getByRole('heading',{name:'Your error budget'}).tap();await expect(page.locator('.slo-point-tooltip')).toHaveCount(0);await context.close();
 }
});

test('365 completed days remain visible, scrollable and keyboard inspectable',{tag:'@matrix'},async({browser},info)=>{
 const width=info.project.use.viewport!.width,theme=info.project.name.endsWith('dark')?'dark':'light',context=await browser.newContext({viewport:{width,height:1000}});await context.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),theme);const page=await context.newPage(),detail=sloFixture(),start='2025-11-12T10:00:00Z';detail.range={...detail.range,effective_start:start,requested_start:start,resolution_seconds:86400};detail.objective.activated_at='2025-10-01T00:00:00Z';detail.objective.evaluation_valid_from=detail.objective.activated_at;detail.current.window.activated_at=detail.objective.activated_at;detail.current.window.evaluation_valid_from=detail.objective.activated_at;
 const base=detail.compliance_series.days[0]!;detail.compliance_series.days=Array.from({length:365},(_,i)=>{const first=Date.UTC(2025,10,12)+i*86400000,end=new Date(first+86400000).toISOString();return{...base,utc_date:new Date(first).toISOString().slice(0,10),day_start:new Date(first).toISOString(),day_end:end,requested_at:end,effective_at:end,quality:{...base.quality,first_observation_at:new Date(first-29*86400000).toISOString(),last_observation_at:end,latest_receipt_at:end}};});detail.compliance_summary={...detail.compliance_summary,met_days:365,evaluable_days:365};await installSloFixture(page,detail);
 try{await page.goto(`/slos/${sloID}?range=1y`);const strip=page.getByRole('group',{name:'Inspect completed UTC days'});await expect(strip.locator('i')).toHaveCount(365);expect((await strip.locator('i').first().boundingBox())!.width).toBeGreaterThanOrEqual(8);expect(await strip.evaluate(el=>el.scrollWidth>el.clientWidth)).toBe(true);await strip.focus();await page.keyboard.press('End');await expect(page.locator('.slo-day-inspection')).toContainText('2026-11-11');await expect.poll(()=>strip.evaluate(el=>el.scrollLeft)).toBeGreaterThan(0);expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);}finally{await context.close();}
});

test('dense 30-day budget stays visible during inspection',{tag:'@matrix'},async({page},info)=>{
 const detail=sloFixture(),start='2026-10-13T10:00:00Z',end=detail.range.effective_end,base=detail.budget_series.samples[0]!;
 detail.range={...detail.range,requested_start:start,effective_start:start};
 detail.objective.activated_at='2026-10-01T00:00:00Z';detail.objective.evaluation_valid_from=detail.objective.activated_at;detail.current.window.activated_at=detail.objective.activated_at;detail.current.window.evaluation_valid_from=detail.objective.activated_at;
 const day=detail.compliance_series.days[0]!;detail.compliance_series.days=Array.from({length:30},(_,i)=>{const first=Date.UTC(2026,9,13)+i*86400000,last=new Date(first+86400000).toISOString();return {...day,utc_date:new Date(first).toISOString().slice(0,10),day_start:new Date(first).toISOString(),day_end:last,requested_at:last,effective_at:last};});detail.compliance_summary={...detail.compliance_summary,met_days:30,evaluable_days:30};
 detail.budget_series.samples=Array.from({length:720},(_,i)=>{const time=new Date(Date.parse(start)+i*3600000).toISOString(),missing=i>=350&&i<370;return {...base,evaluation_at:time,bucket_start:time,bucket_end:new Date(Date.parse(time)+3600000).toISOString(),state:missing?'missing':'partial',remaining_fraction:missing?null:String(.9-i/1000),quality:{...base.quality,state:'partial',freshness:'fresh'}};});
 await page.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),info.project.name.endsWith('dark')?'dark':'light');
 await installSloFixture(page,detail);await page.goto(`/slos/${sloID}?range=30d`);
 const chart=page.locator('.slo-history-chart.budget'),svg=chart.locator('svg');await expect(chart.locator('path.qualified-point')).toHaveCount(700);
 expect(await chart.locator('.qualified-point').first().evaluate(el=>el.getBoundingClientRect().width)).toBeLessThan(3);
 const before=await chart.boundingBox();await chart.focus();await page.keyboard.press('End');await expect(chart.getByRole('tooltip')).toContainText('Partial data');
 expect((await chart.boundingBox())!.height).toBe(before!.height);expect((await chart.getByRole('tooltip').boundingBox())!.width).toBeLessThanOrEqual(260);
 await page.keyboard.press('Home');for(let i=0;i<350;i++)await page.keyboard.press('ArrowRight');await expect(chart.getByRole('tooltip')).toContainText('Missing');await expect(chart.locator('.inspection-dot')).toHaveCount(0);
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.screenshot({path:`../.local/slo-density-${info.project.name}.png`,fullPage:true});
});


test('point tooltip stays compact without shifting the graph or covering selection',{tag:'@matrix'},async({page},info)=>{
 await page.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),info.project.name.endsWith('dark')?'dark':'light');await installSloFixture(page);await page.goto(`/slos/${sloID}`);
 const chart=page.locator('.slo-history-chart.budget'),next=page.locator('#burn'),svg=chart.locator('svg');const before=await chart.boundingBox(),nextBefore=await next.evaluate(el=>el.getBoundingClientRect().top+scrollY);
 await chart.focus();
 for(const key of ['Home','End']){
 await page.keyboard.press(key);const tip=chart.getByRole('tooltip');await expect(tip).toBeVisible();const box=(await tip.boundingBox())!,point=(await chart.locator('.inspection-dot').boundingBox())!;
 expect(box.width).toBeLessThanOrEqual(260);expect(box.x).toBeGreaterThanOrEqual(before!.x);expect(box.x+box.width).toBeLessThanOrEqual(before!.x+before!.width);
 expect(box.x+box.width<point.x||box.x>point.x+point.width||box.y+box.height<point.y||box.y>point.y+point.height).toBe(true);
 expect((await chart.boundingBox())!.height).toBe(before!.height);expect(await next.evaluate(el=>el.getBoundingClientRect().top+scrollY)).toBe(nextBefore);
 await chart.screenshot({path:`../.local/slo-tooltip-${info.project.name}-${key}.png`});
 }
 await page.keyboard.press('Escape');await expect(chart.getByRole('tooltip')).toHaveCount(0);
 const bounds=(await svg.boundingBox())!;await page.mouse.move(bounds.x+bounds.width*.45,bounds.y+bounds.height*.5);await expect(chart.getByRole('tooltip')).toBeVisible();expect((await chart.boundingBox())!.height).toBe(before!.height);
 await page.screenshot({path:`../.local/slo-hover-${info.project.name}.png`,fullPage:true});
 await page.mouse.move(0,0);await expect(chart.getByRole('tooltip')).toHaveCount(0);
});
