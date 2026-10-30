import { afterEach, expect, it, vi } from 'vitest';
import { sloClient, SloFailure } from '../src/slos/client.ts';
import type { AuthContext } from '../src/auth/client.ts';
import type { components } from '../src/api/generated.ts';
const context:AuthContext={csrf_token:'csrf',session:{user_id:'u',login:'editor',role:'editor',restricted:false}};
const id='11111111-1111-1111-1111-111111111111';
const service={id,name:'checkout',display_name:'Checkout',version:'1',created_at:'2026-01-01T00:00:00Z',updated_at:'2026-01-01T00:00:00Z'};
const instant='2026-10-29T12:00:00Z';
function operationalDetail(objectiveID=id):components['schemas']['SloOperationalDetail'] {
 const quality:components['schemas']['SloOperationalQuality']={state:'complete',freshness:'fresh',expected_intervals:1,observed_intervals:1,known_empty_intervals:0,missing_intervals:0,expired_intervals:0,zero_traffic_intervals:0,coverage:'1',first_observation_at:instant,last_observation_at:instant,latest_receipt_at:instant,reason:null};
 const performance:components['schemas']['SloEvaluationPerformance']={state:'met',observed_state:'met',provisional:false,reason:null,good:'999',total:'1000',bad:'1',attainment:'0.999',allowed_budget:'100',remaining_budget:'90',consumed_fraction:'0.1',remaining_fraction:'0.9',burn:'0.1',budget_unit:'events'};
 const objective:components['schemas']['Slo']={id:objectiveID,name:'checkout',display_name:'Checkout',version:'1',definition_revision:'1',service_id:id,service_display_name:'Checkout service',sli_id:id,sli_display_name:'Request success',target:'0.99',rolling_window_seconds:86400,cadence_seconds:60,activated_at:'2026-10-01T00:00:00Z',evaluation_valid_from:'2026-10-01T00:00:00Z',historical_requested_by:null,historical_requested_at:null,created_at:'2026-10-01T00:00:00Z',updated_at:'2026-10-01T00:00:00Z'};
 const window:components['schemas']['SloEvaluationWindow']={requested_start:'2026-10-28T12:00:00Z',effective_start:'2026-10-28T12:00:00Z',end:instant,eligible_start:'2026-10-28T12:00:00Z',retention_start:'2025-09-24T12:00:00Z',activated_at:objective.activated_at,evaluation_valid_from:objective.evaluation_valid_from,historical_requested_by:null,historical_requested_at:null};
 const value:components['schemas']['SloOperationalValue']={evaluation_at:instant,window,quality,performance};
 const burn=(windowSeconds:3600|21600):components['schemas']['SloBurnValue']=>({window_seconds:windowSeconds,start:windowSeconds===3600?'2026-10-29T11:00:00Z':'2026-10-29T06:00:00Z',end:instant,quality,performance});
 const series=(kind:components['schemas']['SloOperationalSeries']['kind'],windowSeconds:3600|21600|null):components['schemas']['SloOperationalSeries']=>({kind,burn_window_seconds:windowSeconds,samples:[]});
 const dailyQuality={...quality,expected_intervals:1440,observed_intervals:1440,first_observation_at:'2026-10-22T00:00:00Z'};
 const days=Array.from({length:7},(_,index)=>{const dayEnd=new Date(Date.parse('2026-10-23T00:00:00Z')+index*86_400_000).toISOString(),dayStart=new Date(Date.parse(dayEnd)-86_400_000).toISOString();return {utc_date:dayStart.slice(0,10),day_start:dayStart,day_end:dayEnd,requested_at:dayEnd,effective_at:dayEnd,slo_id:objectiveID,definition_revision:objective.definition_revision,target:objective.target,quality:dailyQuality,performance,status:'met' as const,explanation:'target_met'};});
 return {evaluation_id:'22222222-2222-2222-2222-222222222222',requested_at:instant,effective_at:instant,calculation_version:'1',snapshot_kind:'current_operational_read',objective,context:{sli_id:id,sli_name:'request-success',sli_display_name:'Request success',datasource_id:'33333333-3333-3333-3333-333333333333',datasource_display_name:'Metrics',measurement_revision:'1',polling_interval_seconds:60,settling_delay_seconds:0,good_query:'good',total_query:'total'},current:value,urgency:'healthy',leading_reason:{code:'on_track',provisional:false},reasons:[{code:'on_track',provisional:false}],burn_1h:burn(3600),burn_6h:burn(21600),exhaustion:{state:'unavailable',remaining_events:null,bad_events_per_hour:null,hours_until_exhaustion:null,current_quality:quality,one_hour_quality:quality,assumptions:[]},range:{requested_start:'2026-10-22T12:00:00Z',requested_end:instant,effective_start:'2026-10-22T12:00:00Z',effective_end:instant,source_cadence_seconds:60,resolution_seconds:60,reduced:false,max_samples_per_series:1500,bucket_bounds:'half_open'},attainment_series:series('attainment',null),budget_series:series('rolling_budget',null),burn_series:series('burn',3600),compliance_series:{kind:'daily_compliance',days},compliance_summary:{met_days:7,violated_days:0,evaluable_days:7,unknown_days:0,not_applicable_days:0}};
}
afterEach(()=>vi.unstubAllGlobals());
it('uses the lightweight definition response Date for server-anchored history bounds',async()=>{
 const fetcher=vi.fn(async(_input:RequestInfo|URL)=>new Response(JSON.stringify(operationalDetail().objective),{status:200,headers:{ETag:`"slo:${id}:1"`,Date:'Thu, 29 Oct 2026 12:00:00 GMT'}}));
 vi.stubGlobal('fetch',fetcher);
 await expect(sloClient.serverTime(id)).resolves.toBe(Date.parse(instant));
 expect(fetcher.mock.calls).toHaveLength(1);
 expect(String(fetcher.mock.calls[0]?.[0])).toBe(`/api/v1/slos/${id}`);
});
it('rejects malformed list and evaluation responses rather than treating them as empty',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({items:'not a page'}),{status:200}))); await expect(sloClient.list()).rejects.toThrow('invalid response');
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({window:{},quality:{},performance:{},timeline:{items:[]}}),{status:200}))); await expect(sloClient.evaluation(id)).rejects.toThrow('invalid response');
});
it('does not accept a missing or mismatched resource ETag after create',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(service),{status:201,headers:{ETag:`"service:${id}:2"`}})));
 try { await sloClient.createService({name:'checkout',display_name:'Checkout'},context,'key'); throw new Error('expected failure'); } catch(e) {expect(e).toBeInstanceOf(SloFailure);expect((e as SloFailure).uncertain).toBe(true);}
});
it('accepts operational detail without a resource ETag but still validates its ID and resource ETags',async()=>{
 const missingTag=vi.fn()
  .mockResolvedValueOnce(new Response(JSON.stringify(operationalDetail()),{status:200}))
  .mockResolvedValueOnce(new Response(JSON.stringify(operationalDetail('22222222-2222-2222-2222-222222222222')),{status:200}))
  .mockResolvedValueOnce(new Response(JSON.stringify(operationalDetail().objective),{status:200}));
 vi.stubGlobal('fetch',missingTag);
 await expect(sloClient.operational(id)).resolves.toMatchObject({evaluation_id:'22222222-2222-2222-2222-222222222222',objective:{id}});
 await expect(sloClient.operational(id)).rejects.toThrow('invalid response');
 await expect(sloClient.get(id)).rejects.toThrow('record version could not be confirmed');
});
it('rejects compliance rows that do not match the visible completed UTC day ends',async()=>{
 const value=operationalDetail();value.compliance_series.days[0]!.day_end='2026-10-22T00:00:00Z';
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(value),{status:200})));
 await expect(sloClient.operational(id)).rejects.toThrow('invalid response');
});
it('accepts a full pre-validity daily response through the operational fetch validator',async()=>{
 const value=operationalDetail();value.effective_at='2026-10-29T17:15:45Z';
 value.objective.evaluation_valid_from='2026-10-30T00:00:00Z';
 value.range={requested_start:'2026-10-22T17:15:45Z',requested_end:'2026-10-29T17:15:45Z',effective_start:'2026-10-22T17:15:45Z',effective_end:'2026-10-29T17:15:45Z',source_cadence_seconds:15,resolution_seconds:1620,reduced:true,max_samples_per_series:1500,bucket_bounds:'half_open'};
 const quality:components['schemas']['SloOperationalQuality']={state:'not_applicable',freshness:'unknown',expected_intervals:0,observed_intervals:0,known_empty_intervals:0,missing_intervals:0,expired_intervals:0,zero_traffic_intervals:0,coverage:null,first_observation_at:null,last_observation_at:null,latest_receipt_at:null,reason:'not_applicable'};
 const performance:components['schemas']['SloEvaluationPerformance']={state:'not_applicable',observed_state:null,provisional:false,reason:'not_applicable',good:null,total:null,bad:null,attainment:null,allowed_budget:null,remaining_budget:null,consumed_fraction:null,remaining_fraction:null,burn:null,budget_unit:'events'};
 value.compliance_series.days=Array.from({length:7},(_,index)=>{const dayEnd=new Date(Date.parse('2026-10-23T00:00:00Z')+index*86_400_000).toISOString().replace('.000Z','Z'),dayStart=new Date(Date.parse(dayEnd)-86_400_000).toISOString().replace('.000Z','Z');return {utc_date:dayStart.slice(0,10),day_start:dayStart,day_end:dayEnd,requested_at:dayEnd,effective_at:dayEnd,slo_id:id,definition_revision:'1',target:'0.99',quality,performance,status:'not_applicable' as const,explanation:'before_evaluation_validity'};});
 value.compliance_summary={met_days:0,violated_days:0,evaluable_days:0,unknown_days:0,not_applicable_days:7};
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(value),{status:200})));
 await expect(sloClient.operational(id)).resolves.toMatchObject({compliance_summary:{not_applicable_days:7}});
});
it('rejects swapped page and resource envelopes and a mismatched resource ID',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(service),{status:200,headers:{ETag:`"service:${id}:1"`}}))); await expect(sloClient.services()).rejects.toThrow('invalid response');
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({items:[service],next_cursor:''}),{status:200}))); await expect(sloClient.service(id)).rejects.toThrow('invalid response');
 const other='22222222-2222-2222-2222-222222222222'; vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({...service,id:other}),{status:200,headers:{ETag:`"service:${other}:1"`}}))); await expect(sloClient.service(id)).rejects.toThrow('invalid response');
});
it('rejects an evaluation for a different SLO',async()=>{
 const instant='2026-01-01T00:00:00Z',other='22222222-2222-2222-2222-222222222222';const data={snapshot_kind:'current_evaluation',requested_at:instant,effective_end:instant,objective:{slo_id:other},window:{effective_start:instant,end:instant},quality:{state:'no_data',freshness:'unknown',expected_intervals:0,observed_intervals:0,known_empty_intervals:0,missing_intervals:0,zero_traffic_intervals:0,expired_intervals:0,coverage:null},performance:{state:'unknown',observed_state:null,provisional:false,good:null,total:null,bad:null,attainment:null,allowed_budget:null,remaining_budget:null,consumed_fraction:null,remaining_fraction:null,burn:null,budget_unit:'events'},timeline:{start:instant,end:instant,cadence_seconds:60,max_intervals:1500,items:[]}};
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify(data),{status:200})));await expect(sloClient.evaluation(id)).rejects.toThrow('invalid response');
});
it('shows safe guidance for an invalid historical start without echoing server text',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(JSON.stringify({code:'invalid_request',field_errors:[{field:'evaluation_valid_from',code:'historical_start_invalid',message:'untrusted server text'}]}),{status:400})));
 await expect(sloClient.create({name:'availability',display_name:'Availability',service_id:id,sli_id:id,target:'0.99',rolling_window_seconds:60,evaluation_valid_from:'2026-01-01T00:00:00Z'},context,'key')).rejects.toThrow('The selected history is no longer available. Turn off earlier measurements, or reload the page and choose history again.');
});

