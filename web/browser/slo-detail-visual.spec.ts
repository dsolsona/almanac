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
 await actual.screenshot({path:resolve(output,`disclosures-${width}-${theme}.png`),fullPage:true});const day=actual.locator('.day-strip i').first();await day.dispatchEvent('pointerdown',{pointerType:'touch'});await day.dispatchEvent('pointerleave',{pointerType:'touch'});await expect(actual.locator('.slo-day-inspection')).toBeVisible();await actual.getByRole('heading',{name:'Attainment',exact:true}).click();await expect(actual.locator('.slo-day-inspection')).toHaveCount(0);expect(errors).toEqual([]);
 }finally{await context.close();server.close();}
});

test('SLO disclosures preserve long exact decimal counts on touch screens',async({browser})=>{
 for(const theme of ['light','dark']){
 const context=await browser.newContext({viewport:{width:360,height:900},hasTouch:true,isMobile:true});await context.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),theme);const page=await context.newPage(),detail=sloFixture();
 detail.current.performance={...detail.current.performance,good:'9007199254740993.000000000001',total:'9007199254740994.000000000002',bad:'1.000000000001',allowed_budget:'9007199254740.994000000000',remaining_budget:'9007199254739.993999999999',remaining_fraction:'1.000000000000',attainment:'1.000000000000',consumed_fraction:'0.000000000000'};
 await installSloFixture(page,detail);await page.goto(`/slos/${sloID}`);await page.locator('#calculation summary').click();await expect(page.locator('#calculation')).toContainText('9,007,199,254,740,993.000000000001');
 for(const selector of ['.calculation-inputs','.calculation-equation','.calculation-equation > strong'])for(const row of await page.locator(selector).all())expect(await row.evaluate(el=>el.scrollWidth<=el.clientWidth)).toBe(true);
 const graph=page.locator('.slo-history-chart.budget');await graph.tap();await expect(page.locator('.slo-point-tooltip')).toBeVisible();expect(await page.locator('.slo-point-tooltip').evaluate(el=>getComputedStyle(el).position)).toBe('static');await page.getByRole('heading',{name:'Your error budget'}).tap();await expect(page.locator('.slo-point-tooltip')).toHaveCount(0);await context.close();
 }
});
