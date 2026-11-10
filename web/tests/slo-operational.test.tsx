import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { OperationalDetailView, OperationalOverviewView } from '../src/slos/OperationalViews.tsx';
import { sloClient, SloFailure, type OperationalDetail, type OperationalOverview } from '../src/slos/client.ts';
import type { components } from '../src/api/generated.ts';
import * as telemetry from '../src/telemetry/client.ts';

afterEach(()=>{cleanup();history.replaceState({},'','/');vi.unstubAllGlobals();vi.restoreAllMocks();vi.useRealTimers();});
beforeEach(()=>{
 vi.stubGlobal('ResizeObserver',class {observe(){}unobserve(){}disconnect(){}});
 vi.spyOn(sloClient,'serverTime').mockResolvedValue(Date.parse('2026-10-08T00:00:00Z'));
});

const sloID='22222222-2222-2222-2222-222222222222';
const definition:components['schemas']['Slo']={id:sloID,name:'checkout',display_name:'Checkout',version:'1',definition_revision:'1',service_id:'11111111-1111-1111-1111-111111111111',service_display_name:'Checkout service',sli_id:'33333333-3333-3333-3333-333333333333',sli_display_name:'Request success',target:'0.99',rolling_window_seconds:86400,cadence_seconds:60,activated_at:'2026-10-01T00:00:00Z',evaluation_valid_from:'2026-10-01T00:00:00Z',historical_requested_by:null,historical_requested_at:null,created_at:'2026-10-01T00:00:00Z',updated_at:'2026-10-01T00:00:00Z'};
it('keeps last-known numbers visible with stale status, evaluation time and failed-update context',async()=>{
 vi.spyOn(sloClient,'services').mockResolvedValue({items:[],next_cursor:''});
 const detail=operationalDetail();
 const generation={generation_id:'66666666-6666-6666-6666-666666666666',source_generation:'1',definition_revision:'1',measurement_revision:'1',exclusion_revision:'0',calculation_version:'slo-v1',evaluated_through:detail.current.evaluation_at,published_at:detail.current.evaluation_at,calculation_lag_seconds:60};
 const item:OperationalOverview['items'][number]={slo:definition,current:detail.current,current_state:'stale',generation,urgency:'watch',leading_reason:detail.leading_reason,reasons:detail.reasons,burn_1h:detail.burn_1h,burn_6h:detail.burn_6h,exhaustion:detail.exhaustion,budget_sparkline:[]};
 const read=vi.spyOn(sloClient,'overview').mockResolvedValue({items:[item],snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:0,watch:1,healthy:0},next_cursor:'',previous_cursor:''});
 const view=render(<OperationalOverviewView locationKey="/slos" canWrite={false} onCreate={()=>undefined} onLocationChange={()=>undefined}/>);
 expect(await screen.findByText('Stale')).toBeVisible();
 expect(document.querySelector('.app-slo-budget b')).toHaveTextContent('99%');
 expect(document.querySelector('.app-slo-burn b')).toHaveTextContent('0.1×');
 expect(document.querySelector('time')).toHaveAttribute('datetime',detail.current.evaluation_at);
 expect(document.querySelector('.slo-reference-status')).toHaveAttribute('data-status','stale');
 read.mockResolvedValue({items:[{...item,current_state:'failed'}],snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:0,watch:1,healthy:0},next_cursor:'',previous_cursor:''});
 fireEvent.click(screen.getByRole('button',{name:'Refresh SLOs'}));
 expect(await screen.findByText(/Update failed/)).toBeVisible();
 expect(document.querySelector('.app-slo-budget b')).toHaveTextContent('99%');
 view.unmount();
});
it('keeps graphs and current status usable while history is incomplete and refreshes progress',async()=>{
 const detail=operationalDetail();
 detail.history_preparation={ready_seconds:43200,total_seconds:86400,failed:false};
 const read=vi.spyOn(sloClient,'operational').mockResolvedValueOnce(detail).mockResolvedValue({...detail,history_preparation:undefined});
 const stamp={generation_id:'66666666-6666-6666-6666-666666666666',source_generation:'1',definition_revision:'1',measurement_revision:'1',exclusion_revision:'0',calculation_version:'slo-v1',evaluated_through:detail.effective_at,published_at:detail.effective_at,calculation_lag_seconds:0};
 vi.spyOn(sloClient,'current').mockResolvedValue({slo:definition,current:detail.current,burn_1h:detail.burn_1h,burn_6h:detail.burn_6h,urgency:detail.urgency,leading_reason:detail.leading_reason,reasons:detail.reasons,exhaustion:detail.exhaustion,generation:stamp});
 render(<OperationalDetailView id={sloID} definition={definition} locationKey={`/slos/${sloID}`} onLocationChange={()=>undefined}/>);
 expect(await screen.findByText(/50% of this period’s history is ready/)).toBeVisible();
 expect(screen.getByText('error budget remaining')).toBeVisible();
 expect(screen.getByRole('button',{name:'View SLI as table'})).toBeVisible();
 fireEvent.click(screen.getByRole('button',{name:'Refresh objective history'}));
 await waitFor(()=>expect(screen.queryByText(/of this period’s history is ready/)).not.toBeInTheDocument());
 expect(read).toHaveBeenCalledTimes(2);
});
it('signals timezone-only readiness without refetching history or waiting for a status poll',async()=>{
 const detail=operationalDetail();
 const generation={generation_id:'66666666-6666-6666-6666-666666666666',source_generation:'1',definition_revision:'1',measurement_revision:'1',exclusion_revision:'0',calculation_version:'slo-v1',evaluated_through:detail.effective_at,published_at:detail.effective_at,calculation_lag_seconds:0};
 detail.generation=generation;
 const ready=vi.spyOn(telemetry,'journeyContentReady');
 const historyRead=vi.spyOn(sloClient,'operational').mockResolvedValue(detail);
 const currentRead=vi.spyOn(sloClient,'current').mockResolvedValue({slo:definition,current:detail.current,burn_1h:detail.burn_1h,burn_6h:detail.burn_6h,urgency:detail.urgency,leading_reason:detail.leading_reason,reasons:detail.reasons,exhaustion:detail.exhaustion,generation});
 const view=render(<OperationalDetailView id={sloID} definition={definition} locationKey={`/slos/${sloID}`} onLocationChange={()=>undefined}/>);
 await waitFor(()=>expect(ready).toHaveBeenCalledWith('slo.detail',false));
 ready.mockClear();
 const historyCalls=historyRead.mock.calls.length,currentCalls=currentRead.mock.calls.length;
 view.rerender(<OperationalDetailView id={sloID} definition={definition} locationKey={`/slos/${sloID}?tz=local`} onLocationChange={()=>undefined}/>);
 await waitFor(()=>expect(ready).toHaveBeenCalledWith('slo.detail',false));
 expect(historyRead).toHaveBeenCalledTimes(historyCalls);
 expect(currentRead).toHaveBeenCalledTimes(currentCalls);
});
function operationalDetail(requestedStart='2026-10-01T00:00:00Z',requestedEnd='2026-10-08T00:00:00Z'):OperationalDetail {
 const start='2026-10-01T00:00:00Z',end='2026-10-08T00:00:00Z';
 const quality={state:'complete',freshness:'fresh',expected_intervals:1,observed_intervals:1,known_empty_intervals:0,missing_intervals:0,expired_intervals:0,zero_traffic_intervals:0,coverage:'1',first_observation_at:start,last_observation_at:end,latest_receipt_at:end,reason:null};
 const performance={state:'met',observed_state:'met',provisional:false,reason:null,good:'999',total:'1000',bad:'1',attainment:'0.999',allowed_budget:'100',remaining_budget:'99',consumed_fraction:'0.01',remaining_fraction:'0.99',burn:'0.1',budget_unit:'events'};
 const current={evaluation_at:end,window:{requested_start:start,effective_start:start,end,eligible_start:start,retention_start:start,activated_at:start,evaluation_valid_from:start,historical_requested_by:null,historical_requested_at:null},quality,performance};
 const empty=(kind:'attainment'|'rolling_budget'|'burn',burn_window_seconds:3600|21600|null)=>({kind,burn_window_seconds,samples:[]});
 return {evaluation_id:'44444444-4444-4444-4444-444444444444',requested_at:end,effective_at:end,calculation_version:'slo-v1',snapshot_kind:'current_operational_read',objective:definition,context:{sli_id:definition.sli_id,sli_name:'request_success',sli_display_name:definition.sli_display_name,datasource_id:'55555555-5555-5555-5555-555555555555',datasource_display_name:'Metrics',measurement_revision:'1',polling_interval_seconds:60,settling_delay_seconds:60,good_query:'sum(good)',total_query:'sum(total)'},current,urgency:'healthy',leading_reason:{code:'on_track',provisional:false},reasons:[{code:'on_track',provisional:false}],burn_1h:{window_seconds:3600,start:'2026-10-07T23:00:00Z',end,quality,performance},burn_6h:{window_seconds:21600,start:'2026-10-07T18:00:00Z',end,quality,performance},exhaustion:{state:'unavailable',remaining_events:'99',bad_events_per_hour:'0.1',hours_until_exhaustion:null,current_quality:quality,one_hour_quality:quality,assumptions:[],reason:'No finite estimate'},range:{requested_start:requestedStart,requested_end:requestedEnd,effective_start:requestedStart,effective_end:requestedEnd,source_cadence_seconds:60,resolution_seconds:60,reduced:false,max_samples_per_series:1500,bucket_bounds:'half_open'},attainment_series:empty('attainment',null),budget_series:empty('rolling_budget',null),burn_series:empty('burn',3600),compliance_series:{kind:'daily_compliance',days:[]},compliance_summary:{met_days:0,violated_days:0,evaluable_days:0,unknown_days:0,not_applicable_days:0}} as unknown as OperationalDetail;
}

