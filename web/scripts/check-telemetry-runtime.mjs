import assert from 'node:assert/strict';
import { readFile, writeFile } from 'node:fs/promises';
import { cpus, totalmem, platform, arch } from 'node:os';
import { join } from 'node:path';
import { expect } from '@playwright/test';

const percentile=(values,fraction)=>[...values].sort((a,b)=>a-b)[Math.ceil(values.length*fraction)-1];
const summary=values=>({samples:values.length,p50_ms:percentile(values,.5),p95_ms:percentile(values,.95),max_ms:Math.max(...values)});

function connected(document,event,backendSHA,expectedEnvironment) {
  return document.data?.some(data=>{
    if(data.traceID!==event.trace_id)return false;
    const spans=new Map(data.spans.map(span=>[span.spanID,span]));
    const service=span=>data.processes[span.processID]?.serviceName;
    const build=span=>data.processes[span.processID]?.tags?.find(tag=>tag.key==='almanac.build.sha256')?.value;
    const root=[...spans.values()].find(span=>service(span)==='almanac-web'&&span.operationName==='ui.'+event.journey&&Math.abs(span.duration/1000-event.duration_ms)<=2);
    const environment=span=>data.processes[span.processID]?.tags?.find(tag=>tag.key==='deployment.environment.name')?.value;
    if(!root||build(root)!==backendSHA||environment(root)!==expectedEnvironment)return false;
    const descends=(id,seen=new Set())=>{
      if(id===root.spanID)return true;
      if(seen.has(id))return false;
      seen.add(id);
      return spans.get(id)?.references?.some(reference=>reference.traceID===event.trace_id&&descends(reference.spanID,seen))??false;
    };
    const children=[...spans.values()].filter(span=>descends(span.spanID));
    const chart=event.journey!=='slo.detail'||children.some(span=>service(span)==='almanac-web'&&span.operationName==='ui.chart.render');
    // A browser-cache hit has no new history query. Its caller must join the
    // rendered generation to an independently captured network-backed producer.
    if(event.cache_source_trace_id)return event.journey==='slo.detail'&&chart&&/^[0-9a-f]{32}$/.test(event.cache_source_trace_id)&&event.cache_source_trace_id!==event.trace_id&&/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(event.history_generation);
    return children.some(span=>service(span)==='almanac-backend'&&build(span)===backendSHA&&environment(span)===expectedEnvironment&&/^(GET|POST) /.test(span.operationName))
      &&children.some(span=>service(span)==='almanac-backend'&&span.operationName.startsWith('db.'))
      &&chart;
  })??false;
}

