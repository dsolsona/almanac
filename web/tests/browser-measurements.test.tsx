import { afterEach, expect, it, vi } from 'vitest';
import { waitFor } from '@testing-library/react';

const observers:MutationObserver[]=[];
afterEach(()=>{
  for(const observer of observers)observer.disconnect();
  observers.length=0;
  document.body.innerHTML='';
  Reflect.deleteProperty(document,'fonts');
  Reflect.deleteProperty(window,'__almanacPerformance');
  history.replaceState({},'','/');
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function measurementInstaller(){
  const runtime=(globalThis as unknown as {process:{getBuiltinModule:(name:string)=>unknown}}).process;
  const fs=runtime.getBuiltinModule('node:fs') as {readFileSync:(path:string,encoding:string)=>string};
  const source=fs.readFileSync('scripts/measure-backend-browser.mjs','utf8');
  const start=source.indexOf('function installMeasurements('),end=source.indexOf('\nasync function measuredPage(',start);
  expect(start).toBeGreaterThan(0);expect(end).toBeGreaterThan(start);
  // Run the same self-contained function Playwright serializes, without starting
  // the executable's database/browser capacity workload.
  return new Function(`return (${source.slice(start,end)});`)() as (fixture:{fleetSize:number;datasourceIDs:string[];serviceID:string})=>void;
}

it('recognizes usable rows when the application consumes a stream without Response.json',async()=>{
  const install=measurementInstaller();
  const response=new Response(JSON.stringify({items:[{id:'example',name:'performance-0000',latest_observation:null}],next_cursor:''}));
  const json=vi.spyOn(response,'json');
  vi.stubGlobal('fetch',vi.fn(async()=>response));
  const Observer=globalThis.MutationObserver;
  vi.stubGlobal('MutationObserver',class extends Observer {constructor(callback:MutationCallback){super(callback);observers.push(this);}});
  Object.defineProperty(document,'fonts',{configurable:true,value:{ready:Promise.resolve()}});
  vi.spyOn(Element.prototype,'getBoundingClientRect').mockReturnValue({width:100,height:20} as DOMRect);
  history.replaceState({},'','/slis');
  document.body.innerHTML='<table class="sli-list-table"><tbody><tr><td><a href="/slis/example">Example</a></td><td></td><td></td><td></td><td></td><td></td><td></td></tr></tbody></table>';
  install({fleetSize:1,datasourceIDs:['source'],serviceID:'service'});
  const actual=await window.fetch('/api/v1/slis?limit=20');
  const reader=actual.body!.getReader();let received=0;
  for(;;){const {done,value}=await reader.read();if(done)break;received+=value.byteLength;}
  reader.releaseLock();
  const state=()=> (window as unknown as {__almanacPerformance:{primary_ms:number|null;listEvidence:{ids:string[]};fetches:{observed_body_ms?:number}[]}}).__almanacPerformance;
  await waitFor(()=>expect(Number.isFinite(state().primary_ms)).toBe(true));
  expect(state().listEvidence.ids).toEqual(['example']);
  expect(state().fetches[0]!.observed_body_ms).toBeGreaterThan(0);
  expect(json).not.toHaveBeenCalled();
  expect(received).toBeGreaterThan(0);
});

it('rejects an old drawn generation and timezone until the requested graph is committed',async()=>{
  const id='11111111-1111-1111-1111-111111111111',generation='22222222-2222-2222-2222-222222222222';
  const start='2026-10-01T00:00:00.000Z',end='2026-10-08T00:00:00.000Z';
  const performance={remaining_fraction:'0.5',attainment:'0.999',remaining_budget:'50.000000000000',burn:'1'};
  const current={performance,evaluation_at:end};
  const stamp={generation_id:generation,evaluated_through:end,source_generation:'1',definition_revision:'1',measurement_revision:'1'};
  const objective={id,target:'0.995',rolling_window_seconds:28*86400,cadence_seconds:60,definition_revision:'1'};
  const status={slo:objective,generation:stamp,current,burn_1h:{performance:{burn:'1'}},burn_6h:{performance:{burn:'2'}}};
  const historyValue={objective,generation:stamp,current,burn_1h:status.burn_1h,burn_6h:status.burn_6h,effective_at:end,context:{measurement_revision:'1'},range:{requested_start:start,requested_end:end,effective_start:start,effective_end:end},burn_series:{burn_window_seconds:21600},compliance_summary:{met_days:6,evaluable_days:7}};
  vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL)=>new Response(JSON.stringify(String(input).includes('/status')?status:historyValue))));
  const Observer=globalThis.MutationObserver;
  vi.stubGlobal('MutationObserver',class extends Observer {constructor(callback:MutationCallback){super(callback);observers.push(this);}});
  Object.defineProperty(document,'fonts',{configurable:true,value:{ready:Promise.resolve()}});
  vi.spyOn(Element.prototype,'getBoundingClientRect').mockReturnValue({width:100,height:20} as DOMRect);
  history.replaceState({},'',`/slos/${id}?range=7d&burn_window=21600&tz=local`);
  document.body.innerHTML='<div class="app-budget-hero">50%</div><dl class="app-status-facts">'+[['Attainment','99.9%'],['Target','99.5%'],['Burn 1 h','1×'],['Burn 6 h','2×'],['Bad events left','50']].map(([label,value])=>`<div><dt>${label}</dt><dd>${value}</dd></div>`).join('')+'</dl><button aria-label="Refresh objective status"></button>'+['99.9%','50%','2×','6 / 7'].map(value=>`<div class="app-lane-value">${value}</div>`).join('')+'<div class="app-timeline-plot"><div class="timeline-chart__plot" data-chart-ready="true" data-rendered-generation="old" data-rendered-zone="UTC"><svg></svg></div></div>';
  measurementInstaller()({fleetSize:1,datasourceIDs:['source'],serviceID:'service'});
  const query=new URLSearchParams({at:end,start,end,burn_window_seconds:'21600'});
  await window.fetch(`/api/v1/slos/${id}/status`);
  await window.fetch(`/api/v1/slos/${id}/operational?${query}`);
  const state=()=> (window as unknown as {__almanacPerformance:{primary_ms:number|null;fetches:{observed_body_ms?:number}[];detailEvidence:{rendered_generation:string;time_zone:string;burn_window_seconds:number}}}).__almanacPerformance;
  await waitFor(()=>expect(state().fetches.every(sample=>Number.isFinite(sample.observed_body_ms))).toBe(true));
  expect(state().primary_ms).toBeNull();
  const plot=document.querySelector<HTMLElement>('.timeline-chart__plot')!;
  plot.dataset.renderedGeneration=generation;
  await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
  expect(state().primary_ms).toBeNull();
  plot.dataset.renderedZone='local';
  await waitFor(()=>expect(Number.isFinite(state().primary_ms)).toBe(true));
  expect(state().detailEvidence).toMatchObject({rendered_generation:generation,time_zone:'local',burn_window_seconds:21600});
});

