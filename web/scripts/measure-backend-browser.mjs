import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
import { resolve, sep, join } from 'node:path';
import { chromium } from '@playwright/test';

// This executable belongs to the controlled performance fixture. It is not
// invoked by the ordinary browser catalogue or the two-minute journey gate.
let browser;
let output;
let stage = 'fixture';
const report = {
  schema_version: 1,
  environment: { cpu_slowdown: 4, download_bits_per_second: 10_000_000, latency_ms: 100, viewport: { width: 1440, height: 900 } },
  cold_loads: [], warm_loads: [], lifecycle_loads: [], interactions: [], list_actions: [], list_coverage: {}, failures: [],
};

function distribution(samples, key) {
  const ordered = samples.map(sample => sample[key]).filter(Number.isFinite).sort((a, b) => a - b);
  const rank = percentile => ordered[Math.ceil(percentile * ordered.length) - 1] ?? null;
  return { count: ordered.length, p50_ms: rank(.50), p75_ms: rank(.75), p95_ms: rank(.95), p99_ms: rank(.99), max_ms: ordered.at(-1) ?? null };
}

// Runs before the app. It observes real fetches/DOM changes without supplying or
// altering API responses. Reports retain fixture identities, counts and timings;
// credentials and private request headers never leave the authentication setup.
function installMeasurements({ fleetSize, datasourceIDs, serviceID }) {
  const documents = new Map();
  const requests = new Map();
  let observedBodyReady=()=>{};
  const requestKey=(path,query)=>{const copy=new URLSearchParams(query);copy.sort();return `${path}?${copy}`;};
  const state = window.__almanacPerformance = { fetches: [], primary_ms: null, interaction: null, listOffset: 0, listEvidence: null };
  const originalFetch = window.fetch;
  window.fetch = async function (...args) {
    const input = args[0];
    const url = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url, location.href);
    const started = performance.now();
    const response = await originalFetch.apply(this, args);
    if (url.origin !== location.origin || !url.pathname.startsWith('/api/')) return response;
    const sample = { path: url.pathname, started_ms: started, headers_ms: performance.now(), wall_received_ms:Date.now(),status: response.status, server_date_ms:Date.parse(response.headers.get('Date')) };
    state.fetches.push(sample);
    // Clients parse bounded streams rather than Response.json(). Observe a clone
    // without replacing their reader, validation or telemetry parse spans.
    if(response.status===200&&(url.pathname==='/api/v1/slis'||url.pathname==='/api/v1/slos/overview'||/\/api\/v1\/slos\/[^/]+\/(status|operational)$/.test(url.pathname))){
      const copy=response.clone();
      void (async()=>{
        const reader=copy.body.getReader(),chunks=[];let size=0;
        try{
          for(;;){const {done,value}=await reader.read();if(done)break;size+=value.byteLength;if(size>20*1024*1024){void reader.cancel().catch(()=>{});throw new Error('observed body exceeds bound');}chunks.push(value);}
          const bytes=new Uint8Array(size);let offset=0;for(const chunk of chunks){bytes.set(chunk,offset);offset+=chunk.byteLength;}
          const value=JSON.parse(new TextDecoder().decode(bytes));
          const parsed={ value, query: url.searchParams };
          documents.set(url.pathname, parsed);
          requests.set(requestKey(url.pathname,url.searchParams),parsed);
          sample.observed_body_ms=performance.now();
          observedBodyReady();
        }finally{reader.releaseLock();}
      })().catch(()=>{sample.body_observation_failed=true;});
    }
    return response;
  };
  const visible = element => !!element && element.getBoundingClientRect().width > 0 && element.getBoundingClientRect().height > 0;
  const percent = value => value === null ? '—' : `${(Number(value) * 100).toLocaleString(undefined, { maximumFractionDigits: 3 })}%`;
  const multiple=value=>value===null?'—':`${Number(value).toLocaleString(undefined,{maximumFractionDigits:2})}×`;
  const decimal=value=>value===null?'—':value.replace(/(\.\d*?)0+$/,'$1').replace(/\.$/,'');
  const validGeneration = value => !!value?.generation_id && !!value?.evaluated_through && !!value?.source_generation;
  let namesKey='',namesCache=new Set();
  const expectedNames=(view,params)=>{
    const key=JSON.stringify([view,params.get('q'),params.get('datasource_id'),params.get('service_id')]);
    if(key!==namesKey){
      namesKey=key;namesCache=new Set();
      for(let index=0;index<fleetSize;index++){
        const name=`performance-${String(index).padStart(view==='slis'?4:3,'0')}`;
        if((!params.get('q')||name.includes(params.get('q')))&&(!params.get('datasource_id')||datasourceIDs[Math.floor(index/(fleetSize/10))]===params.get('datasource_id'))&&(!params.get('service_id')||serviceID===params.get('service_id')))namesCache.add(name);
      }
    }
    return namesCache;
  };
  const ready = () => {
    if (document.querySelector('[role="alert"],.slo-error')) return false;
    const params=new URLSearchParams(location.search),pageSize=Number(params.get('page_size')||20);
    if(state.listAction&&(!Number.isFinite(state.listAction.started_ms)||state.listAction.initial_location===location.pathname+location.search))return false;
    const listRequest=path=>{
      const query=new URLSearchParams({limit:String(pageSize)});
      for(const key of ['q','cursor','datasource_id','service_id','urgency'])if(params.get(key))query.set(key,params.get(key));
      return requests.get(requestKey(path,query))?.value;
    };
    if (location.pathname === '/slis') {
      const page = listRequest('/api/v1/slis'),names=expectedNames('slis',params);
      const expectedRows=Math.min(pageSize,names.size-state.listOffset);
      const rows = [...document.querySelectorAll('.sli-list-table tbody tr')];
      const complete=page?.items.length === expectedRows && rows.length === expectedRows && (expectedRows>0||visible(document.querySelector('.sli-empty'))) && page.items.every(item => names.has(item.name)&&rows.some(row => visible(row.querySelector(`a[href="/slis/${item.id}"]`)) && row.querySelectorAll('td').length === 7 && (!item.latest_observation || row.querySelector('time')?.dateTime === item.latest_observation.interval_end)));
      if(complete)state.listEvidence={ids:page.items.map(item=>item.id),names:page.items.map(item=>item.name),items:page.items,expected_total:names.size,next_cursor:page.next_cursor};
      return complete;
    }
    if (location.pathname === '/slos') {
      const page = listRequest('/api/v1/slos/overview'),names=expectedNames('slos',params);
      const rows = [...document.querySelectorAll('.app-slo-row')];
      const count = page && Object.values(page.group_counts).reduce((sum, value) => sum + value, 0);
      const matching=page&&(params.get('urgency')?page.group_counts[params.get('urgency')]:count),expectedRows=Math.min(pageSize,matching-state.listOffset);
      const complete=count===names.size && page.items.length===expectedRows && rows.length===expectedRows && !document.querySelector('.app-slo-rows .slo-reference-spark:not([data-chart-ready="true"])') && (expectedRows>0||visible(document.querySelector('.slo-empty'))) && document.querySelector('.app-slo-list-sub')?.textContent.startsWith(`${matching} SLO`) && page.items.every(item=>names.has(item.slo.name)&&(!params.get('urgency')||item.urgency===params.get('urgency'))&&validGeneration(item.generation)&&rows.some(row=>{
        const budget=item.current.performance.remaining_fraction;
        const budgetText=budget===null?'—':`${(Number(budget)*100).toLocaleString(undefined,{maximumFractionDigits:1})}%`;
        return visible(row.querySelector(`a[href="/slos/${item.slo.id}"]`))&&row.querySelector('.app-slo-budget b')?.textContent===budgetText&&row.querySelector('.app-slo-burn b')?.textContent===multiple(item.burn_1h.performance.burn);
      }));
      if(complete)state.listEvidence={ids:page.items.map(item=>item.slo.id),names:page.items.map(item=>item.slo.name),items:page.items,urgencies:page.items.map(item=>item.urgency),expected_total:matching,counts:page.group_counts,snapshot_id:page.snapshot_id,next_cursor:page.next_cursor,previous_cursor:page.previous_cursor};
      return complete;
    }
    const id = location.pathname.split('/').at(-1);
    const status = documents.get(`/api/v1/slos/${id}/status`)?.value;
    const history = documents.get(`/api/v1/slos/${id}/operational`);
    const burn = params.get('burn_window') === '21600' ? 21600 : 3600;
    const fact=label=>[...document.querySelectorAll('.app-status-facts>div')].find(row=>row.querySelector('dt')?.textContent===label)?.querySelector('dd')?.textContent;
    const statusReady = status?.slo.id === id && validGeneration(status.generation) && status.generation.evaluated_through===status.current.evaluation_at && visible(document.querySelector('.app-status-facts')) && document.querySelector('[aria-label="Refresh objective status"]')?.getAttribute('aria-busy') !== 'true' && document.querySelector('.app-budget-hero')?.textContent === percent(status.current.performance.remaining_fraction)
      &&fact('Attainment')===percent(status.current.performance.attainment)&&fact('Target')===`${Number(status.slo.target)*100}%`&&fact('Burn 1 h')===multiple(status.burn_1h.performance.burn)&&fact('Burn 6 h')===multiple(status.burn_6h.performance.burn)&&fact('Bad events left')===decimal(status.current.performance.remaining_budget);
    const value=history?.value,plot=document.querySelector('.app-timeline-plot .timeline-chart__plot');
    if(!statusReady||value?.objective.id!==id||!validGeneration(value.generation)||value.burn_series.burn_window_seconds!==burn||history.query.get('burn_window_seconds')!==String(burn))return false;
    const windowDays=Math.max(1,Math.min(365,Math.round(value.objective.rolling_window_seconds/86400)));
    const ranges=['7d',`${windowDays}d`,'90d','1y'],range=ranges.includes(params.get('range'))?params.get('range'):`${windowDays}d`;
    const days=range==='1y'?365:Number.parseInt(range,10),cadence=value.objective.cadence_seconds*1000;
    const end=Date.parse(history.query.get('end')),start=Date.parse(history.query.get('start'));
    const rangeReady=Number.isFinite(end)&&end%cadence===0&&start===Math.ceil((end-days*86400000)/cadence)*cadence&&Date.parse(history.query.get('at'))===end&&Date.parse(value.range.requested_start)===start&&Date.parse(value.range.requested_end)===end&&Date.parse(value.range.effective_start)===start&&Date.parse(value.range.effective_end)===end&&Date.parse(value.generation.evaluated_through)===end&&Date.parse(value.effective_at)===end&&value.generation.definition_revision===value.objective.definition_revision&&value.generation.measurement_revision===value.context.measurement_revision;
    const laneValues=[...document.querySelectorAll('.app-lane-value')].map(node=>node.textContent);
    const expected=[percent(value.current.performance.attainment),percent(value.current.performance.remaining_fraction),multiple((burn===3600?value.burn_1h:value.burn_6h).performance.burn),`${value.compliance_summary.met_days} / ${value.compliance_summary.evaluable_days}`];
    const complete=rangeReady&&plot?.dataset.chartReady==='true'&&plot.dataset.renderedGeneration===value.generation.generation_id&&plot.dataset.renderedZone===(params.get('tz')==='local'?'local':'UTC')&&visible(plot.querySelector('svg'))&&laneValues.length===4&&laneValues.every((text,index)=>text===expected[index]);
    if(complete)state.detailEvidence={history_generation:value.generation.generation_id,rendered_generation:plot.dataset.renderedGeneration,history_evaluated_through:value.generation.evaluated_through,status_generation:status.generation.generation_id,status_evaluated_through:status.generation.evaluated_through,range_start:value.range.effective_start,range_end:value.range.effective_end,burn_window_seconds:burn,time_zone:plot.dataset.renderedZone};
    return complete;
  };
  let primaryScheduled = false;
  let primaryGeneration=0;
  state.beginListAction=(offset,event)=>{
    primaryGeneration++;primaryScheduled=false;state.primary_ms=null;state.listOffset=offset;
    state.listAction={started_ms:null,initial_location:location.pathname+location.search};
    document.addEventListener(event,()=>{state.listAction.started_ms=performance.now();},{once:true,capture:true});
  };
  const afterPaint = callback => void document.fonts.ready.then(() => requestAnimationFrame(() => requestAnimationFrame(callback)));
  const observe=mutations => {
    if (state.primary_ms === null && !primaryScheduled && ready()) {
      primaryScheduled = true;
      const generation=primaryGeneration;
      afterPaint(() => { if(generation!==primaryGeneration)return;if (ready()) state.primary_ms = performance.now(); else primaryScheduled = false; });
    }
    const interaction = state.interaction;
    if (!interaction?.started_ms) return;
    if (interaction.feedback_ms === null && interaction.button.getAttribute('aria-pressed') === 'true' && !interaction.feedback_scheduled) {
      interaction.feedback_scheduled = true;
      afterPaint(() => { interaction.feedback_ms = performance.now() - interaction.started_ms; });
    }
    if (mutations.some(mutation => mutation.target instanceof Element && (mutation.target.closest('.timeline-chart__plot') || mutation.target.matches('.app-timeline-plot')))) {
      interaction.plot_mutated = true;
    }
    const refreshing = [...document.querySelectorAll('.app-timeline [role="status"]')].some(node => node.textContent === 'Refreshing operational evaluation');
    if (!interaction.update_scheduled && interaction.plot_mutated && interaction.button.getAttribute('aria-pressed') === 'true' && !refreshing && ready()) {
      interaction.update_scheduled = true;
      afterPaint(() => {
        const parsed = state.fetches.filter(sample => sample.path.endsWith('/operational') && sample.status === 200 && sample.started_ms >= interaction.started_ms).at(-1);
        interaction.finished_ms = performance.now();
        // Headers precede client decoding: this conservatively includes body
        // transfer/parse rather than understating rendering with observer timing.
        interaction.chart_ms = interaction.finished_ms - (parsed?.headers_ms ?? interaction.started_ms);
        interaction.chart_timing_origin='response_headers_upper_bound';
        interaction.network_backed = !!parsed;
      });
    }
  };
  observedBodyReady=()=>observe([]);
  new MutationObserver(observe).observe(document, { subtree: true, childList: true, attributes: true, characterData: true });
}

