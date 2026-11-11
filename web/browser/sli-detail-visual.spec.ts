import { createServer } from 'node:http';
import { readFile, mkdir, writeFile } from 'node:fs/promises';
import { resolve, dirname } from 'node:path';
import { expect, test } from '@playwright/test';
import { sli, sliID, history, installSliDetailFixture } from './support/sli-detail-fixture.ts';

test('SLI measurements match approved E and expose exact interval values', {tag:'@matrix'}, async ({browser},testInfo)=>{
  const width=testInfo.project.use.viewport!.width,theme=testInfo.project.name.endsWith('dark')?'dark':'light';
  const root=resolve('..'),output=resolve('../.local/sli-parity');await mkdir(output,{recursive:true});
  const server=createServer(async(req,res)=>{
    const pathname=new URL(req.url??'/','http://localhost').pathname;
    if(!pathname.startsWith('/docs/design/mocks/')&&!pathname.startsWith('/web/public/fonts/')){res.writeHead(404).end();return;}
    try{const file=resolve(root,'.'+pathname);if(!file.startsWith(root+'/'))throw Error('outside reference');const content=await readFile(file);res.setHeader('Content-Type',file.endsWith('.css')?'text/css':file.endsWith('.js')?'text/javascript':file.endsWith('.ttf')?'font/ttf':'text/html');res.end(content);}catch{res.writeHead(404).end();}
  });
  await new Promise<void>(done=>server.listen(0,'127.0.0.1',done));const address=server.address();if(!address||typeof address==='string')throw Error('Reference unavailable');
  const context=await browser.newContext({viewport:{width,height:1000},storageState:{cookies:[],origins:[]}});
  await context.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),theme);
  const actual=await context.newPage(),reference=await context.newPage(),errors:string[]=[];
  actual.on('pageerror',error=>errors.push(error.message));
  await installSliDetailFixture(actual);
  try {
    await actual.goto(`/slis/${sliID}`);await actual.locator('.chart-point').last().waitFor();
    await reference.goto(`http://127.0.0.1:${address.port}/docs/design/mocks/sli-detail-options/refined.html?theme=${theme}`);
    for(const target of [actual,reference]){await target.evaluate(()=>document.fonts.ready);await target.evaluate(()=>document.documentElement.style.setProperty('--mono','"Liberation Mono", monospace'));}
    await expect(actual.locator('.sli-measurement-summary')).toContainText('99.90%');
    await expect(actual.locator('.sli-measurement-summary')).toContainText('99.48%');
    await expect(actual.locator('.sli-measurement-summary')).toContainText('598 unsuccessful / 114,000 total');
    await expect(actual.locator('.interval-state.observed')).toHaveCount(57);await expect(actual.locator('.interval-state.failed')).toHaveCount(2);await expect(actual.locator('.interval-state.missing')).toHaveCount(1);
    await expect(actual.locator('#sli-intervals')).not.toHaveAttribute('open','');
    await expect(actual.locator('.sli-objectives-table tbody tr')).toHaveCount(2);
    await expect(actual.locator('.sli-objectives-table')).toContainText('Partial data');
    expect(await actual.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    const expectedPanel=reference.locator('.refined-chart'),actualPanel=actual.locator('.sli-measurements-panel');
    const geometry=await Promise.all([actualPanel,expectedPanel].map(panel=>panel.evaluate(el=>({box:el.getBoundingClientRect().toJSON(),children:[...el.querySelectorAll('header,dl,svg')].map(node=>({tag:node.tagName,box:node.getBoundingClientRect().toJSON()}))}))));
    await writeFile(resolve(output,`geometry-${width}-${theme}.json`),JSON.stringify(geometry,null,2));
    await actual.screenshot({path:resolve(output,`page-${width}-${theme}.png`),fullPage:true});
    // Align crop origins so unrelated shell/header height cannot alter SVG subpixel rasterization.
    const origin=Math.ceil(Math.max(geometry[0]!.box.y,geometry[1]!.box.y))+80;
    for(const panel of [actualPanel,expectedPanel])await panel.evaluate((el,origin)=>{
      const box=el.getBoundingClientRect(),frame=document.createElement('div');
      frame.dataset.parityFrame='true';frame.style.cssText=`display:flow-root;position:relative;width:${box.width}px;background:var(--bg);z-index:1;transform:translateZ(0)`;
      el.before(frame);frame.append(el);frame.style.top=`${origin-el.getBoundingClientRect().y}px`;
    },origin);
    const expected=await expectedPanel.screenshot(),received=await actualPanel.screenshot();
    await writeFile(resolve(output,`reference-${width}-${theme}.png`),expected);await writeFile(resolve(output,`actual-${width}-${theme}.png`),received);
    const snapshot=`sli-detail-${width}-${theme}.png`,snapshotPath=testInfo.snapshotPath(snapshot);await mkdir(dirname(snapshotPath),{recursive:true});await writeFile(snapshotPath,expected);
    expect.soft(received).toMatchSnapshot(snapshot,{maxDiffPixels:0});
    // Return to ordinary page positions before interaction checks.
    await actualPanel.evaluate(el=>el.parentElement!.replaceWith(el));
    await actual.locator('[data-point="29"]').hover();
    await expect(actual.getByRole('tooltip')).toContainText('92.50%');await expect(actual.getByRole('tooltip')).toContainText('1,850');
    await actual.keyboard.press('Escape');await expect(actual.getByRole('tooltip')).not.toBeVisible();
    await actual.locator('.interval-state.failed').first().hover();await expect(actual.getByRole('tooltip')).toContainText('Query timed out');
    await actual.locator('.interval-state.missing').hover();await expect(actual.getByRole('tooltip')).toContainText('No observation received');
    const chart=actual.getByRole('group',{name:/Interactive measurements/});await chart.focus();await actual.keyboard.press('Home');await actual.keyboard.press('ArrowRight');await expect(actual.getByRole('tooltip')).toContainText('09:01–09:02 UTC');
    await actual.keyboard.press('End');await expect(actual.getByRole('tooltip')).toContainText('99.90%');await actual.keyboard.press('Escape');
    await actual.getByRole('button',{name:'Inspect gaps ↓'}).click();await expect(actual.locator('.sli-intervals-table tbody tr')).toHaveCount(3);
    await actual.getByRole('button',{name:'View queries',exact:true}).click();await expect(actual.locator('#sli-definition')).toHaveAttribute('open','');
    for(const [name,id] of [['Exact values ↓','intervals'],['View queries','definition']]) {
      await reference.getByRole('button',{name,exact:true}).click();
      const summary=reference.locator(`#${id} > summary`);
      await expect(summary).toBeFocused();
      await reference.keyboard.press('Tab');await reference.keyboard.press('Shift+Tab');
      await expect(summary).toBeFocused();
    }
    expect(errors).toEqual([]);
  } finally {await context.close();server.close();}
});

