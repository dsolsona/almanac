import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { SlisPage } from '../src/slis/SlisPage.tsx';
import { SliObjectives } from '../src/slis/SliObjectives.tsx';
import { clearQueryCache } from '../src/queryCache.ts';
import { sli, sliID, datasource, history as fixtureHistory, linkedStatus } from '../browser/support/sli-detail-fixture.ts';
import type { SliObservation } from '../src/slis/client.ts';

const viewer={csrf_token:'fixture',session:{user_id:'reader',login:'reader',role:'viewer' as const,restricted:false}};
const json=(body:unknown,headers:Record<string,string>={})=>new Response(JSON.stringify(body),{headers:{'Content-Type':'application/json',...headers}});
const complete=Array.from({length:60},(_,i):SliObservation=>({...fixtureHistory.items[0]!,observation_id:`70000000-0000-4000-8000-${String(i+1).padStart(12,'0')}`,interval_start:new Date(Date.parse(fixtureHistory.inspected_start)+i*60_000).toISOString(),interval_end:new Date(Date.parse(fixtureHistory.inspected_start)+(i+1)*60_000).toISOString(),outcome:'observed',good_count:'1998',total_count:'2000',error_code:null}));
afterEach(()=>{cleanup();vi.unstubAllGlobals();clearQueryCache();history.replaceState({},'','/');});
function setup(items:SliObservation[],options:{older?:boolean;cadence?:number}={}) {
  history.replaceState({},'',`/slis/${sliID}`);
  vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL)=>{
    const url=String(input);
    if(url.includes('/observations'))return json(url.includes('cursor=older')?{...fixtureHistory,items:[],inspected_start:'2026-11-11T08:10:00Z',inspected_end:'2026-11-11T09:00:00Z',missing_intervals:[{interval_start:'2026-11-11T08:10:00Z',interval_end:'2026-11-11T09:00:00Z'}]}:{...fixtureHistory,items,next_cursor:options.older?'older':'',inspected_start:options.cadence===15?'2026-11-11T09:59:45Z':fixtureHistory.inspected_start});
    if(url.startsWith('/api/v1/datasources'))return json({items:[datasource],next_cursor:''});
    const resource={...sli,polling_interval_seconds:options.cadence??60,linked_slos:{items:[],total_count:0}};
    if(url.startsWith('/api/v1/slis?'))return json({items:[resource],next_cursor:''});
    return json(resource,{ETag:`"sli:${sliID}:1"`});
  }));
  render(<SlisPage context={viewer}/>);
}
it('includes known-empty outcomes in collection coverage without inventing a ratio',async()=>{
  setup(complete.map((row,i)=>i===0?{...row,outcome:'complete_empty',good_count:null,total_count:null}:row));
  await waitFor(()=>expect(document.querySelectorAll('.interval-state')).toHaveLength(60));
  expect(document.querySelector('.sli-measurement-summary')).toHaveTextContent('60 / 60 min');
  expect(document.querySelectorAll('.chart-point')).toHaveLength(59);
});
it('reconstructs older missing intervals in exact values while retaining the original graph',async()=>{
  setup(fixtureHistory.items,{older:true});
  await screen.findByRole('heading',{name:'Measurements'});
  await userEvent.click(screen.getByRole('button',{name:'Exact values ↓'}));
  await userEvent.click(await screen.findByRole('button',{name:'Load more intervals'}));
  await waitFor(()=>expect(document.querySelectorAll('.sli-intervals-table tbody tr')).toHaveLength(110));
  expect(document.querySelectorAll('.interval-state')).toHaveLength(60);
  expect(document.querySelector('.sli-measurement-summary')).toHaveTextContent('99.48%');
  await userEvent.click(screen.getByRole('radio',{name:/Failed & missing/}));
  expect(document.querySelectorAll('.sli-intervals-table tbody tr')).toHaveLength(53);
});
it('makes a failed objective refresh visible even while retaining previous links',async()=>{
  vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL)=>json(linkedStatus(String(input).includes('000000000002')?1:0))));
  const view=render(<SliObjectives projection={sli} unavailable={false}/>);
  await screen.findAllByText('Partial data');
  view.rerender(<SliObjectives projection={sli} unavailable/>);
  expect(screen.getByRole('alert')).toHaveTextContent(/could not be refreshed/i);
  expect(screen.getAllByText('Unavailable')).toHaveLength(2);
});
it('formats a 0.29 objective target as exactly 29 percent',async()=>{
  vi.stubGlobal('fetch',vi.fn(async(input:RequestInfo|URL)=>{const value=linkedStatus(String(input).includes('000000000002')?1:0);return json({...value,slo:{...value.slo,target:'0.29'}});}));
  render(<SliObjectives projection={sli} unavailable={false}/>);
  await screen.findAllByText('Partial data');
  expect(screen.getByRole('table')).not.toHaveTextContent('28.999');
  expect(screen.getByRole('table')).toHaveTextContent('29%');
});
it('counts zero-traffic intervals as lacking a ratio without treating them as query errors',async()=>{
  setup(complete.map(row=>({...row,good_count:'0',total_count:'0'})));
  await waitFor(()=>expect(document.querySelectorAll('.interval-state.zero')).toHaveLength(60));
  expect(document.querySelector('#sli-intervals > summary')).toHaveTextContent('60 without a ratio');
});
it('calls the minimum a lowest interval when cadence is not one minute',async()=>{
  setup([{...complete[0]!,interval_start:'2026-11-11T09:59:45Z',interval_end:'2026-11-11T10:00:00Z'}],{cadence:15});
  await waitFor(()=>expect(document.querySelectorAll('.interval-state')).toHaveLength(1));
  expect(document.querySelector('.sli-detail-inline-note')).toHaveTextContent('Lowest interval');
});