async function measuredPage(context, fixture) {
  await context.addInitScript(installMeasurements, { fleetSize:fixture.fleet_size,datasourceIDs:fixture.datasource_ids,serviceID:fixture.service_id });
  const page = await context.newPage();
  const cdp = await context.newCDPSession(page);
  await cdp.send('Network.enable');
  await cdp.send('Network.setCacheDisabled', { cacheDisabled: true });
  await cdp.send('Network.emulateNetworkConditions', { offline: false, latency: 100, downloadThroughput: 1_250_000, uploadThroughput: 1_250_000 });
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: 4 });
  return page;
}

async function browserTimings(page) {
  return page.evaluate(() => ({
    primary_ms: window.__almanacPerformance.primary_ms,
    detail:window.__almanacPerformance.detailEvidence,
    fetches: window.__almanacPerformance.fetches,
    resources: performance.getEntriesByType('resource').map(entry => ({
      path: new URL(entry.name).pathname, initiator: entry.initiatorType,
      started_ms: entry.startTime, response_started_ms: entry.responseStart, response_ended_ms: entry.responseEnd,
      transfer_bytes: entry.transferSize, encoded_bytes: entry.encodedBodySize, decoded_bytes: entry.decodedBodySize,
    })),
    navigation: performance.getEntriesByType('navigation').map(entry => ({
      response_started_ms: entry.responseStart, response_ended_ms: entry.responseEnd,
      dom_interactive_ms: entry.domInteractive, dom_content_loaded_ms: entry.domContentLoadedEventEnd,
    })),
  }));
}