test('SLI point inspection supports touch and keeps long exact counts inside its panel',async({browser})=>{
  for(const theme of ['light','dark']) {
    const context=await browser.newContext({viewport:{width:360,height:900},hasTouch:true,isMobile:true});
    await context.addInitScript(theme=>localStorage.setItem('almanac-theme',theme),theme);
    const page=await context.newPage();
    await installSliDetailFixture(page);
    await page.goto(`/slis/${sliID}`);
    await page.locator('[data-point="29"]').tap();
    await expect(page.getByRole('tooltip')).toContainText('92.50%');
    await page.locator('.interval-state.missing').tap();
    await expect(page.getByRole('tooltip')).toContainText('No observation received');
    await page.getByRole('heading',{name:'Measurements',exact:true}).tap();await expect(page.getByRole('tooltip')).not.toBeVisible();
    const row={...history.items[0]!,good_count:'9007199254740993.000000000001',total_count:'9007199254740994.000000000002'};
    await installSliDetailFixture(page,{...sli,latest_observation:row},{...history,items:[row],inspected_start:row.interval_start,inspected_end:row.interval_end,missing_intervals:[]});
    await page.reload();
    await page.locator('[data-point="0"]').tap();
    await expect(page.getByRole('tooltip')).toContainText('9,007,199,254,740,993.000000000001');
    expect(await page.getByRole('tooltip').evaluate(node=>node.scrollWidth<=node.clientWidth)).toBe(true);
    expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
    await context.close();
  }
});
