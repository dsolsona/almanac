import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { request } from 'node:https';
import { join } from 'node:path';
let browser, page, fixture, stage='input';
try {
  let input=''; for await (const chunk of process.stdin) { input+=chunk; if(Buffer.byteLength(input)>16384) throw new Error('input too large'); }
  fixture=JSON.parse(input); input=''; const origin=new URL(fixture.url);
  assert.equal(origin.protocol,'https:'); assert.equal(origin.hostname,'localhost'); assert.match(fixture.spki,/^[A-Za-z0-9+/]{43}=$/); assert.match(fixture.sli_id,/^[0-9a-f-]{36}$/); assert.ok(typeof fixture.certificate==='string' && fixture.certificate.includes('BEGIN CERTIFICATE')); assert.ok(typeof fixture.artifacts==='string' && fixture.artifacts.length<1024);
  const forwardMutation = async route => {
    const incoming=route.request(); const url=new URL(incoming.url());
    assert.equal(url.origin,origin.origin); assert.equal(url.pathname,'/api/v1/slos'); assert.equal(incoming.method(),'POST');
    const headers=await incoming.allHeaders();
    return await new Promise((resolve,reject)=>{
      const upstream=request(url,{method:incoming.method(),headers,ca:fixture.certificate,timeout:5000,agent:false},response=>{
        const chunks=[]; let size=0;
        response.on('data',chunk=>{size+=chunk.length; if(size>1024*1024) response.destroy(new Error('fixture response too large')); else chunks.push(chunk);});
        response.on('end',()=>resolve({status:response.statusCode,headers:Object.fromEntries(Object.entries(response.headers).map(([name,value])=>[name,Array.isArray(value)?value.join(', '):value])),body:Buffer.concat(chunks)}));
        response.on('error',reject);
      });
      upstream.on('timeout',()=>upstream.destroy(new Error('fixture deadline'))); upstream.on('error',reject); upstream.end(incoming.postDataBuffer());
    });
  };
  await mkdir(fixture.artifacts,{recursive:true,mode:0o700});
  const {chromium,expect}=await import('@playwright/test'); browser=await chromium.launch({headless:true,args:[`--ignore-certificate-errors-spki-list=${fixture.spki}`]});
  const context=await browser.newContext({viewport:{width:1440,height:900},colorScheme:'light'}); page=await context.newPage();
  const capture = async (name, chart = false) => {
    await page.evaluate(()=>window.scrollTo(0,0));
    await expect.poll(() => page.evaluate((requireChart) => {
      const plot=document.querySelector('.timeline-chart__plot');
      const svg=plot?.querySelector('svg');
      return document.documentElement.scrollWidth <= window.innerWidth &&
        (!requireChart || !!plot && !!svg && svg.getBoundingClientRect().width <= plot.getBoundingClientRect().width + 1);
    }, chart)).toBe(true);
    await page.screenshot({path:join(fixture.artifacts,name),fullPage:true,animations:'disabled'});
  };
  const checkFormAlignment = async () => {
    for (const [left,right] of [['Display name','SLO identifier'],['SLI','Target (%)']]) {
      await expect.poll(async () => {
        const first=await page.getByLabel(left,{exact:true}).boundingBox();
        const second=await page.getByLabel(right,{exact:true}).boundingBox();
        return first && second ? Math.max(Math.abs(first.y-second.y),Math.abs(first.height-second.height)) : Infinity;
      }).toBeLessThanOrEqual(1);
    }
  };
  stage='login'; await page.goto(fixture.url); await page.getByLabel('Login',{exact:true}).fill(fixture.login); await page.getByLabel('Password',{exact:true}).fill(fixture.password); await page.getByLabel('Password',{exact:true}).press('Enter'); fixture.password=''; await expect(page.getByRole('heading',{name:'Agents',exact:true})).toBeVisible();
  stage='create'; await page.getByRole('link',{name:'SLOs',exact:true}).click(); await page.getByRole('button',{name:'Create SLO',exact:true}).click();
  await page.getByRole('button',{name:'Create service',exact:true}).click(); await page.getByLabel('Service display name').fill('Checkout'); await page.getByRole('button',{name:'Create service',exact:true}).click();
  await page.getByLabel('Display name',{exact:true}).fill('Checkout availability'); await page.getByLabel('SLI',{exact:true}).selectOption(fixture.sli_id);
  await page.getByLabel('Target (%)').focus(); await expect(page.getByLabel('Target (%)')).toBeFocused();
  await page.keyboard.press('Tab'); await expect(page.getByLabel('Rolling window')).toBeFocused();
  await page.keyboard.press('Shift+Tab'); await expect(page.getByLabel('Target (%)')).toBeFocused();
  await checkFormAlignment(); await capture('slo-form-light-desktop.png');
  await page.setViewportSize({width:360,height:844}); await capture('slo-form-light-mobile.png');
  await page.setViewportSize({width:1440,height:900}); await page.getByRole('button',{name:'Switch to dark theme'}).click();
  await checkFormAlignment(); await capture('slo-form-dark-desktop.png');
  await page.setViewportSize({width:360,height:844}); await capture('slo-form-dark-mobile.png');
  await page.setViewportSize({width:1440,height:900}); await page.getByRole('button',{name:'Switch to light theme'}).click();
  await page.getByLabel('Target (%)').fill('100'); await page.getByLabel('Target (%)').press('Enter'); await expect(page.getByRole('alert')).toContainText('not including 100%'); await expect(page.getByLabel('Display name',{exact:true})).toHaveValue('Checkout availability');
  await page.getByLabel('Target (%)').fill('99.9'); await page.getByLabel('Apply this target to earlier SLI measurements').check();
  stage='lost historical create response'; let original, committed, forwardingFailed=false;
  await page.route('**/api/v1/slos',async route=>{try {const incoming=route.request(); original={body:incoming.postData(),key:incoming.headers()['idempotency-key']}; const response=await forwardMutation(route); assert.equal(response.status,201); committed=JSON.parse(response.body.toString('utf8'));} catch {forwardingFailed=true;} finally {await route.abort('failed');}},{times:1});
  await page.getByRole('button',{name:'Create SLO',exact:true}).click(); await expect(page.getByRole('button',{name:'Retry same SLO request'})).toBeVisible(); assert.equal(forwardingFailed,false,'forwarding the historical create failed'); assert.ok(committed.evaluation_valid_from);
  await page.getByRole('link',{name:'SLOs',exact:true}).first().click(); await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible();
  await page.evaluate(()=>{const now=Date.now(); Date.now=()=>now+31*86400*1000;}); await page.getByRole('button',{name:'Create SLO',exact:true}).click();
  const replay=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/v1/slos'); await page.getByRole('button',{name:'Retry same SLO request'}).click(); const response=await replay; assert.equal(response.status(),201); assert.equal(response.request().postData(),original.body); assert.equal(response.request().headers()['idempotency-key'],original.key); const slo=await response.json(); assert.equal(slo.id,committed.id); assert.equal(slo.target,'0.999'); assert.ok(slo.evaluation_valid_from);
  stage='result'; await expect(page.getByRole('heading',{name:'Checkout availability',exact:true})).toBeVisible(); await expect(page.getByText('Measured success',{exact:true})).toBeVisible(); const result=await page.evaluate(async id=>{const r=await fetch(`/api/v1/slos/${id}/evaluation`,{credentials:'same-origin',cache:'no-store'});return {status:r.status,body:await r.json()};},slo.id); assert.equal(result.status,200); assert.ok(result.body.performance.attainment!==null); assert.ok(BigInt(result.body.performance.total.split('.')[0]) > 0n); await expect(page.locator('.slo-metrics')).toContainText('99%');
  stage='navigation'; const detail=page.url(); await page.getByRole('link',{name:'Checkout',exact:true}).click(); await expect(page).toHaveURL(/\/services\//); await page.goBack(); await expect(page).toHaveURL(detail); await page.locator('a[href="/slis/'+fixture.sli_id+'"]').click(); await expect(page).toHaveURL(/\/slis\//); await page.goBack(); await expect(page).toHaveURL(detail); await page.reload(); await expect(page.getByRole('heading',{name:'Checkout availability',exact:true})).toBeVisible(); await expect(page.getByText('Measured success',{exact:true})).toBeVisible(); await expect(page.locator('.slo-metrics')).toContainText('99%');
  stage='layouts'; await expect(page.getByRole('heading',{name:'Success over recent intervals'})).toBeVisible(); await expect(page.locator('.timeline-chart__plot svg')).toBeVisible();
  const exact=page.getByText('View exact values',{exact:true}); await exact.focus(); await page.keyboard.press('Enter'); await expect(page.getByRole('heading',{name:'Exact values'})).toBeVisible(); await page.keyboard.press('Enter');
  for(const theme of ['light','dark']) { if(theme==='dark') { await page.setViewportSize({width:1440,height:900}); await page.getByRole('button',{name:'Switch to dark theme'}).click(); } for(const [label,width,height] of [['desktop',1440,900],['mobile',360,844]]) {await page.setViewportSize({width,height}); await capture(`slo-${theme}-${label}.png`,true); } }
  await writeFile(join(fixture.artifacts,'slo-journey.json'),JSON.stringify({slo_id:slo.id,service_id:slo.service_id,sli_id:slo.sli_id,evaluation:result.body})+'\n',{mode:0o600});
  stage='delayed create after sign-out'; await page.setViewportSize({width:1440,height:900}); await page.getByRole('link',{name:'SLOs',exact:true}).first().click(); await page.getByRole('button',{name:'Create SLO',exact:true}).click(); await page.getByLabel('Display name',{exact:true}).fill('Delayed objective'); await page.getByLabel('Service',{exact:true}).selectOption(slo.service_id); await page.getByLabel('SLI',{exact:true}).selectOption(fixture.sli_id); await page.getByLabel('Apply this target to earlier SLI measurements').uncheck();
  let release, received=false; const held=new Promise(resolve=>{release=resolve;});
  await page.route('**/api/v1/slos',async route=>{try {const delayed=await forwardMutation(route); assert.equal(delayed.status,201); received=true; await held; await route.fulfill(delayed);} catch {forwardingFailed=true; await route.abort('failed');}},{times:1});
  try {
    await page.getByRole('button',{name:'Create SLO',exact:true}).click(); await expect.poll(()=>received||forwardingFailed).toBe(true); assert.equal(forwardingFailed,false,'forwarding the delayed create failed'); await page.getByRole('button',{name:'Sign out',exact:true}).click(); await expect(page.getByLabel('Login',{exact:true})).toBeVisible(); const signedOutURL=page.url(); const delayedResponse=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/v1/slos'); release(); const delivered=await delayedResponse; assert.equal(await delivered.finished(),null); await page.evaluate(()=>new Promise(resolve=>setTimeout(resolve,0))); assert.equal(page.url(),signedOutURL); await expect(page.getByLabel('Login',{exact:true})).toBeVisible();
  } finally { release(); }
  await context.close(); await browser.close(); console.log('PASS browser-slo-journey');
} catch(error) { await page?.screenshot({path:fixture?.artifacts?join(fixture.artifacts,'slo-failure.png'):undefined,fullPage:true}).catch(()=>{}); await browser?.close().catch(()=>{}); console.error(`FAIL browser-slo-journey stage=${stage} ${error instanceof Error?error.message:'unknown error'}`); process.exitCode=1; }