export async function captureTraces(traceURL,events,artifacts,backendSHA,containsSecret,expectedEnvironment='local',requiredEvents=null) {
  const endpoint=new URL(traceURL);
  assert.equal(endpoint.protocol,'http:'); assert.equal(endpoint.hostname,'127.0.0.1');
  assert.ok(['local','acceptance'].includes(expectedEnvironment));
  const required=requiredEvents??['sli.list','slo.list','slo.detail'].map(journey=>{
    const event=events.find(value=>value.journey===journey&&value.result==='ready');
    assert.ok(event&&/^[0-9a-f]{32}$/.test(event.trace_id)&&Number.isFinite(event.duration_ms),'missing rendered container journey '+journey);
    return event;
  });
  assert.ok(required.length>0&&required.length<=9,'trace lookup batch is outside its bound');
  for(const event of required)assert.ok(['sli.list','slo.list','slo.detail'].includes(event.journey)&&event.result==='ready'&&/^[0-9a-f]{32}$/.test(event.trace_id)&&Number.isFinite(event.duration_ms)&&event.duration_ms>=0,'missing rendered journey identity');
  const pending=new Map(required.map((event,index)=>[requiredEvents?`${event.journey}-${index}`:event.journey,{event,lookups:[],document:null,connected:false}]));
  for(const event of events.filter(value=>value.diagnostic).slice(0,6))pending.set(`${event.journey}-failure-${event.trace_id}`,{event,lookups:[],document:null,connected:false});
  const started=performance.now();
  // The pinned sink deliberately hides traces for 30s and flushes indexes at
  // 20s. This collector observation bound never changes the page-load budget.
  while(performance.now()-started<40_000&&[...pending.values()].some(value=>!value.connected)) {
    await Promise.all([...pending.values()].filter(value=>!value.connected).map(async value=>{
      let status=0,spans=0;
      try {
        const response=await fetch(new URL('/select/jaeger/api/traces/'+value.event.trace_id,endpoint),{signal:AbortSignal.timeout(2000),redirect:'error'});
        status=response.status;
        const reader=response.body.getReader(); const chunks=[]; let size=0;
        for(;;){const {done,value:chunk}=await reader.read();if(done)break;size+=chunk.length;if(size>4*1024*1024){await reader.cancel();throw new Error('trace response exceeds bound');}chunks.push(chunk);}
        if(status===200){
          const text=Buffer.concat(chunks).toString('utf8');
          if(containsSecret(text)){value.unsafe=true;throw new Error('trace contains fixture credentials');}
          const document=JSON.parse(text);
          for(const data of document.data??[])for(const span of data.spans)for(const tag of span.tags??[])if(/password|cookie|authorization|(?:^|[._])token(?:[._]|$)|^db\.(?:statement|query\.text|connection_string)$|^url\.(?:full|query)$/i.test(tag.key)){value.unsafe=true;throw new Error('trace contains forbidden metadata');}
          value.document=document;spans=document.data?.reduce((sum,data)=>sum+data.spans.length,0)??0;value.connected=connected(document,value.event,backendSHA,expectedEnvironment);
        }
      } catch(error) { if(value.unsafe)throw error; /* The bounded lookup history retains incomplete capture. */ }
      value.lookups.push({elapsed_ms:performance.now()-started,status,spans,connected:value.connected});
    }));
    if([...pending.values()].some(value=>!value.connected))await new Promise(resolve=>setTimeout(resolve,200));
  }
  for(const [journey,value]of pending){
    await writeFile(join(artifacts,`container-trace-${journey}.json`),JSON.stringify(value),{mode:0o600});
    assert.equal(value.connected,true,'container trace is incomplete for '+journey);
  }
  return [...pending.entries()].map(([key,value])=>({journey:value.event.journey,trace_id:value.event.trace_id,duration_ms:value.event.duration_ms,connected:value.connected,history_generation:value.event.history_generation??'',cache_source_trace_id:value.event.cache_source_trace_id??'',artifact:`container-trace-${key}.json`}));
}