it('shows server group counts and sends submitted filters through the operational endpoint',async()=>{
 const detail=operationalDetail();
 const items=['needs_attention','watch','healthy'].map((urgency,index)=>({
  slo:{...definition,id:`11111111-1111-1111-1111-11111111111${index}`},current:detail.current,
  urgency,leading_reason:detail.leading_reason,reasons:detail.reasons,
  burn_1h:detail.burn_1h,burn_6h:detail.burn_6h,exhaustion:detail.exhaustion,budget_sparkline:[],
 }));
 const fetcher=vi.fn(async(input:RequestInfo|URL)=>new Response(JSON.stringify(new URL(String(input),'https://localhost').pathname==='/api/v1/services'?{items:[],next_cursor:''}:{items,snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:2,watch:3,healthy:4},next_cursor:'',previous_cursor:''}),{status:200}));
 vi.stubGlobal('fetch',fetcher);
 history.replaceState({},'','/slos');
 const updateLocation=vi.fn();
 render(<OperationalOverviewView locationKey="/slos" canWrite={false} onCreate={()=>undefined} onLocationChange={updateLocation}/>);
 expect(await screen.findByRole('heading',{name:'Needs attention, 2 SLOs'})).toBeVisible();
 expect(String(fetcher.mock.calls.find(call=>String(call[0]).includes('/slos/overview'))?.[0])).toContain('limit=20');
 expect(screen.getByLabelText('Per page')).toHaveValue('20');
 expect(screen.getByRole('heading',{name:'Watch, 3 SLOs'})).toBeVisible();
 expect(screen.getByRole('heading',{name:'Healthy, 4 SLOs'})).toBeVisible();
 fireEvent.change(screen.getByLabelText('Search SLOs'),{target:{value:'checkout'}});
 fireEvent.submit(screen.getByRole('search'));
 await waitFor(()=>expect(updateLocation).toHaveBeenCalledWith(expect.stringContaining('q=checkout')));
 fireEvent.change(screen.getByLabelText('Per page'),{target:{value:'10'}});
 expect(updateLocation).toHaveBeenCalledWith(expect.stringContaining('page_size=10'));
});

