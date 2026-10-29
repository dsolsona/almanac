import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { OperationalOverviewView } from '../src/slos/OperationalViews.tsx';
import type { OperationalOverview } from '../src/slos/client.ts';

afterEach(()=>{cleanup();history.replaceState({},'','/');vi.unstubAllGlobals();});

it('shows server group counts and sends submitted filters through the operational endpoint',async()=>{
 const id='11111111-1111-1111-1111-111111111111';
 const fetcher=vi.fn(async()=>new Response(JSON.stringify({snapshot_id:id,items:[],group_counts:{needs_attention:2,watch:3,healthy:4},next_cursor:'',previous_cursor:''}),{status:200}));
 vi.stubGlobal('fetch',fetcher);
 history.replaceState({},'','/slos');
 const updateLocation=vi.fn();
 render(<OperationalOverviewView locationKey="/slos" canWrite={false} onCreate={()=>undefined} onLocationChange={updateLocation}/>);
 expect(await screen.findByRole('heading',{name:'Needs attention, 2 SLOs'})).toBeVisible();
 expect(screen.getByRole('heading',{name:'Watch, 3 SLOs'})).toBeVisible();
 expect(screen.getByRole('heading',{name:'Healthy, 4 SLOs'})).toBeVisible();
 fireEvent.change(screen.getByLabelText('Search SLOs'),{target:{value:'checkout'}});
 fireEvent.click(screen.getByRole('button',{name:'Search'}));
 await waitFor(()=>expect(updateLocation).toHaveBeenCalledWith(expect.stringContaining('q=checkout')));
});

it('uses the signed previous cursor from the same server snapshot',async()=>{
 const id='11111111-1111-1111-1111-111111111111';
 const fetcher=vi.fn(async(input:RequestInfo|URL)=>{const query=new URL(String(input),'http://localhost').searchParams;const second=query.get('cursor')==='next-token';return new Response(JSON.stringify({snapshot_id:id,items:[],group_counts:{needs_attention:0,watch:0,healthy:0},next_cursor:second?'':'next-token',previous_cursor:second?'signed-first-page-token':''}),{status:200});});
 vi.stubGlobal('fetch',fetcher);history.replaceState({},'','/slos');const onLocationChange=vi.fn();const props={canWrite:false,onCreate:()=>undefined,onLocationChange};
 const view=render(<OperationalOverviewView {...props} locationKey="/slos"/>);await screen.findByRole('heading',{name:'Needs attention, 0 SLOs'});expect(screen.getByRole('button',{name:'Previous page'})).toBeDisabled();fireEvent.click(screen.getByRole('button',{name:'Next page'}));expect(onLocationChange).toHaveBeenCalledWith('/slos?cursor=next-token');
 view.rerender(<OperationalOverviewView {...props} locationKey="/slos?cursor=next-token"/>);await waitFor(()=>expect(screen.getByRole('button',{name:'Previous page'})).toBeEnabled());fireEvent.click(screen.getByRole('button',{name:'Previous page'}));expect(onLocationChange).toHaveBeenLastCalledWith('/slos?cursor=signed-first-page-token');
});

it('marks retained overview values with their last confirmed evaluation after a same-view refresh fails',async()=>{
 const id='11111111-1111-1111-1111-111111111111',evaluatedAt='2026-10-29T12:00:00Z';let reads=0;
 const quality={state:'complete',freshness:'fresh',expected_intervals:1,observed_intervals:1,known_empty_intervals:0,missing_intervals:0,expired_intervals:0,zero_traffic_intervals:0,coverage:'1',first_observation_at:evaluatedAt,last_observation_at:evaluatedAt,latest_receipt_at:evaluatedAt,reason:null};
 const performance={state:'met',observed_state:'met',provisional:false,reason:null,good:'999',total:'1000',bad:'1',attainment:'0.999',allowed_budget:'100',remaining_budget:'90',consumed_fraction:'0.1',remaining_fraction:'0.9',burn:'0.1',budget_unit:'events'};
 const window={requested_start:'2026-10-28T12:00:00Z',effective_start:'2026-10-28T12:00:00Z',end:evaluatedAt,eligible_start:'2026-10-28T12:00:00Z',retention_start:'2025-09-24T12:00:00Z',activated_at:'2026-10-01T00:00:00Z',evaluation_valid_from:'2026-10-01T00:00:00Z',historical_requested_by:null,historical_requested_at:null};
 const item={slo:{id,name:'checkout',display_name:'Checkout',version:'1',definition_revision:'1',service_id:id,service_display_name:'Checkout service',sli_id:id,sli_display_name:'Request success',target:'0.99',rolling_window_seconds:86400,cadence_seconds:60,activated_at:'2026-10-01T00:00:00Z',evaluation_valid_from:'2026-10-01T00:00:00Z'},
  current:{evaluation_at:evaluatedAt,window,quality,performance},leading_reason:{code:'on_track',provisional:false},reasons:[{code:'on_track',provisional:false}],urgency:'healthy',
  burn_1h:{window_seconds:3600,start:'2026-10-29T11:00:00Z',end:evaluatedAt,quality,performance},burn_6h:{window_seconds:21600,start:'2026-10-29T06:00:00Z',end:evaluatedAt,quality,performance},
  exhaustion:{state:'unavailable',remaining_events:null,bad_events_per_hour:null,hours_until_exhaustion:null,current_quality:quality,one_hour_quality:quality,assumptions:[]},budget_sparkline:[]};
 const fetcher=vi.fn(async(input:RequestInfo|URL)=>{const path=new URL(String(input),'https://localhost').pathname;if(path==='/api/v1/services')return new Response(JSON.stringify({items:[],next_cursor:''}),{status:200});reads++;if(reads===1)return new Response(JSON.stringify({snapshot_id:id,items:[item],group_counts:{needs_attention:0,watch:0,healthy:1},next_cursor:'',previous_cursor:''} as unknown as OperationalOverview),{status:200});return new Response(JSON.stringify({message:'refresh failed'}),{status:503});});
 vi.stubGlobal('fetch',fetcher);history.replaceState({},'','/slos');const props={canWrite:false,onCreate:()=>undefined,onLocationChange:vi.fn()};
 render(<OperationalOverviewView {...props} locationKey="/slos"/>);expect(await screen.findByRole('link',{name:/^Checkout$/})).toBeVisible();fireEvent.click(screen.getByRole('button',{name:'Refresh'}));
 await waitFor(()=>expect(screen.getAllByRole('alert').some(alert=>alert.textContent?.includes('request could not be completed'))).toBe(true));const alert=screen.getAllByRole('alert').find(value=>value.textContent?.includes('request could not be completed'));expect(alert).toHaveTextContent(`Last confirmed evaluation: ${evaluatedAt}`);expect(screen.getByRole('link',{name:/^Checkout$/})).toBeVisible();
});
