import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { request } from 'node:https';
import { join } from 'node:path';
import { captureTelemetryDelivery } from './capture-telemetry-delivery.mjs';
import { browserTransportReady } from './browser-readiness.mjs';
let browser, page, fixture, stage='input';
const wire=[];
const wireRequests=new Map();
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
  await browserTransportReady(browser,fixture.url);
  const operationalRequests=new Map();
  const context=await browser.newContext({viewport:{width:1440,height:900},colorScheme:'light'}); page=await context.newPage();
  await page.addInitScript(()=>{
    const original=globalThis.fetch;
    globalThis.fetch=(input,init)=>{
      const url=new URL(typeof input==='string'?input:input instanceof URL?input.href:input.url,location.href);
      const observe=url.origin===location.origin&&url.pathname==='/api/v1/slos/overview'&&['10','20'].includes(url.searchParams.get('limit'));
      const observed=observe?{settled:false,search:url.search}:null;
      if(observe)globalThis.__almanacRefreshResponse=observed;
      return original(input,init).then(response=>{
        if(observe){
          const copy=response.clone();
          void (async()=>{
            const reader=copy.body.getReader(),chunks=[];let size=0;
            try{
              for(;;){const item=await reader.read();if(item.done)break;size+=item.value.byteLength;if(size>20*1024*1024){void reader.cancel().catch(()=>{});throw new Error('response exceeds bound');}chunks.push(item.value);}
              const bytes=new Uint8Array(size);let offset=0;for(const chunk of chunks){bytes.set(chunk,offset);offset+=chunk.byteLength;}
              Object.assign(observed,{settled:true,status:response.status,body:JSON.parse(new TextDecoder().decode(bytes))});
            }catch{Object.assign(observed,{settled:true,failed:true});}
            finally{reader.releaseLock();}
          })();
        }
        return response;
      });
    };
  });
  const telemetryDeliveries=await captureTelemetryDelivery(page);
  const journeyTraces=[];
  await page.exposeFunction('recordAlmanacJourney',value=>{if(journeyTraces.length<256)journeyTraces.push({...value,stage});});
  await page.addInitScript(()=>addEventListener('almanac:journey',event=>{const value=event.detail;if(value&&/^[0-9a-f]{32}$/.test(value.traceId)&&Number.isFinite(value.duration))void window.recordAlmanacJourney({trace_id:value.traceId,journey:value.journey,result:value.result,duration_ms:value.duration});}));
  page.on('request',request=>{
    const url=new URL(request.url());
    if(request.method()!=='GET'||url.origin!==origin.origin||!/^\/api\/v1\/(slos|services)(\/|$)/.test(url.pathname)||wire.length>=512)return;
    const entry={path:url.pathname,stage,started_ms:Date.now()};wire.push(entry);wireRequests.set(request,entry);
  });
  page.on('response',response=>{
    const entry=wireRequests.get(response.request());if(!entry)return;
    Object.assign(entry,{status:response.status(),elapsed_ms:Date.now()-entry.started_ms,retry_after:response.headers()['retry-after']??null,date:response.headers().date??null});
    if(response.status()>=400)void response.json().then(body=>{entry.code=body.code??null;entry.preparation=body.preparation?.state??null;}).catch(()=>{entry.problem_unreadable=true;});
  });
  page.on('requestfailed',request=>{const entry=wireRequests.get(request);if(entry){entry.failed=true;const error=request.failure()?.errorText??'';entry.transport_error=/^net::ERR_[A-Z0-9_]+$/.test(error)?error:'unclassified';}});
  const preparationReads=[];
  const assertOverviewMatchesResponse=async()=>{
    stage='filtered overview matching response';
    await page.waitForFunction(()=>{
      const state=globalThis.__almanacRefreshResponse;
      if(!state?.settled)return false;
      const requested=new URLSearchParams(state.search),current=new URLSearchParams(location.search);
      return ['q','service_id','urgency','cursor'].every(key=>(requested.get(key)??'')===(current.get(key)??''));
    }).catch(async error=>{
      const observed=await page.evaluate(()=>{
        const state=globalThis.__almanacRefreshResponse;
        const requested=new URLSearchParams(state?.search??''),current=new URLSearchParams(location.search);
        return {settled:state?.settled,failed:state?.failed,status:state?.status,fields:['q','service_id','urgency','cursor'].map(key=>({key,same:(requested.get(key)??'')===(current.get(key)??''),requested_present:requested.has(key),current_present:current.has(key)}))};
      });
      throw new Error(`${error.name}: filtered response observation ${JSON.stringify(observed)}`);
    });
    const observed=await page.evaluate(()=>globalThis.__almanacRefreshResponse);
    assert.equal(observed.failed,undefined,'browser could not consume the filtered overview response');assert.equal(observed.status,200);
    const urgency=new URLSearchParams(new URL(page.url()).search).get('urgency');
    const rows=observed.body.items.filter(item=>!urgency||item.urgency===urgency).map(item=>`/slos/${item.slo.id}`).sort();
    stage='filtered overview matching rows';
    await expect.poll(()=>page.locator('.app-slo-name>a.app-slo-hitarea').evaluateAll(links=>links.map(link=>new URL(link.href).pathname).sort())).toEqual(rows);
    const count=urgency?observed.body.group_counts[urgency]:Object.values(observed.body.group_counts).reduce((sum,value)=>sum+value,0);
    stage='filtered overview matching count';
    await expect(page.locator('.app-slo-list-sub')).toHaveText(`${count} SLO${count===1?'':'s'} · grouped by urgency · UTC`);
    return observed.body;
  };
  const openPreparedOverview=async()=>{
    const isOverview=response=>response.request().method()==='GET'&&new URL(response.url()).pathname==='/api/v1/slos/overview';
    const first=page.waitForResponse(isOverview);
    const started=Date.now();
    await page.getByRole('link',{name:'SLOs',exact:true}).click();
    await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible();
    let response=await first,pending=0;
    // This fixture has just created objectives. Preparation has a ten-second
    // currentness contract; ready rendering is checked separately below.
    const deadline=started+10000;
    while(response.status()===503){
      const problem=await response.json();
      assert.equal(problem.code,'derived_data_pending');
      assert.equal(response.headers()['retry-after'],'2');
      assert.equal(problem.preparation?.state,'pending');
      assert.equal(problem.preparation?.retry_after_seconds,2);
      pending++;
      await expect(page.getByLabel('Search SLOs')).toBeEditable();
      await expect(page.getByRole('button',{name:'New SLO',exact:true})).toBeEnabled();
      const remaining=deadline-Date.now();assert.ok(remaining>0,'initial current preparation exceeded ten seconds');
      response=await page.waitForResponse(isOverview,{timeout:remaining});
    }
    assert.equal(response.status(),200);
    const ready=Date.now();
    await expect(page.getByRole('table').first()).toBeVisible({timeout:1000});
    await writeFile(join(fixture.artifacts,'slo-initial-preparation.json'),JSON.stringify({pending_responses:pending,preparation_ms:ready-started,ready_render_ms:Date.now()-ready})+'\n',{mode:0o600});
  };
  const readPreparedJSON=async path=>{
    let confirmed;
    await expect.poll(async()=>{
      const result=await page.evaluate(async path=>{
        const response=await fetch(path,{credentials:'same-origin',cache:'no-store'});
        return {status:response.status,retryAfter:response.headers.get('Retry-After'),body:await response.json()};
      },path);
      preparationReads.push({path:new URL(path,origin).pathname,status:result.status,code:result.body?.code??null,retry_after:result.retryAfter});
      await writeFile(join(fixture.artifacts,'preparation-observations.json'),JSON.stringify(preparationReads)+'\n',{mode:0o600});
      if(result.status===503){
        assert.equal(result.body.code,'derived_data_pending');
        assert.equal(result.retryAfter,'2');
        assert.equal(result.body.preparation?.state,'pending');
        assert.equal(result.body.preparation?.retry_after_seconds,2);
      } else {assert.equal(result.status,200);confirmed=result;}
      return result.status;
    },{intervals:[2000]}).toBe(200);
    return confirmed;
  };
  page.on('request',request=>{const url=new URL(request.url());if(/^\/api\/v1\/slos\/[0-9a-f-]+\/operational$/.test(url.pathname))operationalRequests.set(url.pathname.split('/')[4],url.href);});
  const capture = async (name, chart = false) => {
    await page.evaluate(()=>window.scrollTo(0,0));
    if(chart)await expect(page.getByText('Current status could not refresh. Showing the last confirmed evaluation.',{exact:true})).toHaveCount(0);
    // Publication can move the fixture out of the selected urgency group.
    if(name.startsWith('slo-list-')&&page.viewportSize().width<=680&&await page.locator('.app-slo-row').count()===0){
      await expect(page.getByText('No SLOs match these filters.',{exact:true})).toBeVisible();
    }else if(name.startsWith('slo-list-')&&page.viewportSize().width<=680){
      await expect.poll(()=>page.locator('.app-slo-row').first().evaluate(row=>{
        const objective=row.querySelector('.app-slo-name').getBoundingClientRect();
        const status=row.querySelector('.app-slo-status').getBoundingClientRect();
        return status.bottom<=objective.top+1;
      })).toBe(true);
    }
    await expect.poll(() => page.evaluate((requireChart) => {
      const plot=document.querySelector('.timeline-chart__plot');
      const svg=plot?.querySelector('svg');
      return document.documentElement.scrollWidth <= window.innerWidth &&
        (!requireChart || !!plot && !!svg && svg.getBoundingClientRect().width <= plot.getBoundingClientRect().width + 1);
    }, chart)).toBe(true);
    await page.screenshot({path:join(fixture.artifacts,name),fullPage:true,animations:'disabled'});
  };
  // Visual evidence is separate from the API assertions: compare the actual
  // screen against the maintainer's supplied image, with no mocked app data.
  const referenceCaptures={};
  const captureReference = async (screen,selector,reference) => {
    await page.setViewportSize({width:2048,height:1280});
    await page.evaluate(()=>window.scrollTo(0,0));
    await page.evaluate(()=>document.fonts.ready);
    // The reference fills the available workspace. A generic 1240px shell cap
    // previously escaped the 1440px checks and narrowed both screens.
    await expect(page.locator('.app-main')).toHaveCSS('width',/17\d\dpx/);
    const actual=await page.locator(selector).first().screenshot({animations:'disabled'});
    const expected=await readFile(new URL(`../../docs/design/references/slo-tracker/${reference}`,import.meta.url));
    referenceCaptures[screen]={actual:actual.toString('base64'),expected:expected.toString('base64')};
    await writeFile(join(fixture.artifacts,`reference-${screen}-actual.png`),actual,{mode:0o600});
    await page.setViewportSize({width:1440,height:900});
  };
  const writeReferenceReport = async () => {
    const sections=Object.entries(referenceCaptures).map(([name,{actual,expected}])=>`<section><h2>${name}</h2><div class="comparison"><figure><figcaption>Supplied screenshot</figcaption><img src="data:image/png;base64,${expected}"></figure><figure><figcaption>Almanac · real acceptance data</figcaption><img src="data:image/png;base64,${actual}"></figure></div></section>`).join('');
    await writeFile(join(fixture.artifacts,'visual-comparison.html'),`<!doctype html><meta charset="utf-8"><title>SLO visual comparison</title><style>body{font:16px system-ui;margin:24px;background:#eee}h1{font-size:24px}.comparison{display:grid;grid-template-columns:1fr 1fr;gap:16px}figure{margin:0;min-width:0}figcaption{padding:12px;background:white}img{width:100%;display:block}section{margin:32px 0}</style><h1>SLO reference comparison</h1><p>Actual pages use real API/database data. Content, observation history and browser scale differ. Compare layout and controls; these captures do not establish pixel identity.</p>${sections}`,{mode:0o600});
  };
  const checkFormAlignment = async () => {
    for (const [left,right] of [['Display name','SLO identifier'],['Target (%)','Rolling window']]) {
      await expect.poll(async () => {
        const first=await page.getByLabel(left,{exact:true}).boundingBox();
        const second=await page.getByLabel(right,{exact:true}).boundingBox();
        return first && second ? Math.max(Math.abs(first.y-second.y),Math.abs(first.height-second.height)) : Infinity;
      }).toBeLessThanOrEqual(1);
    }
    await expect.poll(async()=>{
      const target=await page.getByRole('button',{name:'99%',exact:true}).locator('..').boundingBox();
      const window=await page.getByRole('button',{name:'7d',exact:true}).locator('..').boundingBox();
      return target&&window?Math.abs(target.y+target.height-window.y-window.height):Infinity;
    }).toBeLessThanOrEqual(1);
  };
  const checkOverviewToolbarAlignment = async () => {
    await expect.poll(async()=>{
      const search=await page.locator('.app-slo-search').boundingBox(),button=await page.getByRole('button',{name:'New SLO',exact:true}).boundingBox();
      return search&&button?Math.abs(search.y+search.height/2-button.y-button.height/2):Infinity;
    }).toBeLessThanOrEqual(1);
  };
  const checkOperationalLaneAlignment = async () => {
    await expect.poll(()=>page.evaluate(()=>{
      const labels=[...document.querySelectorAll('.app-lane-label')],plot=document.querySelector('.timeline-chart__plot');
      if(labels.length!==4||!plot)return Infinity;
      const heights=[162,202,202,142],bounds=plot.getBoundingClientRect();
      let top=bounds.top;
      return Math.max(...labels.map((label,index)=>{const box=label.getBoundingClientRect(),difference=Math.max(Math.abs(box.top-top),Math.abs(box.height-heights[index]));top+=heights[index];return difference;}));
    })).toBeLessThanOrEqual(1);
  };
  const checkIntermediatePlotWidth = async () => {
    await expect.poll(()=>page.locator('.timeline-chart__plot').evaluate(plot=>plot.getBoundingClientRect().width)).toBeGreaterThanOrEqual(250);
  };
  const checkMobileLaneCards = async () => {
    await expect(page.locator('.app-timeline-body')).toHaveCSS('grid-template-columns',/110px /);
    await checkOperationalLaneAlignment();
  };
  const checkReferenceLabelBounds = async () => {
    await expect.poll(() => page.evaluate(() => {
      const plot=document.querySelector('.timeline-chart__plot');
      if(!plot)return Infinity;
      const plotBounds=plot.getBoundingClientRect();
      const texts=[...plot.querySelectorAll('svg text')],targets=texts.filter(text=>text.textContent?.trim()==='Target 99.9%'),burn=texts.find(text=>text.textContent?.trim()==='1×');
      if(targets.length!==1||!burn)return Infinity;
      const labels=[...targets,burn];
      return Math.max(...labels.map(label=>{
        const bounds=label.getBoundingClientRect();
        return Math.max(plotBounds.left-bounds.left,bounds.right-plotBounds.right,plotBounds.top-bounds.top,bounds.bottom-plotBounds.bottom,0);
      }));
    })).toBeLessThanOrEqual(1);
  };
  stage='login'; await page.goto(fixture.url); await page.getByLabel('Login',{exact:true}).fill(fixture.login); await page.getByLabel('Password',{exact:true}).fill(fixture.password); await page.getByLabel('Password',{exact:true}).press('Enter'); fixture.password=''; await expect(page.getByRole('heading',{name:'Agents',exact:true})).toBeVisible();
  stage='create'; await openPreparedOverview(); await page.getByLabel('Search SLOs').fill('no-objective-matches-this-query'); await page.getByLabel('Search SLOs').press('Enter'); await expect(page.getByText('No SLOs match these filters.',{exact:true})).toBeVisible(); await expect(page.getByRole('table')).toHaveCount(0); await expect(page.getByRole('navigation',{name:'SLO result pages'})).toHaveCount(0); await page.getByLabel('Search SLOs').fill(''); await page.getByLabel('Search SLOs').press('Enter'); const createStarted=performance.now(); await page.getByRole('button',{name:'New SLO',exact:true}).click(); await expect(page.getByLabel('Display name',{exact:true})).toBeEditable(); const formReadyMs=performance.now()-createStarted;
  await page.getByRole('button',{name:'Create service',exact:true}).click(); const choicesReadyMs=performance.now()-createStarted; await writeFile(join(fixture.artifacts,'slo-create-timing.json'),JSON.stringify({form_ready_ms:formReadyMs,choices_ready_ms:choicesReadyMs})+'\n',{mode:0o600}); await page.getByLabel('Service display name').fill('Checkout'); await page.getByRole('button',{name:'Create service',exact:true}).click();
  await page.getByLabel('Display name',{exact:true}).fill('Checkout availability'); await page.getByLabel('SLI',{exact:true}).selectOption(fixture.sli_id); await expect(page.getByText('Uses measurements already collected for this SLI. Missing intervals remain unknown.',{exact:true})).toBeVisible(); await expect(page.getByRole('button',{name:'Retry loading history',exact:true})).toHaveCount(0); await expect(page.getByText(/A day is 24 hours/)).toHaveCount(0);
  stage='presets'; for(const value of ['99','99.5','99.9','99.95','99.99']) {const preset=page.getByRole('button',{name:`${value}%`,exact:true}); await preset.click(); await expect(preset).toHaveAttribute('aria-pressed','true'); await expect(page.getByLabel('Target (%)')).toHaveValue(value);} for(const value of ['7d','28d','30d','90d']) {const preset=page.getByRole('button',{name:value,exact:true}); await preset.click(); await expect(preset).toHaveAttribute('aria-pressed','true');} await page.getByRole('button',{name:'99.9%',exact:true}).click(); await page.getByRole('button',{name:'30d',exact:true}).click();
  await page.getByLabel('Target (%)').focus(); await expect(page.getByLabel('Target (%)')).toBeFocused();
  for(const value of ['99%','99.5%','99.9%','99.95%','99.99%']) { await page.keyboard.press('Tab'); await expect(page.getByRole('button',{name:value,exact:true})).toBeFocused(); }
  await page.keyboard.press('Tab'); await expect(page.getByRole('textbox',{name:'Rolling window',exact:true})).toBeFocused();
  await page.keyboard.press('Tab'); await expect(page.getByLabel('Window unit',{exact:true})).toBeFocused();
  for(const value of ['7d','28d','30d','90d']) { await page.keyboard.press('Tab'); await expect(page.getByRole('button',{name:value,exact:true})).toBeFocused(); }
  for(const value of ['30d','28d','7d']) { await page.keyboard.press('Shift+Tab'); await expect(page.getByRole('button',{name:value,exact:true})).toBeFocused(); }
  await page.keyboard.press('Shift+Tab'); await expect(page.getByLabel('Window unit',{exact:true})).toBeFocused();
  await page.keyboard.press('Shift+Tab'); await expect(page.getByRole('textbox',{name:'Rolling window',exact:true})).toBeFocused();
  for(const value of ['99.99%','99.95%','99.9%','99.5%','99%']) { await page.keyboard.press('Shift+Tab'); await expect(page.getByRole('button',{name:value,exact:true})).toBeFocused(); }
  await page.keyboard.press('Shift+Tab'); await expect(page.getByLabel('Target (%)')).toBeFocused();
  await checkFormAlignment(); await capture('slo-form-light-desktop.png'); await page.setViewportSize({width:1024,height:900}); await checkFormAlignment(); await capture('slo-form-light-intermediate.png');
  await page.setViewportSize({width:360,height:844}); await capture('slo-form-light-mobile.png');
  await page.setViewportSize({width:1440,height:900}); await page.getByRole('button',{name:'Switch to dark theme'}).click();
  await checkFormAlignment(); await capture('slo-form-dark-desktop.png');
  await page.setViewportSize({width:1024,height:900}); await checkFormAlignment(); await capture('slo-form-dark-intermediate.png');
  await page.setViewportSize({width:360,height:844}); await capture('slo-form-dark-mobile.png');
  await page.setViewportSize({width:1440,height:900}); await page.getByRole('button',{name:'Switch to light theme'}).click();
  await page.getByLabel('Target (%)').fill('100'); await page.getByLabel('Target (%)').press('Enter'); await expect(page.getByRole('alert')).toContainText('not including 100%'); await expect(page.getByLabel('Display name',{exact:true})).toHaveValue('Checkout availability');
  await page.getByLabel('Target (%)').fill('99.9');
  stage='normal create without retrospective history'; const normalPost=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/v1/slos'); await page.getByRole('button',{name:'Create SLO',exact:true}).click(); const normalResponse=await normalPost; assert.equal(normalResponse.status(),201); const normalSlo=await normalResponse.json(); assert.ok(normalSlo.evaluation_valid_from); assert.equal(normalSlo.historical_requested_by,null);
  await expect.poll(()=>operationalRequests.get(normalSlo.id)).toBeTruthy(); const normalDetail=await readPreparedJSON(operationalRequests.get(normalSlo.id)); assert.equal(normalDetail.status,200,'new non-retrospective objective should open operational detail without failing on pre-validity measurements'); assert.ok(normalDetail.body.current.quality); await expect(page.getByText('This evaluation does not apply to the selected range.',{exact:true})).toHaveCount(0); if(normalDetail.body.current.quality.state==='not_applicable'&&Date.parse(normalDetail.body.current.window.end)<Date.parse(normalDetail.body.current.window.evaluation_valid_from))await expect(page.getByText('Waiting for the first SLO evaluation.',{exact:true})).toBeVisible(); if(normalDetail.body.current.performance.remaining_fraction===null)await expect(page.locator('.app-budget-hero')).toHaveText('—');
  await page.getByRole('link',{name:/^(SLOs|← All SLOs)$/}).first().click(); await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible(); await page.getByRole('button',{name:'New SLO',exact:true}).click();
  await page.getByLabel('Display name',{exact:true}).fill('Checkout availability historical'); await page.getByLabel('Service',{exact:true}).selectOption(normalSlo.service_id);
  await page.getByLabel('SLI',{exact:true}).selectOption(normalSlo.sli_id); await page.getByLabel('Target (%)').fill('99.9'); await page.getByRole('button',{name:'30d',exact:true}).click(); await page.getByLabel('Apply this target to earlier SLI measurements').check();
  stage='lost historical create response'; let original, committed, forwardingFailed=false;
  await page.route('**/api/v1/slos',async route=>{try {const incoming=route.request(); original={body:incoming.postData(),key:incoming.headers()['idempotency-key']}; const response=await forwardMutation(route); assert.equal(response.status,201); committed=JSON.parse(response.body.toString('utf8'));} catch {forwardingFailed=true;} finally {await route.abort('failed');}},{times:1});
  await page.getByRole('button',{name:'Create SLO',exact:true}).click(); await expect(page.getByRole('button',{name:'Retry same SLO request'})).toBeVisible(); assert.equal(forwardingFailed,false,'forwarding the historical create failed'); assert.ok(committed.evaluation_valid_from);
  await page.getByRole('link',{name:/^(SLOs|← All SLOs)$/}).first().click(); await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible();
  await page.evaluate(()=>{const original=Date.now;const now=original();globalThis.restoreAlmanacFixtureClock=()=>{Date.now=original;delete globalThis.restoreAlmanacFixtureClock;};Date.now=()=>now+31*86400*1000;}); await page.getByRole('button',{name:'New SLO',exact:true}).click();
  const replay=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/v1/slos'); await page.getByRole('button',{name:'Retry same SLO request'}).click(); const response=await replay; assert.equal(response.status(),201); assert.equal(response.request().postData(),original.body); assert.equal(response.request().headers()['idempotency-key'],original.key); const slo=await response.json(); assert.equal(slo.id,committed.id); assert.equal(slo.target,'0.999'); assert.ok(slo.evaluation_valid_from);
  await page.evaluate(()=>globalThis.restoreAlmanacFixtureClock());
  stage='overview evidence'; await expect(page.getByRole('heading',{name:'Checkout availability historical',exact:true})).toBeVisible();
  const overview=await readPreparedJSON('/api/v1/slos/overview?limit=50'); assert.equal(overview.status,200); let overviewItem=overview.body.items.find(item=>item.slo.id===slo.id); assert.ok(overviewItem,'created SLO appears in operational overview'); assert.ok(['needs_attention','watch','healthy'].includes(overviewItem.urgency)); assert.ok(overviewItem.leading_reason.code); for(const [burn,quality] of [[overviewItem.burn_1h.performance.burn,overviewItem.burn_1h.quality],[overviewItem.burn_6h.performance.burn,overviewItem.burn_6h.quality]]) { assert.ok(typeof burn==='string'||burn===null); if(burn===null)assert.ok(quality.reason||quality.state!=='complete','unavailable burn has a quality explanation'); }
  stage='current status metadata'; const liveStatus=await readPreparedJSON(`/api/v1/slos/${slo.id}/status`); await writeFile(join(fixture.artifacts,'status-generation.json'),JSON.stringify({generation:liveStatus.body.generation,evaluation_at:liveStatus.body.current.evaluation_at})+'\n',{mode:0o600}); assert.equal(liveStatus.body.generation.evaluated_through,liveStatus.body.current.evaluation_at); assert.match(liveStatus.body.generation.published_at,/Z$/);
  stage='operational detail'; const visibleDays=Math.max(1,Math.min(365,Math.round(slo.rolling_window_seconds/86400))); await expect.poll(()=>{const request=operationalRequests.get(slo.id);if(!request)return 0;const bounds=new URL(request).searchParams;return bounds.get('period')==='window'?visibleDays:0;}).toBe(visibleDays); await expect(page.locator('.app-lane-label')).toHaveCount(4); const result=await readPreparedJSON(operationalRequests.get(slo.id)); assert.equal(result.status,200); assert.equal((Date.parse(result.body.range.effective_end)-Date.parse(result.body.range.effective_start))/86400000,visibleDays); const compliance=result.body.compliance_series.days,complianceSummary=result.body.compliance_summary; await writeFile(join(fixture.artifacts,'compliance-wire.json'),JSON.stringify({objective:{id:result.body.objective.id,definition_revision:result.body.objective.definition_revision,target:result.body.objective.target},range:result.body.range,effective_at:result.body.effective_at,compliance_series:result.body.compliance_series,compliance_summary:result.body.compliance_summary})+'\n',{mode:0o600}); assert.ok(result.body.evaluation_id); assert.ok(result.body.current.performance.attainment!==null); assert.ok(BigInt(result.body.current.performance.total.split('.')[0])>0n); assert.ok(result.body.attainment_series.samples.length>0); assert.ok(result.body.budget_series.samples.length>0); assert.ok(result.body.burn_series.samples.length>0); assert.equal(result.body.burn_1h.window_seconds,3600); assert.equal(result.body.burn_6h.window_seconds,21600); assert.ok(Array.isArray(compliance)&&compliance.length>0); assert.equal(compliance.length,complianceSummary.met_days+complianceSummary.violated_days+complianceSummary.unknown_days+complianceSummary.not_applicable_days); assert.equal(complianceSummary.evaluable_days,complianceSummary.met_days+complianceSummary.violated_days); for(const day of compliance){assert.equal(day.requested_at,day.day_end); assert.ok(Date.parse(day.effective_at)<=Date.parse(day.requested_at)); assert.ok(day.day_end.endsWith('T00:00:00.000Z')||day.day_end.endsWith('T00:00:00Z'));}
  await expect(page.getByRole('heading',{name:'Timeline',exact:true})).toBeVisible(); await expect(page.getByText('Burn 1 h',{exact:true})).toBeVisible(); await expect(page.getByText('Burn 6 h',{exact:true})).toBeVisible(); await expect(page.locator('.app-detail-meta a[href="/services/'+slo.service_id+'"]')).toHaveCount(1); await expect(page.locator('.app-detail-meta a[href="/slis/'+fixture.sli_id+'"]')).toHaveCount(1); await expect(page.locator('.app-detail-meta a[href^="/services/"]')).toHaveCount(1); await expect(page.getByText('View calculation details').locator('..')).not.toHaveAttribute('open','');
  await capture('slo-detail-early-light-desktop.png',true);
  await captureReference('detail','.app-slo-detail','slo-detail.png');
  await expect(page.locator('.app-detail-rail')).toContainText('collection coverage'); await expect(page.locator(`a[href="/datasources/${result.body.context.datasource_id}"]`)).toHaveText(result.body.context.datasource_display_name);
  await writeFile(join(fixture.artifacts,'telemetry-objective.json'),JSON.stringify({checkpoint:'operational_detail_verified',slo_id:slo.id,sli_id:slo.sli_id})+'\n',{mode:0o600});
  stage='filtered overview navigation'; await page.getByRole('link',{name:/^(SLOs|← All SLOs)$/}).first().click(); await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible(); await expect(page.getByRole('link',{name:'Checkout availability historical',exact:true})).toBeVisible(); await expect(page.getByRole('heading',{name:/Needs attention|Watch|Healthy/}).first()).toBeVisible();
  await captureReference('list','.app-slo-group','slo-needs-attention.png'); await writeReferenceReport();
  // Compare the row with its own response: publication can advance while checking detail.
  // Refresh cancels the preceding overview; do not consume that request's headers.
  const precedingRequests=new Set(wireRequests.keys());
  stage='filtered overview Refresh response';
  const precedingJourneys=journeyTraces.length;
  const refreshedOverview=page.waitForResponse(r=>!precedingRequests.has(r.request())&&r.request().method()==='GET'&&new URL(r.url()).pathname==='/api/v1/slos/overview')
    .then(response=>response.status());
  await page.getByRole('button',{name:'Refresh',exact:true}).click();
  assert.equal(await refreshedOverview,200);
  await page.waitForFunction(()=>globalThis.__almanacRefreshResponse?.settled);
  const renderedOverviewResponse=await page.evaluate(()=>globalThis.__almanacRefreshResponse);
  assert.equal(renderedOverviewResponse.failed,undefined,'browser could not consume the actual Refresh response');
  assert.equal(renderedOverviewResponse.status,200);
  await expect.poll(()=>journeyTraces.slice(precedingJourneys).some(value=>value.journey==='slo.list'&&value.result==='ready')).toBe(true);
  stage='filtered overview Refresh values';
  const renderedOverviewItem=renderedOverviewResponse.body.items.find(item=>item.slo.id===slo.id);
  assert.ok(renderedOverviewItem,'created SLO appears in refreshed operational overview');
  overviewItem=renderedOverviewItem;
  const expectedReason=({on_track:'On track',stale_observations:'Observations are stale',no_data:'No evaluable data',expired_history:'History has expired',complete_window_violation:'Complete window is below target',negative_observed_budget:'Observed budget is negative',low_observed_budget:'Less than 20% of budget remains',high_1h_burn:'1 h burn is at least 1×',evaluation_unavailable:'Current evaluation unavailable',not_yet_eligible:'Not yet eligible',zero_traffic:'No traffic',incomplete_coverage:'Coverage is incomplete',short_burn_unavailable:'Short-window burn unavailable',budget_below_half:'Less than half of budget remains'})[renderedOverviewItem.leading_reason.code]??renderedOverviewItem.leading_reason.code;
  const objectiveRow=page.getByRole('row').filter({has:page.getByRole('link',{name:'Checkout availability historical',exact:true})}); await expect.poll(()=>objectiveRow.locator('.slo-reference-status').getAttribute('title')).toContain(expectedReason); await page.getByLabel('Search SLOs').fill('Checkout availability historical'); await page.getByLabel('Search SLOs').press('Enter'); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('q'))).toBe('Checkout availability historical');
  await page.getByText('Filters',{exact:true}).click(); await page.getByLabel('Service',{exact:true}).selectOption(slo.service_id); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('service_id'))).toBe(slo.service_id);
  stage='filtered overview urgency';
  await page.getByLabel('Urgency',{exact:true}).selectOption(overviewItem.urgency);
  stage='filtered overview urgency URL';
  await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('urgency'))).toBe(overviewItem.urgency);
  await assertOverviewMatchesResponse();
  await page.getByText('Filters',{exact:true}).click(); await page.setViewportSize({width:1440,height:900}); await checkOverviewToolbarAlignment();
  for(const theme of ['light','dark']) {
    if(theme==='dark') {await page.setViewportSize({width:1440,height:900});await page.getByRole('button',{name:'Switch to dark theme'}).click();}
    for(const [label,width,height] of [['desktop',1440,900],['intermediate',1024,900],['mobile',360,844]]) {await page.setViewportSize({width,height}); await capture(`slo-list-${theme}-${label}.png`);}
    if(theme==='dark') {await page.setViewportSize({width:1440,height:900});await page.getByRole('button',{name:'Switch to light theme'}).click();}
  }
  const filteredURL=page.url();
  stage='filtered overview reload'; await page.reload();
  await assertOverviewMatchesResponse(); await expect(page).toHaveURL(filteredURL);
  await page.goBack(); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('urgency'))).not.toBe(overviewItem.urgency);
  stage='navigation'; await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible(); await page.getByRole('link',{name:'Checkout availability historical',exact:true}).click(); await expect(page.getByRole('heading',{name:'Checkout availability historical',exact:true})).toBeVisible(); const detail=page.url(); await page.locator('.app-detail-meta a[href="/services/'+slo.service_id+'"]').click(); await expect(page).toHaveURL(/\/services\//); await page.goBack(); await expect(page).toHaveURL(detail); await page.locator('a[href="/slis/'+fixture.sli_id+'"]').first().click(); await expect(page).toHaveURL(/\/slis\//); await page.goBack(); await expect(page).toHaveURL(detail); await page.reload(); await expect(page.getByRole('heading',{name:'Checkout availability historical',exact:true})).toBeVisible(); await expect(page.locator('.app-lane-label').nth(0).locator('h3')).toHaveText('SLI'); await expect(page.getByText('Burn 1 h',{exact:true})).toBeVisible();
  stage='layout-chart'; await expect(page.locator('.timeline-chart__plot svg')).toBeVisible();
  stage='layout-controls'; await expect(page.locator('.app-lane-label')).toHaveCount(4); await expect(page.locator('.app-lane-label').nth(0)).toContainText('SLI'); await expect(page.locator('.app-lane-label').nth(1)).toContainText('Budget remaining'); await expect(page.locator('.app-lane-label').nth(2)).toContainText('Burn rate'); await expect(page.locator('.app-lane-label').nth(3)).toContainText('Compliance'); await expect(page.locator('.app-budget-caption')).toHaveText('error budget remaining');
  stage='layout-alignment'; await checkOperationalLaneAlignment();
  stage='layout-target-label'; await expect.poll(async()=>page.locator('.timeline-chart__plot svg text').allTextContents()).toContain('Target 99.9%');
  stage='layout-burn-label'; await expect.poll(async()=>page.locator('.timeline-chart__plot svg text').allTextContents()).toContain('1×');
  stage='layout-daily-target-label'; await expect.poll(async()=>page.locator('.timeline-chart__plot svg text').allTextContents()).toContain('99.9%');
  stage='layout-reference-bounds'; await checkReferenceLabelBounds();
  stage='exact-attainment-table'; const firstComplianceDate=compliance[0].utc_date; const firstLane=page.locator('.app-lane-label').first(),exact=firstLane.getByRole('button',{name:'View SLI as table'}); await expect(exact).toHaveAccessibleName('View SLI as table'); const exactTarget=await exact.getAttribute('aria-controls'); await expect(page.locator(`#${exactTarget}`)).toBeHidden(); await exact.focus(); await page.keyboard.press('Enter'); await expect(exact).toHaveAttribute('aria-expanded','true'); await expect(page.locator(`#${exactTarget}`)).toBeVisible(); await expect(page.getByRole('heading',{name:'SLI exact values'})).toBeVisible(); await expect.poll(async()=>{const table=await page.locator(`#${exactTarget}`).boundingBox(),following=await page.locator('.app-lane-label').nth(1).boundingBox();return table&&following?table.y+table.height-following.y:Infinity;}).toBeLessThanOrEqual(1); await expect(page.getByRole('region',{name:'Exact timeline values'})).toBeVisible(); await expect(page.locator('.app-timeline-table thead th')).toHaveText(['Time (UTC)','Value','Good','Total','Samples','Coverage']); await capture('slo-inline-table-light-desktop.png',true); await page.getByRole('button',{name:'Close table',exact:true}).click(); await expect(exact).toHaveAttribute('aria-expanded','false'); await expect(exact).toBeFocused();
  stage='exact-compliance-table'; const dailyLaneControl=page.locator('.app-lane-label').nth(3),complianceTable=dailyLaneControl.getByRole('button'); await expect(complianceTable).toHaveAccessibleName('View Compliance as table'); await complianceTable.focus(); await page.keyboard.press('Enter'); await expect(complianceTable).toHaveAttribute('aria-expanded','true'); const complianceTarget=await complianceTable.getAttribute('aria-controls'); await expect(page.locator(`#${complianceTarget}`)).toBeVisible(); await expect(page.getByRole('heading',{name:'Compliance exact values'})).toBeVisible(); const dailyStatusLabel=({met:'Met',violated:'Violated',unknown:'Unknown',not_applicable:'Not applicable'})[compliance[0].status];  await expect(page.locator('.app-timeline-table tbody tr').first().locator('td').nth(1)).toContainText(`${dailyStatusLabel}${compliance[0].performance.provisional?' · provisional':''}`); await expect(page.locator('.app-timeline-table tbody tr').first().locator('td').nth(6)).toHaveAttribute('title',compliance[0].effective_at); const utcFirstComplianceCell=await page.locator('.app-timeline-table tbody tr').first().locator('td').first().innerText(); assert.equal(Date.parse(utcFirstComplianceCell),Date.parse(firstComplianceDate)); await complianceTable.click(); await expect(complianceTable).toHaveAttribute('aria-expanded','false');
  stage='utc-local-table-stability'; await page.getByRole('button',{name:'Local',exact:true}).click(); await complianceTable.click(); await expect(complianceTable).toHaveAttribute('aria-expanded','true'); await expect(page.getByRole('columnheader',{name:'Evaluated at (local)',exact:true})).toBeVisible(); const localFirstComplianceCell=await page.locator('.app-timeline-table tbody tr').first().locator('td').first().innerText(); assert.equal(Date.parse(localFirstComplianceCell),Date.parse(firstComplianceDate)); await complianceTable.click(); await expect(complianceTable).toHaveAttribute('aria-expanded','false');
  stage='range-controls'; await page.getByRole('button',{name:'90 d',exact:true}).click(); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('range'))).toBe('90d'); await page.getByRole('group',{name:'Burn window',exact:true}).getByRole('button',{name:'6 h',exact:true}).click(); await expect.poll(()=>page.evaluate(()=>{const q=new URLSearchParams(location.search);return [q.get('range'),q.get('tz'),q.get('burn_window')].join('|');})).toBe('90d|local|21600'); await page.reload(); await expect(page.getByRole('button',{name:'90 d',exact:true})).toHaveAttribute('aria-pressed','true'); await expect(page.getByRole('button',{name:'Local',exact:true})).toHaveAttribute('aria-pressed','true'); await expect(page.getByRole('group',{name:'Burn window',exact:true}).getByRole('button',{name:'6 h',exact:true})).toHaveAttribute('aria-pressed','true'); await page.goBack(); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('burn_window'))).not.toBe('21600'); await expect(page.getByRole('button',{name:'Refresh objective history'})).not.toHaveAttribute('aria-busy','true'); await expect(page.locator('.app-lane-label').nth(2).locator('h3')).toHaveText('Burn rate');
  for(const theme of ['light','dark']) { if(theme==='dark') { await page.setViewportSize({width:1440,height:900}); await page.getByRole('button',{name:'Switch to dark theme'}).click(); } for(const [label,width,height] of [['desktop',1440,900],['intermediate',1024,900],['mobile',360,844]]) {await page.setViewportSize({width,height}); if(width===1024)await checkIntermediatePlotWidth(); if(width===360)await checkMobileLaneCards(); await capture(`slo-${theme}-${label}.png`,true); if(width>700)await checkOperationalLaneAlignment(); } }
  await writeFile(join(fixture.artifacts,'slo-journey.json'),JSON.stringify({slo_id:slo.id,service_id:slo.service_id,sli_id:slo.sli_id,overview:overviewItem,operational:result.body})+'\n',{mode:0o600});
  if(fixture.trace_control_url!==undefined) {
  stage='trace exporter outage';
  const controlURL=new URL(fixture.trace_control_url);
  assert.equal(controlURL.protocol,'http:'); assert.equal(controlURL.hostname,'127.0.0.1'); assert.match(fixture.trace_control_key,/^[0-9a-f]{48}$/);
  const control=async(path,method='GET')=>{
    const response=await fetch(new URL('/control/'+path,controlURL),{method,headers:{'X-Fixture-Control':fixture.trace_control_key},signal:AbortSignal.timeout(2000),redirect:'error'});
    assert.equal(response.status,method==='POST'?204:200,'isolated trace controller failed');
    return method==='POST'?null:response.json();
  };
  const outageStarted=performance.now();
  await control('outage','POST');
  let outageProgress;
  try {
    await page.setViewportSize({width:1440,height:900});
    await page.getByRole('link',{name:'SLIs',exact:true}).click();
    await expect(page.getByRole('heading',{name:'SLIs',exact:true})).toBeVisible();
    await expect(page.locator(`a[href="/slis/${fixture.sli_id}"]`).first()).toBeVisible();
    await openPreparedOverview();
    await page.locator(`a[href="/slos/${slo.id}"]`).first().click();
    await expect(page.getByRole('heading',{name:'Checkout availability historical',exact:true})).toBeVisible();
    await expect(page.locator('.app-lane-label')).toHaveCount(4);
    await expect(page.locator('.app-budget-hero')).toHaveText('-900%');
    await expect(page.locator('.app-lane-label').first()).toContainText('99%');
    await expect.poll(async()=>{
      outageProgress=await control('progress');
      assert.equal(outageProgress.invalid_values,0,'collection changed while trace export was unavailable');
      assert.equal(outageProgress.interval_gaps,0,'collection skipped an interval while trace export was unavailable');
      return outageProgress.new_observations>0&&outageProgress.sink_failures>0&&outageProgress.backend_export_failures>0&&outageProgress.browser_export_failures>0;
    },{timeout:20_000,intervals:[250]}).toBe(true);
  } finally { await control('recover','POST'); }
  await writeFile(join(fixture.artifacts,'trace-outage.json'),JSON.stringify({elapsed_ms:performance.now()-outageStarted,...outageProgress})+'\n',{mode:0o600});
  }
  const telemetryStats=await page.evaluate(()=>new Promise(resolve=>{
    const timeout=setTimeout(()=>resolve({result:'timeout'}),3000);
    addEventListener('almanac:telemetry-flushed',event=>{clearTimeout(timeout);resolve(event.detail);},{once:true});
    dispatchEvent(new Event('almanac:telemetry-flush'));
  }));
  await writeFile(join(fixture.artifacts,'telemetry-stats.json'),JSON.stringify(telemetryStats)+'\n',{mode:0o600});
  stage='delayed create after sign-out'; await page.setViewportSize({width:1440,height:900}); await page.getByRole('link',{name:/^(SLOs|← All SLOs)$/}).first().click(); await page.getByRole('button',{name:'New SLO',exact:true}).click(); await page.getByLabel('Display name',{exact:true}).fill('Delayed objective'); await page.getByLabel('Service',{exact:true}).selectOption(slo.service_id); await page.getByLabel('SLI',{exact:true}).selectOption(fixture.sli_id); await page.getByLabel('Apply this target to earlier SLI measurements').uncheck();
  let release, received=false; const held=new Promise(resolve=>{release=resolve;});
  await page.route('**/api/v1/slos',async route=>{try {const delayed=await forwardMutation(route); assert.equal(delayed.status,201); received=true; await held; await route.fulfill(delayed);} catch {forwardingFailed=true; await route.abort('failed');}},{times:1});
  try {
    await page.getByRole('button',{name:'Create SLO',exact:true}).click(); await expect.poll(()=>received||forwardingFailed).toBe(true); assert.equal(forwardingFailed,false,'forwarding the delayed create failed'); await page.getByRole('button',{name:'Sign out',exact:true}).click(); await expect(page.getByLabel('Login',{exact:true})).toBeVisible(); const signedOutURL=page.url(); const delayedResponse=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/v1/slos'); release(); const delivered=await delayedResponse; assert.equal(await delivered.finished(),null); await page.evaluate(()=>new Promise(resolve=>setTimeout(resolve,0))); assert.equal(page.url(),signedOutURL); await expect(page.getByLabel('Login',{exact:true})).toBeVisible();
  } finally { release(); }
  await writeFile(join(fixture.artifacts,'slo-network.json'),JSON.stringify(wire)+'\n',{mode:0o600});
  assert.ok(journeyTraces.some(value=>value.journey==='slo.list'&&value.result==='ready'),'missing rendered SLO list trace');
  assert.ok(journeyTraces.some(value=>value.journey==='slo.detail'&&value.result==='ready'),'missing rendered SLO detail trace');
  await writeFile(join(fixture.artifacts,'journey-traces.json'),JSON.stringify(journeyTraces)+'\n',{mode:0o600});
  await writeFile(join(fixture.artifacts,'telemetry-deliveries.json'),JSON.stringify(telemetryDeliveries)+'\n',{mode:0o600});
  await context.close(); await browser.close(); console.log('PASS browser-slo-journey');
} catch(error) {
  let reason=String(error instanceof Error?error.message:error);
  for(const secret of [fixture?.login,fixture?.password,fixture?.trace_control_key])if(typeof secret==='string'&&secret)reason=reason.replaceAll(secret,'[redacted]');
  if(fixture?.artifacts&&stage.startsWith('filtered overview')){
    const diagnostics=await page?.evaluate(()=>{
      const state=globalThis.__almanacRefreshResponse,current=new URLSearchParams(location.search),requested=new URLSearchParams(state?.search??'');
      return {overview:{settled:state?.settled,failed:state?.failed,status:state?.status},filters_open:document.querySelector('.app-slo-filters')?.open,
        filters:['q','service_id','urgency','cursor'].map(key=>({key,same:(requested.get(key)??'')===(current.get(key)??''),requested_present:requested.has(key),current_present:current.has(key)})),
        groups:state?.body?.group_counts,items:state?.body?.items?.map(item=>({id:item.slo.id,urgency:item.urgency})),
        rows:Array.from(document.querySelectorAll('.app-slo-name>a.app-slo-hitarea')).map(link=>new URL(link.href).pathname),
        alerts:Array.from(document.querySelectorAll('[role="alert"]')).map(node=>node.textContent?.slice(0,1000))};
    }).catch(()=>null);
    if(diagnostics)await writeFile(join(fixture.artifacts,'refresh-cancellation.json'),JSON.stringify(diagnostics)+'\n',{mode:0o600}).catch(()=>{});
  }
  if(fixture?.artifacts)await writeFile(join(fixture.artifacts,'failure-location.json'),JSON.stringify({locations:String(error?.stack??'').split('\n').filter(line=>line.includes('check-slo-browser.mjs:')).slice(0,8)})+'\n',{mode:0o600}).catch(()=>{});
  await page?.screenshot({path:fixture?.artifacts?join(fixture.artifacts,'slo-failure.png'):undefined,fullPage:true}).catch(()=>{});
  if(fixture?.artifacts)await writeFile(join(fixture.artifacts,'slo-network.json'),JSON.stringify(wire)+'\n',{mode:0o600}).catch(()=>{});
  if(fixture?.artifacts)await writeFile(join(fixture.artifacts,'failure-stage.txt'),stage+'\n',{mode:0o600}).catch(()=>{});
  if(fixture?.artifacts)await writeFile(join(fixture.artifacts,'failure-reason.txt'),reason.slice(0,4000),{mode:0o600}).catch(()=>{});
  await browser?.close().catch(()=>{}); console.error(`FAIL browser-slo-journey stage=${stage} ${reason}`); process.exitCode=1;
}