it('never polls preparing first pages and leaves cursor pages pinned until explicit navigation',async()=>{
 vi.useFakeTimers();
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({items:[],next_cursor:''}),{status:200})));
 const detail=operationalDetail();
 const item={slo:definition,current:detail.current,urgency:detail.urgency,leading_reason:detail.leading_reason,reasons:detail.reasons,burn_1h:detail.burn_1h,burn_6h:detail.burn_6h,exhaustion:detail.exhaustion,budget_sparkline:[],budget_history_state:'preparing' as const};
 const read=vi.spyOn(sloClient,'overview').mockResolvedValue({items:[item],snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:0,watch:0,healthy:1},next_cursor:'page-two',previous_cursor:''});
 const view=render(<OperationalOverviewView locationKey="/slos" canWrite={false} onCreate={()=>undefined} onLocationChange={()=>undefined}/>);
 await act(async()=>{});
 expect(read).toHaveBeenCalledTimes(1);
 await act(async()=>{await vi.advanceTimersByTimeAsync(60000);});
 expect(read).toHaveBeenCalledTimes(1);
 fireEvent.click(screen.getByRole('button',{name:'Refresh SLOs'}));
 await act(async()=>{});
 expect(read).toHaveBeenCalledTimes(2);
 view.rerender(<OperationalOverviewView locationKey="/slos?cursor=page-two" canWrite={false} onCreate={()=>undefined} onLocationChange={()=>undefined}/>);
 await act(async()=>{});
 expect(read).toHaveBeenCalledTimes(3);
 await act(async()=>{await vi.advanceTimersByTimeAsync(60000);});
 expect(read).toHaveBeenCalledTimes(3);
 expect(read.mock.calls[2]?.[0]).toMatchObject({cursor:'page-two'});
});

