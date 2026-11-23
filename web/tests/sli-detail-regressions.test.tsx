import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, expect, it, vi } from 'vitest';
import { SlisPage } from '../src/slis/SlisPage.tsx';
import { SliMeasurementHistory } from '../src/slis/SliMeasurementHistory.tsx';
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
    if (url.endsWith('/measurements?period=30d')) return json({
      bucket_seconds: 10800,
      buckets: [
        {start:'2026-11-11T01:00:00Z', end:'2026-11-11T04:00:00Z', good:'90', total:'100', observed:179, missing:1, zero_traffic:0, known_empty:0, failed:0, incomplete:0},
        {start:'2026-11-11T04:00:00Z', end:'2026-11-11T07:00:00Z', good:null, total:null, observed:0, missing:180, zero_traffic:0, known_empty:0, failed:0, incomplete:0},
        {start:'2026-11-11T07:00:00Z', end:'2026-11-11T10:00:00Z', good:'990', total:'1000', observed:180, missing:0, zero_traffic:0, known_empty:0, failed:0, incomplete:0},
      ],
    });
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
  expect(document.querySelectorAll('.chart-point')).toHaveLength(0);
});
it('refreshes the one-hour chart when the first backfill snapshot already has recovered points', async () => {
  setup(complete.slice(1), {older: true});
  const summary = () => document.querySelector('.sli-measurement-summary');
  await waitFor(() => expect(summary()).toHaveTextContent('59 / 60 min'));
  await userEvent.click(screen.getByRole('button', { name: 'Exact values ↓' }));
  await userEvent.click(await screen.findByRole('button', { name: 'Load more intervals' }));
  await waitFor(() => expect(document.querySelectorAll('.sli-intervals-table tbody tr')).toHaveLength(110));
  const fetchMock = vi.mocked(fetch);
  const fixture = fetchMock.getMockImplementation()!;
  fetchMock.mockImplementation((input, init) => {
    const url = String(input);
    if (url.endsWith('/backfills')) {
      const bounds = {start:complete[0]!.interval_start, end:complete[0]!.interval_end};
      return Promise.resolve(json(init?.method === 'POST' ? {...bounds, chunk_count:0}
        : [{...bounds, id:'70000000-0000-4000-8000-000000000001', state:'completed',
            automatic:false, chunks:1, committed:1, gaps:0, requested_intervals:1, recovered_intervals:1}]));
    }
    if (url.includes('/observations')) return Promise.resolve(json({...fixtureHistory,items:complete}));
    return fixture(input, init);
  });
  await userEvent.click(screen.getByText('Historical backfill', {exact: true}));
  await screen.findByText(/1 \/ 1 intervals recovered/);
  await waitFor(() => expect(summary()).toHaveTextContent('60 / 60 min'));
  expect(document.querySelectorAll('.sli-intervals-table tbody tr')).toHaveLength(110);
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

it('loads a bounded 30-day measurements series independently of exact intervals', async () => {
  setup(complete);
  await screen.findByRole('heading', {name: 'Measurements'});
  await userEvent.click(screen.getByRole('radio', {name: '30d'}));
  await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([url]) =>
    String(url).endsWith('/measurements?period=30d'))).toBe(true));
  await screen.findByText('98.18%');
  expect(screen.getByRole('img', { name: /Good-to-total ratio/ })).toHaveTextContent('12 Oct');
  expect(screen.getByText(/outside available measurement history/)).toBeVisible();
  expect(document.querySelectorAll('.interval-state')).toHaveLength(3);
  expect(document.querySelectorAll('.interval-state.partial')).toHaveLength(1);
  expect(document.querySelectorAll('.gap')).toHaveLength(1);
  await userEvent.click(screen.getByRole('radio', {name: '1h'}));
  expect(document.querySelectorAll('.interval-state')).toHaveLength(60);
});

it('does not show unavailable history for the remainder of a non-dividing cadence', async () => {
  const end='2026-11-11T10:00:00Z';
  const start=new Date(Date.parse(end) - 635 * 17_000).toISOString();
  const buckets=[{start, end, good:'99', total:'100', observed:635, missing:0, zero_traffic:0, known_empty:0, failed:0, incomplete:0}];
  vi.stubGlobal('fetch',vi.fn().mockResolvedValue(json({bucket_seconds:51,buckets})));
  render(<SliMeasurementHistory id={sliID} period="3h" cadence={17} refresh={0} latest={null}/>);
  await screen.findByText('99.00%');
  expect(screen.queryByText(/outside available measurement history/)).not.toBeInTheDocument();
});

it('keeps a loaded chart through a pending and failed same-range refresh', async () => {
  const bucket = {start:'2026-11-11T07:00:00Z', end:'2026-11-11T10:00:00Z', good:'99', total:'100', observed:179, missing:1, zero_traffic:0, known_empty:0, failed:0, incomplete:0};
  const fetchMock = vi.fn().mockResolvedValueOnce(json({bucket_seconds:10800,buckets:[bucket]}));
  let finish!: (response: Response) => void;
  fetchMock.mockImplementationOnce(() => new Promise<Response>(resolve => {finish = resolve;}));
  vi.stubGlobal('fetch',fetchMock);
  const view=render(<SliMeasurementHistory id={sliID} period="30d" cadence={60} refresh={0} latest={null}/>);
  await screen.findByText('99.00%');
  expect(view.container.querySelector('.chart-point.partial')).toBeInTheDocument();
  view.rerender(<SliMeasurementHistory id={sliID} period="30d" cadence={60} refresh={1} latest={null}/>);
  expect(screen.getByRole('status')).toHaveTextContent('Loading measurements');
  expect(screen.getByText('99.00%')).toBeInTheDocument();
  finish(new Response('{}',{status:503}));
  await screen.findByRole('alert');
  expect(screen.getByText('99.00%')).toBeInTheDocument();
});

it('shows an exact pagination error beside recent intervals on a longer range', async () => {
  setup(complete,{older:true});
  const fetchMock=vi.mocked(fetch);
  const fetchFixture=fetchMock.getMockImplementation()!;
  fetchMock.mockImplementation((input,init) => String(input).includes('cursor=older')
    ? Promise.resolve(new Response('{}',{status:503}))
    : fetchFixture(input,init));
  await screen.findByRole('radio',{name:'30d'});
  await userEvent.click(screen.getByRole('radio',{name:'30d'}));
  await screen.findByText('98.18%');
  await userEvent.click(screen.getByRole('button',{name:'Recent intervals ↓'}));
  await userEvent.click(await screen.findByRole('button',{name:'Load more intervals'}));
  expect(await screen.findByRole('alert')).toHaveTextContent(/temporarily unavailable/);
  expect(document.querySelector('#sli-intervals [role="alert"]')).toBeInTheDocument();
  expect(screen.getByText('98.18%')).toBeInTheDocument();
});
