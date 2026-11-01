import { afterEach, expect, it, vi } from 'vitest';
import { waitFor } from '@testing-library/react';

const observers:MutationObserver[]=[];
afterEach(()=>{
  for(const observer of observers)observer.disconnect();
  document.body.innerHTML='';
  Reflect.deleteProperty(document,'fonts');
  Reflect.deleteProperty(window,'__almanacPerformance');
  history.replaceState({},'','/');
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it('recognizes usable rows when the application consumes a stream without Response.json',async()=>{
  const runtime=(globalThis as unknown as {process:{getBuiltinModule:(name:string)=>unknown}}).process;
  const fs=runtime.getBuiltinModule('node:fs') as {readFileSync:(path:string,encoding:string)=>string};
  const source=fs.readFileSync('scripts/measure-backend-browser.mjs','utf8');
  const start=source.indexOf('function installMeasurements('),end=source.indexOf('\nasync function measuredPage(',start);
  expect(start).toBeGreaterThan(0);expect(end).toBeGreaterThan(start);
  // Run the same self-contained function Playwright serializes, without starting
  // the executable's database/browser capacity workload.
  const install=new Function(`return (${source.slice(start,end)});`)() as (fixture:{fleetSize:number;datasourceIDs:string[];serviceID:string})=>void;
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