it('opens exact values from the generation displayed by the timeline',async()=>{
 const detail=operationalDetail();
 const stamp={generation_id:'66666666-6666-6666-6666-666666666666',source_generation:'1',definition_revision:'1',measurement_revision:'1',exclusion_revision:'0',calculation_version:'slo-v1',evaluated_through:detail.effective_at,published_at:detail.effective_at,calculation_lag_seconds:0};
 detail.generation=stamp;
 vi.spyOn(sloClient,'operational').mockResolvedValue(detail);
 vi.spyOn(sloClient,'current').mockResolvedValue({slo:definition,current:detail.current,burn_1h:detail.burn_1h,burn_6h:detail.burn_6h,urgency:detail.urgency,leading_reason:detail.leading_reason,reasons:detail.reasons,exhaustion:detail.exhaustion,generation:stamp});
 const values=vi.spyOn(sloClient,'values').mockResolvedValue({generation:stamp,lane:'attainment',offset:0,next_offset:null,total_values:0,samples:[],days:[]});
 render(<OperationalDetailView id={sloID} definition={definition} locationKey={`/slos/${sloID}`} onLocationChange={()=>undefined}/>);
 fireEvent.click(await screen.findByRole('button',{name:'View SLI as table'}));
 expect(await screen.findByText('No samples are available for this range.')).toBeVisible();
 expect(values).toHaveBeenCalledWith(sloID,stamp.generation_id,'attainment',0,expect.any(AbortSignal));
});

it('clears confirmed data on revocation and rejects late independent history completion',async()=>{
 const detail=operationalDetail();
 const stamp={generation_id:'66666666-6666-6666-6666-666666666666',source_generation:'1',definition_revision:'1',measurement_revision:'1',exclusion_revision:'0',calculation_version:'slo-v1',evaluated_through:detail.effective_at,published_at:detail.effective_at,calculation_lag_seconds:0};
 vi.spyOn(sloClient,'current').mockResolvedValueOnce({slo:definition,current:detail.current,burn_1h:detail.burn_1h,burn_6h:detail.burn_6h,urgency:detail.urgency,leading_reason:detail.leading_reason,reasons:detail.reasons,exhaustion:detail.exhaustion,generation:stamp}).mockRejectedValue(new SloFailure('Session revoked',false,401));
 let complete!:(value:OperationalDetail)=>void;
 const history=vi.spyOn(sloClient,'operational').mockResolvedValueOnce(detail).mockImplementation(()=>new Promise(resolve=>{complete=resolve;}));
 const revoked=vi.fn(),props={id:sloID,definition,locationKey:`/slos/${sloID}`,onLocationChange:()=>undefined,onSessionFailure:revoked};
 const view=render(<OperationalDetailView {...props}/>);
 expect(await screen.findByText('error budget remaining')).toBeVisible();
 view.rerender(<OperationalDetailView {...props} refreshVersion={1}/>);
 await waitFor(()=>expect(revoked).toHaveBeenCalled());
 await waitFor(()=>expect(history).toHaveBeenCalledTimes(2));
 await act(async()=>{complete(detail);});
 expect(screen.queryByText('error budget remaining')).not.toBeInTheDocument();
});

