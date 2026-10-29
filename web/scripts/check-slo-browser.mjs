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
    if(name.startsWith('slo-list-')&&page.viewportSize().width<=680){
      await expect.poll(()=>page.locator('.slo-operational-row').first().evaluate(row=>{
        const objective=row.querySelector('.slo-operational-row__objective').getBoundingClientRect();
        const status=row.querySelector('.slo-operational-row__status').getBoundingClientRect();
        return objective.bottom<=status.top+1;
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
    await expect.poll(async () => {
      const controls=await Promise.all([
        page.getByLabel('Search SLOs',{exact:true}).boundingBox(),
        page.getByRole('button',{name:'Search',exact:true}).boundingBox(),
        page.getByLabel('Service',{exact:true}).boundingBox(),
        page.getByLabel('Urgency',{exact:true}).boundingBox(),
        page.getByRole('button',{name:'Create SLO',exact:true}).boundingBox(),
        page.getByRole('button',{name:'Refresh',exact:true}).boundingBox(),
      ]);
      if(controls.some(box=>!box))return Infinity;
      return Math.max(...controls.map(box=>Math.abs(box.y-controls[0].y)),...controls.map(box=>Math.abs(box.height-controls[0].height)));
    }).toBeLessThanOrEqual(1);
  };
  const checkOperationalLaneAlignment = async () => {
    await expect.poll(() => page.evaluate(() => {
      const plot=document.querySelector('.timeline-chart__plot');
      const controls=[...document.querySelectorAll('.timeline-chart__lane-control')];
      if(!plot||controls.length!==4)return Infinity;
      const bounds=plot.getBoundingClientRect(),laneHeight=(78-6*3)/4;
      return Math.max(...controls.map((control,index)=>{
        const controlBounds=control.getBoundingClientRect();
        const expected=bounds.top+bounds.height*(6+index*(laneHeight+6)+laneHeight/2)/100;
        return Math.abs((controlBounds.top+controlBounds.height/2)-expected);
      }));
    })).toBeLessThanOrEqual(2);
  };
  const checkIntermediatePlotWidth = async () => {
    await expect.poll(()=>page.locator('.timeline-chart__plot').evaluate(plot=>plot.getBoundingClientRect().width)).toBeGreaterThanOrEqual(250);
  };
  const checkMobileLaneCards = async () => {
    await expect.poll(()=>page.evaluate(()=>{
      const container=document.querySelector('.timeline-chart__lane-controls'),cards=[...document.querySelectorAll('.timeline-chart__lane-control')];
      if(!container||cards.length!==4)return false;
      const outer=container.getBoundingClientRect(),boxes=cards.map(card=>card.getBoundingClientRect());
      return boxes.every(box=>box.left>=outer.left-1&&box.right<=outer.right+1)&&boxes.every((box,index)=>boxes.slice(index+1).every(other=>box.right<=other.left+1||other.right<=box.left+1||box.bottom<=other.top+1||other.bottom<=box.top+1));
    })).toBe(true);
  };
  const checkReferenceLabelBounds = async () => {
    await expect.poll(() => page.evaluate(() => {
      const plot=document.querySelector('.timeline-chart__plot');
      if(!plot)return Infinity;
      const plotBounds=plot.getBoundingClientRect();
      const texts=[...plot.querySelectorAll('svg text')],targets=texts.filter(text=>text.textContent?.trim()==='Target 99.9%'),burn=texts.find(text=>text.textContent?.trim()==='1×');
      if(targets.length!==2||!burn)return Infinity;
      const labels=[...targets,burn];
      return Math.max(...labels.map(label=>{
        const bounds=label.getBoundingClientRect();
        return Math.max(plotBounds.left-bounds.left,bounds.right-plotBounds.right,plotBounds.top-bounds.top,bounds.bottom-plotBounds.bottom,0);
      }));
    })).toBeLessThanOrEqual(1);
  };
  stage='login'; await page.goto(fixture.url); await page.getByLabel('Login',{exact:true}).fill(fixture.login); await page.getByLabel('Password',{exact:true}).fill(fixture.password); await page.getByLabel('Password',{exact:true}).press('Enter'); fixture.password=''; await expect(page.getByRole('heading',{name:'Agents',exact:true})).toBeVisible();
  stage='create'; await page.getByRole('link',{name:'SLOs',exact:true}).click(); await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible(); await expect(page.getByRole('table').first()).toBeVisible(); await page.getByLabel('Search SLOs').fill('no-objective-matches-this-query'); await page.getByRole('button',{name:'Search',exact:true}).click(); await expect(page.getByText('No SLOs match these filters.',{exact:true})).toBeVisible(); await expect(page.getByRole('table')).toHaveCount(0); await expect(page.getByRole('navigation',{name:'SLO result pages'})).toHaveCount(0); await page.getByLabel('Search SLOs').fill(''); await page.getByRole('button',{name:'Search',exact:true}).click(); const createStarted=performance.now(); await page.getByRole('button',{name:'Create SLO',exact:true}).click(); await expect(page.getByLabel('Display name',{exact:true})).toBeEditable(); const formReadyMs=performance.now()-createStarted;
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
  const normalDetail=await page.evaluate(async id=>{const r=await fetch(`/api/v1/slos/${id}/operational`,{credentials:'same-origin',cache:'no-store'});return {status:r.status,body:await r.json()};},normalSlo.id); assert.equal(normalDetail.status,200,'new non-retrospective objective should open operational detail without failing on pre-validity measurements'); assert.ok(normalDetail.body.current.quality); await expect(page.getByText('This evaluation does not apply to the selected range.',{exact:true})).toHaveCount(0); if(normalDetail.body.current.quality.state==='not_applicable'&&Date.parse(normalDetail.body.current.window.end)<Date.parse(normalDetail.body.current.window.evaluation_valid_from))await expect(page.getByText('Waiting for the first SLO evaluation.',{exact:true})).toBeVisible(); if(normalDetail.body.current.performance.remaining_fraction===null)await expect(page.locator('.slo-status-hero')).toHaveClass(/slo-status-hero--unavailable/);
  await page.getByRole('link',{name:'SLOs',exact:true}).first().click(); await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible(); await page.getByRole('button',{name:'Create SLO',exact:true}).click();
  await page.getByLabel('Display name',{exact:true}).fill('Checkout availability historical'); await page.getByLabel('Service',{exact:true}).selectOption(normalSlo.service_id);
  await page.getByLabel('SLI',{exact:true}).selectOption(normalSlo.sli_id); await page.getByLabel('Target (%)').fill('99.9'); await page.getByRole('button',{name:'30d',exact:true}).click(); await page.getByLabel('Apply this target to earlier SLI measurements').check();
  stage='lost historical create response'; let original, committed, forwardingFailed=false;
  await page.route('**/api/v1/slos',async route=>{try {const incoming=route.request(); original={body:incoming.postData(),key:incoming.headers()['idempotency-key']}; const response=await forwardMutation(route); assert.equal(response.status,201); committed=JSON.parse(response.body.toString('utf8'));} catch {forwardingFailed=true;} finally {await route.abort('failed');}},{times:1});
  await page.getByRole('button',{name:'Create SLO',exact:true}).click(); await expect(page.getByRole('button',{name:'Retry same SLO request'})).toBeVisible(); assert.equal(forwardingFailed,false,'forwarding the historical create failed'); assert.ok(committed.evaluation_valid_from);
  await page.getByRole('link',{name:'SLOs',exact:true}).first().click(); await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible();
  await page.evaluate(()=>{const now=Date.now(); Date.now=()=>now+31*86400*1000;}); await page.getByRole('button',{name:'Create SLO',exact:true}).click();
  const replay=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/v1/slos'); await page.getByRole('button',{name:'Retry same SLO request'}).click(); const response=await replay; assert.equal(response.status(),201); assert.equal(response.request().postData(),original.body); assert.equal(response.request().headers()['idempotency-key'],original.key); const slo=await response.json(); assert.equal(slo.id,committed.id); assert.equal(slo.target,'0.999'); assert.ok(slo.evaluation_valid_from);
  stage='overview evidence'; await expect(page.getByRole('heading',{name:'Checkout availability historical',exact:true})).toBeVisible();
  const overview=await page.evaluate(async()=>{const r=await fetch('/api/v1/slos/overview?limit=50',{credentials:'same-origin',cache:'no-store'});return {status:r.status,body:await r.json()};}); assert.equal(overview.status,200); const overviewItem=overview.body.items.find(item=>item.slo.id===slo.id); assert.ok(overviewItem,'created SLO appears in operational overview'); assert.ok(['needs_attention','watch','healthy'].includes(overviewItem.urgency)); assert.ok(overviewItem.leading_reason.code); for(const [burn,quality] of [[overviewItem.burn_1h.performance.burn,overviewItem.burn_1h.quality],[overviewItem.burn_6h.performance.burn,overviewItem.burn_6h.quality]]) { assert.ok(typeof burn==='string'||burn===null); if(burn===null)assert.ok(quality.reason||quality.state!=='complete','unavailable burn has a quality explanation'); }
  stage='operational detail'; const result=await page.evaluate(async id=>{const r=await fetch(`/api/v1/slos/${id}/operational`,{credentials:'same-origin',cache:'no-store'});return {status:r.status,body:await r.json()};},slo.id); assert.equal(result.status,200); const compliance=result.body.compliance_series.days,complianceSummary=result.body.compliance_summary; await writeFile(join(fixture.artifacts,'compliance-wire.json'),JSON.stringify({objective:{id:result.body.objective.id,definition_revision:result.body.objective.definition_revision,target:result.body.objective.target},range:result.body.range,effective_at:result.body.effective_at,compliance_series:result.body.compliance_series,compliance_summary:result.body.compliance_summary})+'\n',{mode:0o600}); assert.ok(result.body.evaluation_id); assert.ok(result.body.current.performance.attainment!==null); assert.ok(BigInt(result.body.current.performance.total.split('.')[0])>0n); assert.ok(result.body.attainment_series.samples.length>0); assert.ok(result.body.budget_series.samples.length>0); assert.ok(result.body.burn_series.samples.length>0); assert.equal(result.body.burn_1h.window_seconds,3600); assert.equal(result.body.burn_6h.window_seconds,21600); assert.ok(Array.isArray(compliance)&&compliance.length>0); assert.equal(compliance.length,complianceSummary.met_days+complianceSummary.violated_days+complianceSummary.unknown_days+complianceSummary.not_applicable_days); assert.equal(complianceSummary.evaluable_days,complianceSummary.met_days+complianceSummary.violated_days); for(const day of compliance){assert.equal(day.requested_at,day.day_end); assert.ok(Date.parse(day.effective_at)<=Date.parse(day.requested_at)); assert.ok(day.day_end.endsWith('T00:00:00.000Z')||day.day_end.endsWith('T00:00:00Z'));}
  await expect(page.getByRole('heading',{name:'Timeline',exact:true})).toBeVisible(); await expect(page.getByText('Burn 1 h',{exact:true})).toBeVisible(); await expect(page.getByText('Burn 6 h',{exact:true})).toBeVisible(); await expect(page.locator('.slo-detail-context a[href="/services/'+slo.service_id+'"]')).toHaveCount(1); await expect(page.locator('.slo-detail-context a[href="/slis/'+fixture.sli_id+'"]')).toHaveCount(1); await expect(page.locator('.slo-detail-heading a[href^="/services/"],.slo-detail-heading a[href^="/slis/"]')).toHaveCount(0); await expect(page.getByText('Calculation and coverage details').locator('..')).not.toHaveAttribute('open','');
  await capture('slo-detail-early-light-desktop.png',true);
  await expect(page.locator('.slo-operational-rail')).toContainText('Collection coverage'); await expect(page.locator(`a[href="/datasources/${result.body.context.datasource_id}"]`)).toHaveText(result.body.context.datasource_display_name);
  stage='filtered overview navigation'; await page.getByRole('link',{name:'SLOs',exact:true}).first().click(); await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible(); await expect(page.getByRole('link',{name:'Checkout availability historical',exact:true})).toBeVisible(); await expect(page.getByRole('heading',{name:/Needs attention|Watch|Healthy/}).first()).toBeVisible();
  const expectedReason=({on_track:'On track',stale_observations:'Observations are stale',no_data:'No evaluable data',expired_history:'History has expired',complete_window_violation:'Complete window is below target',negative_observed_budget:'Observed budget is negative',low_observed_budget:'Less than 20% of budget remains',high_1h_burn:'1 h burn is at least 1×',evaluation_unavailable:'Current evaluation unavailable',not_yet_eligible:'Not yet eligible',zero_traffic:'No traffic',incomplete_coverage:'Coverage is incomplete',short_burn_unavailable:'Short-window burn unavailable',budget_below_half:'Less than half of budget remains'})[overviewItem.leading_reason.code]??overviewItem.leading_reason.code;
  const objectiveRow=page.getByRole('row').filter({has:page.getByRole('link',{name:'Checkout availability historical',exact:true})}); await expect(objectiveRow.getByText(expectedReason,{exact:false})).toBeVisible(); await page.getByLabel('Search SLOs').fill('Checkout availability historical'); await page.getByRole('button',{name:'Search',exact:true}).click(); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('q'))).toBe('Checkout availability historical');
  await page.getByLabel('Service',{exact:true}).selectOption(slo.service_id); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('service_id'))).toBe(slo.service_id);
  await page.getByLabel('Urgency',{exact:true}).selectOption(overviewItem.urgency); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('urgency'))).toBe(overviewItem.urgency); await expect(page.getByRole('link',{name:'Checkout availability historical',exact:true})).toBeVisible(); await page.setViewportSize({width:1440,height:900}); await checkOverviewToolbarAlignment(); for(const theme of ['light','dark']) { if(theme==='dark') {await page.setViewportSize({width:1440,height:900});await page.getByRole('button',{name:'Switch to dark theme'}).click();} for(const [label,width,height] of [['desktop',1440,900],['intermediate',1024,900],['mobile',360,844]]) {await page.setViewportSize({width,height}); await capture(`slo-list-${theme}-${label}.png`);} if(theme==='dark') {await page.setViewportSize({width:1440,height:900});await page.getByRole('button',{name:'Switch to light theme'}).click();} } const filteredURL=page.url(); await page.reload(); await expect(page.getByRole('link',{name:'Checkout availability historical',exact:true})).toBeVisible(); await expect(page).toHaveURL(filteredURL); await page.goBack(); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('urgency'))).not.toBe(overviewItem.urgency);
  stage='navigation'; await expect(page.getByRole('heading',{name:'SLOs',exact:true})).toBeVisible(); await page.getByRole('link',{name:'Checkout availability historical',exact:true}).click(); await expect(page.getByRole('heading',{name:'Checkout availability historical',exact:true})).toBeVisible(); const detail=page.url(); await page.locator('.slo-detail-context a[href="/services/'+slo.service_id+'"]').click(); await expect(page).toHaveURL(/\/services\//); await page.goBack(); await expect(page).toHaveURL(detail); await page.locator('a[href="/slis/'+fixture.sli_id+'"]').first().click(); await expect(page).toHaveURL(/\/slis\//); await page.goBack(); await expect(page).toHaveURL(detail); await page.reload(); await expect(page.getByRole('heading',{name:'Checkout availability historical',exact:true})).toBeVisible(); await expect(page.locator('.timeline-chart__lane-controls .timeline-chart__lane-control').nth(0).locator('strong')).toHaveText('SLI attainment'); await expect(page.getByText('Burn 1 h',{exact:true})).toBeVisible();
  stage='layout-chart'; await expect(page.locator('.timeline-chart__plot svg')).toBeVisible();
  stage='layout-controls'; await expect(page.locator('.timeline-chart__lane-control')).toHaveCount(4); await expect(page.locator('.timeline-chart__lane-control').nth(0)).toContainText('SLI attainment'); await expect(page.locator('.timeline-chart__lane-control').nth(1)).toContainText('Rolling budget remaining'); await expect(page.locator('.timeline-chart__lane-control').nth(2)).toContainText('1 h burn'); await expect(page.locator('.timeline-chart__lane-control').nth(3)).toContainText('Daily compliance'); await expect(page.locator('.slo-status-panel')).toContainText('error budget remaining');
  stage='layout-alignment'; await checkOperationalLaneAlignment();
  stage='layout-target-label'; await expect.poll(async()=>page.locator('.timeline-chart__plot svg text').allTextContents()).toContain('Target 99.9%');
  stage='layout-burn-label'; await expect.poll(async()=>page.locator('.timeline-chart__plot svg text').allTextContents()).toContain('1×');
  stage='layout-daily-target-label'; await expect.poll(async()=>(await page.locator('.timeline-chart__plot svg text').allTextContents()).filter(label=>label.trim()==='Target 99.9%').length).toBe(2);
  stage='layout-reference-bounds'; await checkReferenceLabelBounds();
  stage='exact-attainment-table'; const firstComplianceDate=compliance[0].day_end; const firstLane=page.locator('.timeline-chart__lane-control').first(),exact=firstLane.getByRole('button'); await expect(exact).toHaveAccessibleName('Show SLI attainment exact values'); const exactTarget=await exact.getAttribute('aria-controls'); await expect(page.locator(`#${exactTarget}`)).toBeHidden(); await exact.focus(); await page.keyboard.press('Enter'); await expect(exact).toHaveAttribute('aria-expanded','true'); await expect(page.locator(`#${exactTarget}`)).toBeVisible(); await expect(page.getByRole('heading',{name:'SLI attainment exact values'})).toBeVisible(); await expect.poll(async()=>{const table=await page.locator(`#${exactTarget}`).boundingBox(),following=await page.locator('.timeline-chart__lane-control').nth(1).boundingBox();return table&&following?table.y+table.height-following.y:Infinity;}).toBeLessThanOrEqual(1); await expect(page.getByRole('region',{name:'Exact timeline values'})).toBeVisible(); await expect(page.locator('.exact-values tbody tr').first().locator('td').nth(1)).toHaveText('SLI attainment'); await capture('slo-inline-table-light-desktop.png',true); await page.getByRole('button',{name:'Close table',exact:true}).click(); await expect(exact).toHaveAttribute('aria-expanded','false'); await expect(exact).toBeFocused();
  stage='exact-compliance-table'; const dailyLaneControl=page.locator('.timeline-chart__lane-control').nth(3),complianceTable=dailyLaneControl.getByRole('button'); await expect(complianceTable).toHaveAccessibleName('Show Daily compliance exact values'); await complianceTable.focus(); await page.keyboard.press('Enter'); await expect(complianceTable).toHaveAttribute('aria-expanded','true'); const complianceTarget=await complianceTable.getAttribute('aria-controls'); await expect(page.locator(`#${complianceTarget}`)).toBeVisible(); await expect(page.getByRole('heading',{name:'Daily compliance exact values'})).toBeVisible(); const dailyStatusLabel=({met:'Met',violated:'Violated',unknown:'Unknown',not_applicable:'Not applicable'})[compliance[0].status]; await expect(page.locator('.exact-values tbody tr').first().locator('td').nth(2)).toHaveText(`${dailyStatusLabel}${compliance[0].performance.provisional?' · provisional':''}`); const expectedAttainment=compliance[0].performance.attainment===null?'—':`${Number(compliance[0].performance.attainment)*100} %`; await expect(page.locator('.exact-values tbody tr').first().locator('td').nth(3)).toHaveText(expectedAttainment); await expect(page.locator('.exact-values tbody tr').first().locator('td').last()).toContainText(`requested ${compliance[0].requested_at} · effective ${compliance[0].effective_at}`); const utcFirstComplianceCell=await page.locator('.exact-values tbody tr').first().locator('td').first().innerText(); assert.equal(Date.parse(utcFirstComplianceCell),Date.parse(firstComplianceDate)); await complianceTable.click(); await expect(complianceTable).toHaveAttribute('aria-expanded','false');
  stage='utc-local-table-stability'; await page.getByRole('button',{name:'Local',exact:true}).click(); await complianceTable.click(); await expect(complianceTable).toHaveAttribute('aria-expanded','true'); const localFirstComplianceCell=await page.locator('.exact-values tbody tr').first().locator('td').first().innerText(); assert.equal(Date.parse(localFirstComplianceCell),Date.parse(firstComplianceDate)); await complianceTable.click(); await expect(complianceTable).toHaveAttribute('aria-expanded','false');
  stage='range-controls'; await page.getByRole('button',{name:'90d',exact:true}).click(); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('range'))).toBe('90d'); await page.getByLabel('Burn window').selectOption('21600'); await expect.poll(()=>page.evaluate(()=>{const q=new URLSearchParams(location.search);return [q.get('range'),q.get('tz'),q.get('burn_window')].join('|');})).toBe('90d|local|21600'); await page.reload(); await expect(page.getByRole('button',{name:'90d',exact:true})).toHaveAttribute('aria-pressed','true'); await expect(page.getByRole('button',{name:'Local',exact:true})).toHaveAttribute('aria-pressed','true'); await expect(page.getByLabel('Burn window')).toHaveValue('21600'); await page.goBack(); await expect.poll(()=>page.evaluate(()=>new URLSearchParams(location.search).get('burn_window'))).not.toBe('21600'); await expect(page.getByRole('button',{name:'Refresh timeline'})).not.toHaveAttribute('aria-busy','true'); const selectedBurn=await page.getByLabel('Burn window').inputValue(); await expect(page.locator('.timeline-chart__lane-control').nth(2).locator('strong')).toHaveText(selectedBurn==='21600'?'6 h burn':'1 h burn');
  for(const theme of ['light','dark']) { if(theme==='dark') { await page.setViewportSize({width:1440,height:900}); await page.getByRole('button',{name:'Switch to dark theme'}).click(); } for(const [label,width,height] of [['desktop',1440,900],['intermediate',1024,900],['mobile',360,844]]) {await page.setViewportSize({width,height}); if(width===1024)await checkIntermediatePlotWidth(); if(width===360)await checkMobileLaneCards(); await capture(`slo-${theme}-${label}.png`,true); if(width>700)await checkOperationalLaneAlignment(); } }
  await writeFile(join(fixture.artifacts,'slo-journey.json'),JSON.stringify({slo_id:slo.id,service_id:slo.service_id,sli_id:slo.sli_id,overview:overviewItem,operational:result.body})+'\n',{mode:0o600});
  stage='delayed create after sign-out'; await page.setViewportSize({width:1440,height:900}); await page.getByRole('link',{name:'SLOs',exact:true}).first().click(); await page.getByRole('button',{name:'Create SLO',exact:true}).click(); await page.getByLabel('Display name',{exact:true}).fill('Delayed objective'); await page.getByLabel('Service',{exact:true}).selectOption(slo.service_id); await page.getByLabel('SLI',{exact:true}).selectOption(fixture.sli_id); await page.getByLabel('Apply this target to earlier SLI measurements').uncheck();
  let release, received=false; const held=new Promise(resolve=>{release=resolve;});
  await page.route('**/api/v1/slos',async route=>{try {const delayed=await forwardMutation(route); assert.equal(delayed.status,201); received=true; await held; await route.fulfill(delayed);} catch {forwardingFailed=true; await route.abort('failed');}},{times:1});
  try {
    await page.getByRole('button',{name:'Create SLO',exact:true}).click(); await expect.poll(()=>received||forwardingFailed).toBe(true); assert.equal(forwardingFailed,false,'forwarding the delayed create failed'); await page.getByRole('button',{name:'Sign out',exact:true}).click(); await expect(page.getByLabel('Login',{exact:true})).toBeVisible(); const signedOutURL=page.url(); const delayedResponse=page.waitForResponse(r=>r.request().method()==='POST'&&new URL(r.url()).pathname==='/api/v1/slos'); release(); const delivered=await delayedResponse; assert.equal(await delivered.finished(),null); await page.evaluate(()=>new Promise(resolve=>setTimeout(resolve,0))); assert.equal(page.url(),signedOutURL); await expect(page.getByLabel('Login',{exact:true})).toBeVisible();
  } finally { release(); }
  await context.close(); await browser.close(); console.log('PASS browser-slo-journey');
} catch(error) { await page?.screenshot({path:fixture?.artifacts?join(fixture.artifacts,'slo-failure.png'):undefined,fullPage:true}).catch(()=>{}); if(fixture?.artifacts)await writeFile(join(fixture.artifacts,'failure-stage.txt'),stage+'\n',{mode:0o600}).catch(()=>{}); await browser?.close().catch(()=>{}); console.error(`FAIL browser-slo-journey stage=${stage} ${error instanceof Error?error.message:'unknown error'}`); process.exitCode=1; }