it('requests server-filtered operational pages and rejects incomplete operational projections', async () => {
 const fetcher=vi.fn()
  .mockResolvedValueOnce(new Response(JSON.stringify({snapshot_id:id,items:[],group_counts:{needs_attention:2,watch:3,healthy:4},next_cursor:'',previous_cursor:''}),{status:200}))
  .mockResolvedValueOnce(new Response(JSON.stringify({snapshot_id:id,items:null,group_counts:{needs_attention:2,watch:3,healthy:4},next_cursor:'',previous_cursor:''}),{status:200}));
 vi.stubGlobal('fetch',fetcher);
 await expect(sloClient.overview({q:'checkout api',service_id:id,urgency:'watch',limit:25,cursor:'opaque'})).resolves.toMatchObject({group_counts:{watch:3}});
 const [url,init]=fetcher.mock.calls[0]!;
 expect(String(url)).toContain('/api/v1/slos/overview?');
 expect(new URL(String(url),'https://almanac.invalid').searchParams.get('q')).toBe('checkout api');
 expect(new URL(String(url),'https://almanac.invalid').searchParams.get('service_id')).toBe(id);
 expect(new URL(String(url),'https://almanac.invalid').searchParams.get('urgency')).toBe('watch');
 expect(new URL(String(url),'https://almanac.invalid').searchParams.get('cursor')).toBe('opaque');
 expect(init).toMatchObject({credentials:'same-origin',cache:'no-store'});
 await expect(sloClient.overview()).rejects.toThrow('invalid response');
});

it('cancels an operational response when its streamed body exceeds the fixed 20 MiB cap',async()=>{
 let cancelled=false;
 const body=new ReadableStream<Uint8Array>({pull(controller){controller.enqueue(new Uint8Array(20*1024*1024+1));},cancel(){cancelled=true;}});
 vi.stubGlobal('fetch',vi.fn(async()=>new Response(body,{status:200})));
 await expect(sloClient.overview()).rejects.toThrow('invalid response');
 expect(cancelled).toBe(true);
});