it('renders the SLO list while service filters are still loading',async()=>{
 vi.spyOn(sloClient,'services').mockImplementation(()=>new Promise(()=>undefined));
 vi.spyOn(sloClient,'overview').mockResolvedValue({items:[],snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:0,watch:0,healthy:0},next_cursor:'',previous_cursor:''} as OperationalOverview);
 render(<OperationalOverviewView locationKey="/slos" canWrite={false} onCreate={()=>undefined} onLocationChange={()=>undefined}/>);
 expect(await screen.findByRole('heading',{name:'No SLOs yet'})).toBeVisible();
});

it('shows one empty-state message without empty urgency tables or false pagination',async()=>{
 const fetcher=vi.fn(async(input:RequestInfo|URL)=>{
  const path=new URL(String(input),'https://localhost').pathname;
  return path==='/api/v1/services'
   ? new Response(JSON.stringify({items:[],next_cursor:''}),{status:200})
   : new Response(JSON.stringify({items:[],snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:0,watch:0,healthy:0},next_cursor:'',previous_cursor:''}),{status:200});
 });
 vi.stubGlobal('fetch',fetcher);history.replaceState({},'','/slos');
 render(<OperationalOverviewView locationKey="/slos" canWrite={false} onCreate={()=>undefined} onLocationChange={()=>undefined}/>);
 expect(await screen.findByRole('heading',{name:'No SLOs yet'})).toBeVisible();
 expect(screen.queryByRole('table')).not.toBeInTheDocument();
 expect(screen.queryByRole('navigation',{name:'SLO result pages'})).not.toBeInTheDocument();
});

it('uses the previous keyset cursor returned by the server',async()=>{
 const fetcher=vi.fn(async(input:RequestInfo|URL)=>{const query=new URL(String(input),'http://localhost').searchParams;const second=query.get('cursor')==='next-token';return new Response(JSON.stringify({items:[],snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:0,watch:0,healthy:0},next_cursor:second?'':'next-token',previous_cursor:second?'signed-first-page-token':''}),{status:200});});
 vi.stubGlobal('fetch',fetcher);history.replaceState({},'','/slos');const onLocationChange=vi.fn();const props={canWrite:false,onCreate:()=>undefined,onLocationChange};
 const view=render(<OperationalOverviewView {...props} locationKey="/slos"/>);await screen.findByRole('heading',{name:'No SLOs yet'});expect(screen.getByRole('button',{name:'‹ Previous'})).toBeDisabled();fireEvent.click(screen.getByRole('button',{name:'Next ›'}));expect(onLocationChange).toHaveBeenCalledWith('/slos?cursor=next-token');
 view.rerender(<OperationalOverviewView {...props} locationKey="/slos?cursor=next-token"/>);await waitFor(()=>expect(screen.getByRole('button',{name:'‹ Previous'})).toBeEnabled());fireEvent.click(screen.getByRole('button',{name:'‹ Previous'}));expect(onLocationChange).toHaveBeenLastCalledWith('/slos?cursor=signed-first-page-token');
});