// Called only by the existing isolated Compose acceptance gate. The application,
// PostgreSQL, authentication and collector are real; no product route is mocked.
export async function checkTelemetryRuntime({browser,url,storageState,sliID,sloID,artifacts,backendSHA,traceURL,setEnabled,containsSecret,sampleRuntime,snapshotDataset,precedingFailure=null}) {
  const dependencies=JSON.parse(await readFile(new URL('../package.json',import.meta.url),'utf8')).dependencies;
  const cases=[['sli.list','/slis'],['slo.list','/slos'],['slo.detail','/slos/'+sloID]];
  const samples=[],events=[];
  const report={hardware:{host_cpu:cpus()[0]?.model,host_memory_bytes:totalmem(),platform:platform(),architecture:arch()},
    software:{node:process.version,chromium:browser.version(),otel_api:dependencies['@opentelemetry/api'],otel_sdk:dependencies['@opentelemetry/sdk-trace'],otel_http_exporter:dependencies['@opentelemetry/exporter-trace-otlp-http']},
    fixture:{viewport:[1440,1000],cpu_slowdown:4,download_mbit:10,upload_mbit:10,rtt_ms:100,root_sampling:1,order:['enabled','disabled','disabled','enabled'],warm_samples_per_page_and_mode:10,matrix_collection_bound_ms:180_000,disabled_mode:'Same production bundle; OTEL_SDK_DISABLED and browser capability disable tracing. Common SDK loading remains in both modes.'},
    backend_sha256:backendSHA,preceding_gate_failure:precedingFailure,samples,overhead:[],process_samples:[],speed_failures:[],minute_boundary:null,tracing_checks_passed:false,page_speed_passed:false};
  let latestDate=0;
  const visit=async(context,journey,path,mode,block,phase)=>{
    report.current={journey,mode,block,phase};
    const page=await context.newPage(); page.setDefaultTimeout(10_000);
    const cdp=await context.newCDPSession(page);
    await cdp.send('Emulation.setCPUThrottlingRate',{rate:4});
    await cdp.send('Network.enable');
    await cdp.send('Network.emulateNetworkConditions',{offline:false,latency:100,downloadThroughput:10_000_000/8,uploadThroughput:10_000_000/8});
    await page.addInitScript(()=>{
      window.almanacRuntimeJourneys=[];
      addEventListener('almanac:journey',event=>{const value=event.detail;if(/^[0-9a-f]{32}$/.test(value?.traceId)&&Number.isFinite(value.duration))window.almanacRuntimeJourneys.push({trace_id:value.traceId,journey:value.journey,result:value.result,duration_ms:value.duration});});
    });
    const capabilities=new Set(); let pending=0;
    const unavailable=[];
    page.on('response',response=>{
      if(new URL(response.url()).pathname.startsWith('/api/')){
        const headers=response.headers();if(headers['x-almanac-tracing'])capabilities.add(headers['x-almanac-tracing']);
        const date=Date.parse(headers.date);if(Number.isFinite(date))latestDate=date;
        if(response.status()===503){
          pending++;
          const trace=response.request().headers().traceparent;
          unavailable.push({method:response.request().method(),path:new URL(response.url()).pathname.replace(/[0-9a-f]{8}-[0-9a-f-]{27}/g,'{id}'),status:503,trace_id:typeof trace==='string'?trace.split('-')[1]:null});
        }
      }
    });
    const started=performance.now(),wallStarted=Date.now();
    const sample={journey,mode,block,phase,result:'pending',elapsed_ms:null};
    samples.push(sample);
    try {
      await page.goto(url+path);
      if(journey==='sli.list')await expect(page.locator(`a[href="/slis/${sliID}"]`).first()).toBeVisible();
      else if(journey==='slo.list'){
        await expect(page.locator(`a[href="/slos/${sloID}"]`).first()).toBeVisible();
        await expect(page.locator('.app-slo-rows .slo-reference-spark:not([data-chart-ready="true"])')).toHaveCount(0);
      }
      else {
        await expect(page.locator('.app-lane-label')).toHaveCount(4);
        await expect(page.locator('.timeline-chart__plot svg')).toBeVisible();
        await expect(page.locator('.timeline-chart__plot')).toHaveAttribute('data-chart-ready','true');
        await expect.poll(()=>page.locator('.timeline-chart__plot svg text').allTextContents()).toContain('1×');
        await expect(page.locator('.app-budget-hero')).toHaveText('-900%');
        await expect(page.locator('.app-lane-label').first()).toContainText('99%');
      }
      await page.evaluate(async()=>{await document.fonts.ready;await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));});
      const elapsed=performance.now()-started;
      const rendered=await page.evaluate(()=>({events:window.almanacRuntimeJourneys,resources:performance.getEntriesByType('resource').filter(entry=>/\/assets\/[^/]+\.js$/.test(new URL(entry.name).pathname)).map(entry=>({path:new URL(entry.name).pathname,start_ms:entry.startTime,duration_ms:entry.duration,response_start_ms:entry.responseStart,response_end_ms:entry.responseEnd,transfer_bytes:entry.transferSize,encoded_bytes:entry.encodedBodySize,decoded_bytes:entry.decodedBodySize}))}));
      const wallElapsed=Date.now()-wallStarted;
      Object.assign(sample,{result:'ready',elapsed_ms:elapsed,wall_elapsed_ms:wallElapsed,clock_adjustment_ms:wallElapsed-elapsed,server_date_ms:latestDate,pending_responses:pending,unavailable_responses:unavailable,script_resources:rendered.resources,script_encoded_bytes:rendered.resources.reduce((sum,entry)=>sum+entry.encoded_bytes,0),script_decoded_bytes:rendered.resources.reduce((sum,entry)=>sum+entry.decoded_bytes,0)});
      assert.ok(Math.abs(wallElapsed-elapsed)<250,'clock discontinuity invalidates the runtime sample');
      assert.deepEqual([...capabilities],[mode==='enabled'?'1':'0'],'runtime tracing configuration was not applied');
      if(mode==='enabled')events.push(...rendered.events);
      if(mode==='enabled'&&(phase==='first'||pending>0)){
        // The measured load is already complete. Preserve this trace before
        // closing its tab; diagnostic export is outside the page-load timing.
        const flushed=await page.evaluate(()=>new Promise(resolve=>{
          const timeout=setTimeout(()=>resolve({result:'timeout'}),2100);
          addEventListener('almanac:telemetry-flushed',event=>{clearTimeout(timeout);resolve(event.detail);},{once:true});
          dispatchEvent(new Event('almanac:telemetry-flush'));
        }));
        assert.equal(flushed.result,'ready','container evidence flush did not finish');
        const emitted=await page.evaluate(()=>window.almanacRuntimeJourneys);
        events.push(...emitted.map(event=>({...event,diagnostic:pending>0})));
        sample.trace_id=emitted.find(event=>event.journey===journey)?.trace_id??null;
      }
    } catch(error) {
      if(sample.result==='ready')sample.evidence_error_type=error.name;
      else Object.assign(sample,{result:'failed',elapsed_ms:performance.now()-started,wall_elapsed_ms:Date.now()-wallStarted,error_type:error.name,pending_responses:pending});
      throw error;
    } finally {await page.close();}
  };
  try {
    report.dataset_before=await snapshotDataset();
    const matrixStarted=performance.now();
    for(const [block,enabled]of [true,false,false,true].entries()){
      await setEnabled(enabled);
      for(const [journey,path]of cases){
        const context=await browser.newContext({storageState,viewport:{width:1440,height:1000},colorScheme:'light'});
        try {
          await visit(context,journey,path,enabled?'enabled':'disabled',block,'first');
          for(let index=0;index<5;index++){
            assert.ok(performance.now()-matrixStarted<180_000,'bounded telemetry comparison exceeded 180s');
            await visit(context,journey,path,enabled?'enabled':'disabled',block,'repeat');
          }
        } finally {await context.close();}
      }
      report.process_samples.push({block,enabled,...await sampleRuntime()});
    }
    report.matrix_elapsed_ms=performance.now()-matrixStarted;
    assert.ok(report.matrix_elapsed_ms<=180_000,'bounded telemetry comparison exceeded 180s');
    // Retain a real ordinary visit across a server minute boundary separately
    // from the enabled/disabled comparison; no time headers are rewritten.
    assert.ok(latestDate>0,'server time evidence is missing');
    const before=Math.floor(latestDate/60_000);
    await new Promise(resolve=>setTimeout(resolve,60_000-latestDate%60_000+1100));
    const context=await browser.newContext({storageState,viewport:{width:1440,height:1000},colorScheme:'light'});
    try {await visit(context,'slo.detail','/slos/'+sloID,'enabled',4,'after-minute');}
    finally {await context.close();}
    report.minute_boundary={before,after:Math.floor(latestDate/60_000)};
    assert.ok(report.minute_boundary.after>before,'server minute boundary was not observed');
    report.dataset_after=await snapshotDataset();
    assert.deepEqual(report.dataset_after,report.dataset_before,'canonical fixture changed during the tracing comparison');
    for(const [journey]of cases){
      const values=mode=>samples.filter(value=>value.journey===journey&&value.mode===mode&&value.phase==='repeat').map(value=>value.elapsed_ms);
      const enabled=summary(values('enabled')),disabled=summary(values('disabled'));
      report.overhead.push({journey,enabled,disabled,p95_fraction:enabled.p95_ms/disabled.p95_ms-1});
    }
    report.speed_failures=samples.filter(value=>value.elapsed_ms>=(value.journey==='slo.detail'?500:1000));
    report.page_speed_passed=report.speed_failures.length===0;
    await writeFile(join(artifacts,'telemetry-runtime.json'),JSON.stringify(report,null,2),{mode:0o600});
    await captureTraces(traceURL,events,artifacts,backendSHA,containsSecret);
    assert.ok(report.overhead.every(value=>value.p95_fraction<=.05),'tracing exceeds the 5% p95 overhead allowance; inspect telemetry-runtime.json');
    report.tracing_checks_passed=true;
    assert.ok(samples.every(value=>value.result==='ready'&&value.pending_responses===0),'normal navigation waited for preparation or unavailable responses; inspect telemetry-runtime.json');
    assert.equal(report.page_speed_passed,true,'rendered page-load budget exceeded; inspect telemetry-runtime.json and connected VictoriaTraces');
  } catch(error) {
    report.failure_type=error.name;
    throw error;
  } finally {
    await writeFile(join(artifacts,'telemetry-runtime.json'),JSON.stringify(report,null,2),{mode:0o600});
    await setEnabled(true);
  }
}