try {
  let input = '';
  for await (const chunk of process.stdin) {
    input += chunk;
    assert.ok(Buffer.byteLength(input) <= 16 * 1024);
  }
  const fixture = JSON.parse(input);
  input = '';
  const origin = new URL(fixture.url);
  assert.equal(origin.protocol, 'https:');
  assert.equal(origin.hostname, 'localhost');
  assert.equal(origin.pathname, '/');
  assert.equal(origin.search, '');
  assert.equal(origin.hash, '');
  assert.equal(origin.username + origin.password, '');
  assert.match(fixture.spki, /^[A-Za-z0-9+/]{43}=$/);
  assert.match(fixture.slo_id, /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
  assert.match(fixture.candidate_binary_sha256, /^[0-9a-f]{64}$/);
  assert.ok([10, 100, 1000].includes(fixture.fleet_size));
  assert.equal(fixture.cache_setup.result,'restarted-with-retained-data');
  assert.equal(fixture.cache_setup.target_slo_id,fixture.slo_id);
  assert.ok(fixture.cache_setup.preserved_target_points>0&&fixture.cache_setup.preserved_target_tiles>0);
  assert.equal(fixture.cache_setup.preserved_current_summaries,fixture.fleet_size);
  assert.equal(fixture.datasource_ids.length,10);
  for(const id of [...fixture.datasource_ids,fixture.service_id])assert.match(id,/^[0-9a-f-]{36}$/);
  assert.ok(typeof fixture.login === 'string' && typeof fixture.password === 'string' && fixture.login && fixture.password);
  output = resolve(fixture.artifacts);
  assert.ok(output.startsWith(resolve('..', '.local') + sep));
  await mkdir(output, { recursive: true, mode: 0o700 });
  report.candidate_binary_sha256 = fixture.candidate_binary_sha256;
  report.cache_setup={result:fixture.cache_setup.result,target_slo_id:fixture.cache_setup.target_slo_id,preserved_target_points:fixture.cache_setup.preserved_target_points,preserved_target_tiles:fixture.cache_setup.preserved_target_tiles,preserved_current_summaries:fixture.cache_setup.preserved_current_summaries,evicted_history_responses:fixture.cache_setup.evicted_history_responses,evicted_overview_snapshots:fixture.cache_setup.evicted_overview_snapshots};
  browser = await chromium.launch({ headless: true, args: [`--ignore-certificate-errors-spki-list=${fixture.spki}`] });
  report.environment.browser = browser.version();
  report.environment.cache = 'cold: fresh context/disabled HTTP cache; warm: same-context reload with HTTP cache enabled; response eviction precedes backend restart; OS/PostgreSQL buffers are not claimed cold';
  stage = 'authenticate';
  const loginContext = await browser.newContext({ viewport: report.environment.viewport });
  const loginPage = await loginContext.newPage();
  await loginPage.goto(origin.href);
  await loginPage.getByLabel('Login', { exact: true }).fill(fixture.login);
  await loginPage.getByLabel('Password', { exact: true }).fill(fixture.password);
  await loginPage.getByLabel('Password', { exact: true }).press('Enter');
  await loginPage.getByRole('heading', { name: 'Agents', exact: true }).waitFor();
  const storageState = await loginContext.storageState();
  fixture.password = '';
  await loginContext.close();

  const load=async(page,view,budget,cohort,index,navigate)=>{
    const sample={view,index,budget_ms:budget,browser_cache:cohort==='cold'?'fresh-disabled':'warm-enabled',server_response_cache:cohort==='cold'&&index===0?'evicted-before-restart; no prior visit to this route':'reuse eligible; actual hit/miss requires trace evidence'};
    try {
      await navigate();
      await page.waitForFunction(()=>Number.isFinite(window.__almanacPerformance?.primary_ms),{},{timeout:10_000});
      Object.assign(sample,await browserTimings(page));
      if(sample.fetches.some(fetch=>fetch.status>=400)){sample.failure='normal_navigation_unavailable';report.failures.push(stage);}
    } catch {
      sample.failure='primary_content_unavailable';sample.primary_ms=await page.evaluate(()=>performance.now()).catch(()=>10_000);report.failures.push(stage);
    }
    if(!Number.isFinite(sample.primary_ms)||sample.primary_ms>=budget)report.failures.push(`${stage}-load-budget`);
    report[`${cohort}_loads`].push(sample);
    await writeFile(join(output,'browser-performance.json'),JSON.stringify(report)+'\n',{mode:0o600});
    return sample;
  };

  for (const [view, path, budget] of [['sli-list', '/slis', 1000], ['slo-list', '/slos', 1000], ['slo-detail', `/slos/${fixture.slo_id}`, 500]]) {
    for (let index = 0; index < 20; index++) {
      stage = `${view}-cold-${index}`;
      const context = await browser.newContext({ storageState, viewport: report.environment.viewport, colorScheme: 'light' });
      const page = await measuredPage(context, fixture);
      try {
        const sample=await load(page,view,budget,'cold',index,()=>page.goto(new URL(path,origin).href,{waitUntil:'domcontentloaded',timeout:10_000}));
        if(index===19&&!sample.failure){
          const cdp=await context.newCDPSession(page);await cdp.send('Network.enable');await cdp.send('Network.setCacheDisabled',{cacheDisabled:false});
          for(let repeat=0;repeat<20;repeat++){
            stage=`${view}-warm-${repeat}`;
            await load(page,view,budget,'warm',repeat,()=>page.reload({waitUntil:'domcontentloaded',timeout:10_000}));
          }
          if(view==='slo-detail'){
            stage='slo-detail-real-minute-boundary';
            const before=await browserTimings(page);
            const clock=before.fetches.filter(fetch=>Number.isFinite(fetch.server_date_ms)).at(-1);
            assert.ok(clock&&Math.abs(clock.wall_received_ms-clock.server_date_ms)<2500,'browser/backend clock mismatch');
            const nextMinute=Math.floor(Date.now()/60_000)*60_000+60_000;
            while(Date.now()<nextMinute+250)await page.waitForTimeout(Math.max(0,Math.min(1000,nextMinute+250-Date.now())));
            const after=await load(page,view,budget,'lifecycle',0,()=>page.reload({waitUntil:'domcontentloaded',timeout:10_000}));
            after.before_evaluated_through=before.detail.history_evaluated_through;
            assert.ok(!after.failure&&Date.parse(after.detail.history_evaluated_through)>Date.parse(before.detail.history_evaluated_through),'minute-boundary visit did not render a newer requested evaluation');
            await writeFile(join(output,'browser-performance.json'),JSON.stringify(report)+'\n',{mode:0o600});
          }
        }
      } finally {
        await context.close();
      }
    }
  }
  for(const view of ['slis','slos']) {
    stage=`${view}-list-actions`;
    const listContext=await browser.newContext({storageState,viewport:report.environment.viewport,colorScheme:'light'});
    try {
      const listPage=await measuredPage(listContext,fixture);
      await listPage.goto(new URL(`/${view}`,origin).href);
      await listPage.waitForFunction(()=>Number.isFinite(window.__almanacPerformance?.primary_ms),{},{timeout:10_000});
      const navigation=listPage.getByRole('navigation',{name:view==='slis'?'SLI result pages':'SLO result pages',exact:true});
      const evidence=()=>listPage.evaluate(()=>window.__almanacPerformance.listEvidence);
      const action=async(name,event,offset,perform)=>{
        stage=`${view}-${name}`;
        await listPage.evaluate(({offset,event})=>window.__almanacPerformance.beginListAction(offset,event),{offset,event});
        await perform();
        await listPage.waitForFunction(()=>Number.isFinite(window.__almanacPerformance.primary_ms),{},{timeout:10_000});
        const measured=await listPage.evaluate(()=>{
          const state=window.__almanacPerformance;
          return {elapsed_ms:state.primary_ms-state.listAction.started_ms,page_size:Number(new URLSearchParams(location.search).get('page_size')||20),data:state.listEvidence,unavailable:state.fetches.some(fetch=>fetch.started_ms>=state.listAction.started_ms&&fetch.status>=400)};
        });
        const {data}=measured;
        report.list_actions.push({view,action:name,offset,page_size:measured.page_size,elapsed_ms:measured.elapsed_ms,ids:data.ids,expected_total:data.expected_total,counts:data.counts,snapshot_id:data.snapshot_id});
        await writeFile(join(output,'browser-performance.json'),JSON.stringify(report)+'\n',{mode:0o600});
        assert.ok(Number.isFinite(measured.elapsed_ms)&&measured.elapsed_ms>=0&&measured.elapsed_ms<1000&&!measured.unavailable,'list action did not meet usable-content acceptance');
        return data;
      };
      let firstCoverage;
      for(const size of [20,10]) {
        if(await listPage.getByLabel(view==='slis'?'SLIs per page':'SLOs per page',{exact:true}).inputValue()!==String(size)) {
          await action(`size-${size}`,'change',0,()=>listPage.getByLabel(view==='slis'?'SLIs per page':'SLOs per page',{exact:true}).selectOption(String(size)));
        }
        let current=await evidence(),offset=0;
        const seen=new Set(),names=new Set(),groups={needs_attention:0,watch:0,healthy:0};
        const initialSnapshot=current.snapshot_id,initialCounts=current.counts;
        for(let pageIndex=0;pageIndex<Math.ceil(fixture.fleet_size/size);pageIndex++) {
          assert.equal(current.ids.length,Math.min(size,fixture.fleet_size-offset));
          for(let index=0;index<current.ids.length;index++) {
            assert.ok(!seen.has(current.ids[index])&&!names.has(current.names[index]),'list pagination duplicated a fixture identity');
            seen.add(current.ids[index]);names.add(current.names[index]);
            if(view==='slos')groups[current.urgencies[index]]++;
          }
          if(view==='slos'){assert.equal(current.snapshot_id,initialSnapshot);assert.deepEqual(current.counts,initialCounts);}
          assert.equal(Boolean(current.next_cursor),offset+current.ids.length<fixture.fleet_size);
          if(!current.next_cursor)break;
          const previous=current;
          current=await action(`next-${size}-${pageIndex}`,'click',offset+size,()=>navigation.getByRole('button',{name:'Next page',exact:true}).click());
          if(pageIndex===0) {
            const forward=current;
            const back=await action(`back-${size}`,'click',offset,()=>navigation.getByRole('button',{name:/^(Previous page|First page)$/}).click());
            assert.deepEqual(back.ids,previous.ids);
            if(view==='slos'){assert.deepEqual(back.items,previous.items);assert.equal(back.snapshot_id,initialSnapshot);}
            current=await action(`next-again-${size}`,'click',offset+size,()=>navigation.getByRole('button',{name:'Next page',exact:true}).click());
            assert.deepEqual(current.ids,forward.ids);
            if(view==='slos')assert.deepEqual(current.items,forward.items);
          }
          offset+=size;
        }
        assert.equal(seen.size,fixture.fleet_size);assert.equal(names.size,fixture.fleet_size);
        if(view==='slos')assert.deepEqual(groups,initialCounts,'group counts describe the whole snapshot');
        const coverage=[...seen].sort();
        if(firstCoverage)assert.deepEqual(coverage,firstCoverage);else firstCoverage=coverage;
      }
      report.list_coverage[view]=firstCoverage;
      const search=listPage.getByLabel(view==='slis'?'Search SLIs':'Search SLOs',{exact:true});
      const searchFor=async(name,query)=>{
        if(view==='slos')await search.fill(query);
        return action(name,view==='slis'?'input':'keydown',0,()=>view==='slis'?search.fill(query):search.press('Enter'));
      };
      const matched=await searchFor('search-match',view==='slis'?'performance-000':'performance-00');
      assert.equal(matched.expected_total,10);assert.equal(matched.ids.length,10);
      const empty=await searchFor('search-empty','no-performance-match');assert.equal(empty.ids.length,0);assert.equal(empty.expected_total,0);
      await searchFor('search-clear','');
      if(view==='slis') {
        const filtered=await action('datasource-filter','change',0,()=>listPage.getByLabel('Datasource',{exact:true}).selectOption(fixture.datasource_ids[0]));
        assert.equal(filtered.expected_total,fixture.fleet_size/10);
        await action('datasource-clear','change',0,()=>listPage.getByLabel('Datasource',{exact:true}).selectOption(''));
      } else {
        await listPage.getByText('Filters',{exact:true}).click();
        await action('service-filter','change',0,()=>listPage.getByLabel('Service',{exact:true}).selectOption(fixture.service_id));
        await action('service-clear','change',0,()=>listPage.getByLabel('Service',{exact:true}).selectOption(''));
        const {counts}=await evidence(),group=Object.keys(counts).find(key=>counts[key]>0);assert.ok(group);
        await action('urgency-filter','change',0,()=>listPage.getByLabel('Urgency',{exact:true}).selectOption(group));
        await action('urgency-clear','change',0,()=>listPage.getByLabel('Urgency',{exact:true}).selectOption(''));
      }
    } finally {await listContext.close();}
  }
  stage = 'interaction-setup';
  const context = await browser.newContext({ storageState, viewport: report.environment.viewport, colorScheme: 'light' });
  const page = await measuredPage(context, fixture);
  await page.goto(new URL(`/slos/${fixture.slo_id}`, origin).href);
  await page.waitForFunction(() => Number.isFinite(window.__almanacPerformance?.primary_ms), { }, { timeout: 10_000 });
  for (let index = 0; index < 50; index++) {
    stage = `interaction-${index}`;
    const group = index % 2 === 0 ? 'Objective history period' : 'Burn window';
    const label = index % 2 === 0 ? ['90 d', '1 y', '7 d'][Math.floor(index / 2) % 3] : Math.floor(index / 2) % 2 === 0 ? '6 h' : '1 h';
    const sample = { index, group, label };
    try {
      await page.evaluate(({ group, label }) => {
        const container = [...document.querySelectorAll('[role="group"]')].find(node => node.getAttribute('aria-label') === group);
        const button = [...container.querySelectorAll('button')].find(node => node.textContent.trim() === label);
        if (!button || button.getAttribute('aria-pressed') === 'true') throw new Error('interaction is not a change');
        const interaction = window.__almanacPerformance.interaction = { button, feedback_ms: null, chart_ms: null };
        button.addEventListener('click', () => { interaction.started_ms = performance.now(); }, { once: true, capture: true });
      }, { group, label });
      await page.getByRole('group', { name: group, exact: true }).getByRole('button', { name: label, exact: true }).click();
      await page.waitForFunction(() => {
        const value = window.__almanacPerformance.interaction;
        return value.feedback_ms !== null && value.chart_ms !== null;
      }, { }, { timeout: 15_000 });
      Object.assign(sample, await page.evaluate(() => {
        const value = window.__almanacPerformance.interaction;
        return { feedback_ms: value.feedback_ms, chart_ms: value.chart_ms, chart_timing_origin:value.chart_timing_origin,total_ms: value.finished_ms - value.started_ms, network_backed: value.network_backed,detail:window.__almanacPerformance.detailEvidence };
      }));
    } catch {
      sample.failure = 'interaction_or_chart_unavailable';
      sample.feedback_ms = sample.chart_ms = 15_000;
      report.failures.push(stage);
    }
    report.interactions.push(sample);
    await writeFile(join(output, 'browser-performance.json'), JSON.stringify(report) + '\n', { mode: 0o600 });
  }
  report.interaction_resources = await browserTimings(page);
  if (report.interaction_resources.fetches.some(fetch=>fetch.status===503)) report.failures.push('interaction-preparation-wait');
  await context.close();
  report.distributions = Object.fromEntries(['sli-list', 'slo-list', 'slo-detail'].map(view => [view, distribution(report.cold_loads.filter(sample => sample.view === view), 'primary_ms')]));
  report.warm_distributions=Object.fromEntries(['sli-list','slo-list','slo-detail'].map(view=>[view,distribution(report.warm_loads.filter(sample=>sample.view===view),'primary_ms')]));
  report.distributions.feedback = distribution(report.interactions, 'feedback_ms');
  report.distributions.chart = distribution(report.interactions, 'chart_ms');
  for (const [view, budget] of [['sli-list', 1000], ['slo-list', 1000], ['slo-detail', 500]]) {
    if (report.distributions[view].count !== 20 || report.distributions[view].max_ms >= budget) report.failures.push(`${view}-load-budget`);
    if(report.warm_distributions[view].count!==20||report.warm_distributions[view].max_ms>=budget)report.failures.push(`${view}-warm-load-budget`);
  }
  if(report.lifecycle_loads.length!==1)report.failures.push('minute-boundary-cohort-missing');
  if (report.interactions.some(sample=>!Number.isFinite(sample.total_ms)||sample.total_ms>=500)) report.failures.push('interaction-total-budget');
  for (const name of ['feedback', 'chart']) {
    if (report.distributions[name].count !== 50 || report.distributions[name].p95_ms > 200) report.failures.push(`${name}-p95-budget`);
  }
  report.result = report.failures.length ? 'failed' : 'passed';
  await writeFile(join(output, 'browser-performance.json'), JSON.stringify(report) + '\n', { mode: 0o600 });
  if (report.result !== 'passed') process.exitCode = 1;
} catch {
  // Playwright errors can contain entered values. Keep credentials and raw error
  // messages out of both the artifact and terminal output.
  report.failures.push(stage);
  report.result = 'failed';
  if (output) await writeFile(join(output, 'browser-performance.json'), JSON.stringify(report) + '\n', { mode: 0o600 }).catch(() => {});
  process.exitCode = 1;
} finally {
  await browser?.close();
}