it('refreshes a later overview page from the first page and retains active filters',async()=>{
 const seen:URLSearchParams[]=[];
 const fetcher=vi.fn(async(input:RequestInfo|URL)=>{const url=new URL(String(input),'https://localhost');if(url.pathname==='/api/v1/services')return new Response(JSON.stringify({items:[],next_cursor:''}),{status:200});seen.push(url.searchParams);return new Response(JSON.stringify({items:[],snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:0,watch:0,healthy:0},next_cursor:'next-page',previous_cursor:'previous-page'}),{status:200});});
 vi.stubGlobal('fetch',fetcher);const filters='q=checkout&service_id=55555555-5555-5555-5555-555555555555&urgency=watch';const later=`/slos?${filters}&cursor=page-two&previous_cursor=page-one`;history.replaceState({},'',later);const onLocationChange=vi.fn();const props={canWrite:false,onCreate:()=>undefined,onLocationChange};
 const view=render(<OperationalOverviewView {...props} locationKey={later}/>);await screen.findByText('No SLOs match these filters.');fireEvent.click(screen.getByRole('button',{name:'Refresh SLOs'}));const first=`/slos?${filters}`;expect(onLocationChange).toHaveBeenCalledWith(first);view.rerender(<OperationalOverviewView {...props} locationKey={first}/>);await waitFor(()=>expect(seen).toHaveLength(2));expect(seen[0]?.get('cursor')).toBe('page-two');expect(seen[1]?.has('cursor')).toBe(false);expect(seen[1]?.get('q')).toBe('checkout');expect(seen[1]?.get('service_id')).toBe('55555555-5555-5555-5555-555555555555');expect(seen[1]?.get('urgency')).toBe('watch');
});

it('keeps retry and range controls available after the first detail read fails, then recovers on a new range',async()=>{
 const read=vi.spyOn(sloClient,'operational').mockRejectedValueOnce(new Error('temporary read failure')).mockRejectedValueOnce(new Error('temporary retry failure')).mockImplementation((_id,options)=>Promise.resolve(operationalDetail(options?.start,options?.end)));
 const locationKey=`/slos/${sloID}`;history.replaceState({},'',locationKey);let view!:ReturnType<typeof render>;const onLocationChange=(path:string)=>{history.replaceState({},'',path);view.rerender(<OperationalDetailView id={sloID} definition={definition} locationKey={path} onLocationChange={onLocationChange}/>);};
 view=render(<OperationalDetailView id={sloID} definition={definition} locationKey={locationKey} onLocationChange={onLocationChange}/>);expect(await screen.findByRole('alert')).toHaveTextContent('temporary read failure');expect(screen.getByRole('button',{name:'Refresh objective history'})).toBeEnabled();expect(screen.getByRole('button',{name:'90 d'})).toBeEnabled();expect(screen.getByRole('button',{name:'90 d'})).toBeEnabled();
 fireEvent.click(screen.getByRole('button',{name:'Refresh objective history'}));await waitFor(()=>expect(read).toHaveBeenCalledTimes(2));expect(await screen.findByRole('alert')).toHaveTextContent('temporary retry failure');fireEvent.click(screen.getByRole('button',{name:'90 d'}));await waitFor(()=>expect(read).toHaveBeenCalledTimes(3));expect(read.mock.calls[2]?.[1]).toMatchObject({period:'90d',burn_window_seconds:3600});expect(await screen.findByText('Bad events left')).toBeVisible();expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});