it('requires matching build, timing and descended backend/database/chart spans for trace evidence',()=>{
  const runtime=(globalThis as unknown as {process:{getBuiltinModule:(name:string)=>unknown}}).process;
  const fs=runtime.getBuiltinModule('node:fs') as {readFileSync:(path:string,encoding:string)=>string};
  const source=fs.readFileSync('scripts/check-telemetry-runtime.mjs','utf8');
  const start=source.indexOf('function connected('),end=source.indexOf('\nexport async function captureTraces(',start);
  expect(start).toBeGreaterThan(0);expect(end).toBeGreaterThan(start);
  const connected=new Function(`return (${source.slice(start,end)});`)() as (document:unknown,event:unknown,hash:string,environment:string)=>boolean;
  const trace='a'.repeat(32),hash='b'.repeat(64),event={trace_id:trace,journey:'slo.detail',duration_ms:120};
  const tags=()=>[{key:'almanac.build.sha256',value:hash},{key:'deployment.environment.name',value:'acceptance'}];
  const span=(id:string,parent:string,name:string,process:string,duration=1000)=>({traceID:trace,spanID:id,processID:process,operationName:name,duration,references:parent?[{traceID:trace,spanID:parent}]:[]});
  const makeDocument=()=>({data:[{traceID:trace,processes:{web:{serviceName:'almanac-web',tags:tags()},backend:{serviceName:'almanac-backend',tags:tags()}},spans:[span('root','','ui.slo.detail','web',120000),span('http','root','GET /api/v1/slos/{id}/operational','backend'),span('db','http','db.slo.read','backend'),span('chart','root','ui.chart.render','web')]}]});
  expect(connected(makeDocument(),event,hash,'acceptance')).toBe(true);
  const wrongDuration=makeDocument();wrongDuration.data[0]!.spans[0]!.duration=130000;
  expect(connected(wrongDuration,event,hash,'acceptance')).toBe(false);
  expect(connected(makeDocument(),event,'c'.repeat(64),'acceptance')).toBe(false);
  expect(connected(makeDocument(),event,hash,'local')).toBe(false);
  const orphan=makeDocument();orphan.data[0]!.spans[2]!.references=[];
  expect(connected(orphan,event,hash,'acceptance')).toBe(false);
  const missingChart=makeDocument();missingChart.data[0]!.spans.pop();
  expect(connected(missingChart,event,hash,'acceptance')).toBe(false);
});