it('marks retained overview values with their last confirmed evaluation after a same-view refresh fails',async()=>{
 const id='11111111-1111-1111-1111-111111111111',evaluatedAt='2026-10-29T12:00:00Z';let reads=0;
 const quality={state:'complete',freshness:'stale',expected_intervals:1,observed_intervals:1,known_empty_intervals:0,missing_intervals:0,expired_intervals:0,zero_traffic_intervals:0,coverage:'1',first_observation_at:evaluatedAt,last_observation_at:evaluatedAt,latest_receipt_at:evaluatedAt,reason:null};
 const performance={state:'violated',observed_state:'violated',provisional:false,reason:null,good:'980',total:'1000',bad:'20',attainment:'0.98',allowed_budget:'10',remaining_budget:'-10',consumed_fraction:'2',remaining_fraction:'-1',burn:'2',budget_unit:'events'};
 const window={requested_start:'2026-10-28T12:00:00Z',effective_start:'2026-10-28T12:00:00Z',end:evaluatedAt,eligible_start:'2026-10-28T12:00:00Z',retention_start:'2025-09-24T12:00:00Z',activated_at:'2026-10-01T00:00:00Z',evaluation_valid_from:'2026-10-01T00:00:00Z',historical_requested_by:null,historical_requested_at:null};
 const item={slo:{id,name:'checkout',display_name:'Checkout',version:'1',definition_revision:'1',service_id:id,service_display_name:'Checkout service',sli_id:id,sli_display_name:'Request success',target:'0.99',rolling_window_seconds:86400,cadence_seconds:60,activated_at:'2026-10-01T00:00:00Z',evaluation_valid_from:'2026-10-01T00:00:00Z'},
  current:{evaluation_at:evaluatedAt,window,quality,performance},leading_reason:{code:'stale_observations',provisional:false},reasons:[{code:'stale_observations',provisional:false},{code:'complete_window_violation',provisional:false}],urgency:'needs_attention',
  burn_1h:{window_seconds:3600,start:'2026-10-29T11:00:00Z',end:evaluatedAt,quality,performance},burn_6h:{window_seconds:21600,start:'2026-10-29T06:00:00Z',end:evaluatedAt,quality,performance},
  exhaustion:{state:'unavailable',remaining_events:null,bad_events_per_hour:null,hours_until_exhaustion:null,current_quality:quality,one_hour_quality:quality,assumptions:[]},budget_sparkline:[]};
 const fetcher=vi.fn(async(input:RequestInfo|URL)=>{const path=new URL(String(input),'https://localhost').pathname;if(path==='/api/v1/services')return new Response(JSON.stringify({items:[],next_cursor:''}),{status:200});reads++;if(reads===1)return new Response(JSON.stringify({items:[item],snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:1,watch:0,healthy:0},next_cursor:'',previous_cursor:''} as unknown as OperationalOverview),{status:200});return new Response(JSON.stringify({message:'refresh failed'}),{status:503});});
 vi.stubGlobal('fetch',fetcher);history.replaceState({},'','/slos');const props={canWrite:false,onCreate:()=>undefined,onLocationChange:vi.fn()};
 render(<OperationalOverviewView {...props} locationKey="/slos"/>);expect(await screen.findByRole('link',{name:/^Checkout$/})).toBeVisible();expect(document.querySelector('.slo-reference-status')).toHaveAttribute('title','Observations are stale; Complete window is below target');fireEvent.click(screen.getByRole('button',{name:'Refresh SLOs'}));
 expect(document.querySelector('.app-slo-status .ui-status-chip')).toHaveTextContent(/^Not reporting$/);expect(screen.getByText('Observations are stale; Complete window is below target')).toBeInTheDocument();
 await waitFor(()=>expect(screen.getAllByRole('alert').some(alert=>alert.textContent?.includes('request could not be completed'))).toBe(true));const alert=screen.getAllByRole('alert').find(value=>value.textContent?.includes('request could not be completed'));expect(alert).toHaveTextContent(`Last confirmed evaluation: ${evaluatedAt}`);expect(screen.getByRole('link',{name:/^Checkout$/})).toBeVisible();
});

it('triage tiles show server group counts, toggle the urgency filter and never label a last-known row Healthy',async()=>{
 const detail=operationalDetail();
 const generation={generation_id:'66666666-6666-6666-6666-666666666666',source_generation:'1',definition_revision:'1',measurement_revision:'1',exclusion_revision:'0',calculation_version:'slo-v1',evaluated_through:detail.current.evaluation_at,published_at:detail.current.evaluation_at,calculation_lag_seconds:60};
 const item:OperationalOverview['items'][number]={slo:definition,current:detail.current,current_state:'stale',generation,urgency:'watch',leading_reason:{code:'budget_below_half',provisional:false},reasons:[{code:'budget_below_half',provisional:false}],burn_1h:detail.burn_1h,burn_6h:detail.burn_6h,exhaustion:detail.exhaustion,budget_sparkline:[]};
 vi.spyOn(sloClient,'services').mockResolvedValue({items:[],next_cursor:''});
 vi.spyOn(sloClient,'overview').mockResolvedValue({items:[item],snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:2,watch:1,healthy:4},next_cursor:'',previous_cursor:''});
 const updateLocation=vi.fn();
 render(<OperationalOverviewView locationKey="/slos" canWrite={false} onCreate={()=>undefined} onLocationChange={updateLocation}/>);
 const tiles=await screen.findByRole('group',{name:'Filter by urgency'});
 expect(tiles).toHaveTextContent('2');expect(tiles).toHaveTextContent('Needs attention');
 fireEvent.click(screen.getByRole('button',{name:/^2\s*Needs attention/}));
 expect(updateLocation).toHaveBeenCalledWith('/slos?urgency=needs_attention');
 expect(document.querySelector('.app-slo-status')).toHaveTextContent('Stale');
 expect(document.querySelector('.app-slo-status')).not.toHaveTextContent('Healthy');
 expect(document.querySelector('.app-slo-objective')).toHaveTextContent('99% · 1 d');
});
it('names the status from the leading reason and keeps every reason in the title',async()=>{
 const detail=operationalDetail();
 const items=[
  {code:'high_1h_burn',label:'Burning fast'},{code:'complete_window_violation',label:'Breached'},{code:'stale_observations',label:'Not reporting'},{code:'low_observed_budget',label:'Low budget'},
 ].map((entry,index)=>({slo:{...definition,id:`11111111-1111-1111-1111-11111111111${index}`,display_name:`SLO ${index}`},current:detail.current,urgency:'needs_attention' as const,leading_reason:{code:entry.code as never,provisional:false},reasons:[{code:entry.code as never,provisional:false},{code:'budget_below_half' as const,provisional:false}],burn_1h:detail.burn_1h,burn_6h:detail.burn_6h,exhaustion:detail.exhaustion,budget_sparkline:[]}));
 vi.spyOn(sloClient,'services').mockResolvedValue({items:[],next_cursor:''});
 vi.spyOn(sloClient,'overview').mockResolvedValue({items,snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:4,watch:0,healthy:0},next_cursor:'',previous_cursor:''});
 render(<OperationalOverviewView locationKey="/slos" canWrite={false} onCreate={()=>undefined} onLocationChange={()=>undefined}/>);
 await screen.findByRole('link',{name:'SLO 0'});
 const statuses=[...document.querySelectorAll('.app-slo-row .slo-reference-status')];
 expect(statuses.map(node=>node.querySelector('.ui-status-chip')?.textContent)).toEqual(['Burning fast','Breached','Not reporting','Low budget']);
 expect(statuses[0]).toHaveAttribute('title','1 h burn is at least 1×; Less than half of budget remains');
 // ADR-0032: the status cell is the chip only; the reasons stay in its tooltip.
 expect(statuses[0]?.textContent).toBe('Burning fast');
});
it('opens an SLO from anywhere on its row unless text is selected or an inner link is clicked',async()=>{
 const detail=operationalDetail();
 const item={slo:definition,current:detail.current,urgency:'watch' as const,leading_reason:detail.leading_reason,reasons:detail.reasons,burn_1h:detail.burn_1h,burn_6h:detail.burn_6h,exhaustion:detail.exhaustion,budget_sparkline:[]} as unknown as OperationalOverview['items'][number];
 vi.spyOn(sloClient,'services').mockResolvedValue({items:[],next_cursor:''});
 vi.spyOn(sloClient,'overview').mockResolvedValue({items:[item],snapshot_id:'11111111-1111-4111-8111-111111111111',group_counts:{needs_attention:0,watch:1,healthy:0},next_cursor:'',previous_cursor:''});
 const open=vi.fn();
 render(<OperationalOverviewView locationKey="/slos" canWrite={false} onCreate={()=>undefined} onLocationChange={open}/>);
 await screen.findByRole('link',{name:'Checkout'});
 const objective=document.querySelector('.app-slo-objective')!;
 fireEvent.click(objective);
 expect(open).toHaveBeenLastCalledWith(`/slos/${sloID}`);
 open.mockClear();
 const selection=vi.spyOn(window,'getSelection').mockReturnValue({toString:()=>'99.9%'} as Selection);
 fireEvent.click(objective);
 expect(open).not.toHaveBeenCalled();
 selection.mockRestore();
 fireEvent.click(screen.getByRole('link',{name:'Checkout service'}));
 expect(open).not.toHaveBeenCalled();
 fireEvent.click(objective,{metaKey:true});
 expect(open).not.toHaveBeenCalled();
});
