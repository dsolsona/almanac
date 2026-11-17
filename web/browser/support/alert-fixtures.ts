import type { Page } from '@playwright/test';
import type { components } from '../../src/api/generated.ts';
import { overview, services } from './service-fixtures.ts';
type Schema=components['schemas'];
export const alertID='90000000-0000-4000-8000-000000000001',policyID='90000000-0000-4000-8000-000000000002',actorID='90000000-0000-4000-8000-000000000003';
const at='2026-11-15T12:00:00Z',earlier='2026-11-15T11:58:00Z';
const reference=overview(services[0]!.id);
export const alertSlo={...reference.items[0]!.slo,display_name:'Checkout availability'};
export const alertPolicy:Schema['AlertPolicy']={id:policyID,name:'Checkout burn',slo_id:alertSlo.id,severity:'medium',enabled:true,version:'1',evaluation_revision:'1',created_at:at,updated_at:at,conditions:[{id:'fast',short_window_seconds:300,long_window_seconds:3600,threshold:'14.4'},{id:'sustained',short_window_seconds:1800,long_window_seconds:21600,threshold:'6'}]};
function evidence(cutoff:string):Schema['AlertEvidence']{
 const snapshot={slo_id:alertSlo.id,slo_revision:'1',sli_revision:'1',source_generation:'1',exclusion_revision:'0',calculation_version:'occurrences-operational-v1',cutoff};
 const window=(seconds:number)=>({snapshot,start:new Date(Date.parse(cutoff)-seconds*1000).toISOString(),end:cutoff,quality:'complete' as const,good:'9856',total:'10000',bad:'144',observed_error_rate:'0.0144',allowed_error_rate:'0.001',burn:'14.4'});
 return {id:cutoff===at?'90000000-0000-4000-8000-000000000004':'90000000-0000-4000-8000-000000000005',scheduled_at:cutoff,committed_at:cutoff,evaluation:{policy_id:policyID,evaluation_revision:'1',snapshot,truth:'breaching',conditions:alertPolicy.conditions.map(condition=>({id:condition.id,threshold:condition.threshold,outcome:'matching',short:window(condition.short_window_seconds),long:window(condition.long_window_seconds)}))},objective:{target:'0.999',cadence_seconds:60,definition_valid_from:'2026-10-01T00:00:00Z',measurement_valid_from:'2026-10-01T00:00:00Z',retention_start:'2026-10-01T00:00:00Z'}};
}
export async function alertFixture(page:Page,options:{createDelay?:Promise<void>;sampledSeries?:boolean;role?:string;uncertainCreate?:boolean;conflictUpdate?:boolean;expiredReceipt?:boolean;tracing?:boolean;listDelay?:Promise<void>;sloDelay?:Promise<void>;sloFailure?:boolean;occurrenceFailure?:boolean}={}){
 let sloRequests=0;
 let policy=structuredClone(alertPolicy),acknowledged=false,createCount=0,createKey='',command:Schema['AlertCommandResult']|undefined;
 const requests:{path:string;method:string;body:unknown;key:string|null;etag:string|null}[]=[];
 const latest=evidence(at),original=evidence(earlier);
 const processing=()=>({policy_id:policyID,revision:policy.evaluation_revision,schedulable:policy.enabled,exhausted:false,next_boundary:'2026-11-15T12:01:00Z',available_at:at,last_cutoff:at,failure_attempts:0,lag_seconds:0});
 const occurrence=():Schema['AlertRecord']=>({id:alertID,policy_id:policyID,evaluation_revision:'1',state:acknowledged?'acknowledged':'firing',version:acknowledged?'2':'1',sequence:acknowledged?'2':'1',firing_snapshot:original.evaluation.snapshot,latest_snapshot:latest.evaluation.snapshot,latest_breaching_snapshot:latest.evaluation.snapshot,opened_detected_at:earlier,updated_at:at,...(acknowledged?{acknowledged_by:actorID,acknowledged_at:at}:{}),original_policy:{name:alertPolicy.name,severity:'medium',slo_name:alertSlo.display_name,service_name:'Checkout',target:'0.999',rolling_window_seconds:2592000}});
 await page.route('**/api/**',async route=>{
  const request=route.request(),url=new URL(request.url()),path=url.pathname,method=request.method();
  const reply=(json:unknown,status=200,etag='')=>route.fulfill({status,json,headers:{Date:'Sun, 15 Nov 2026 12:00:00 GMT',...(options.tracing?{'X-Almanac-Tracing':'1'}:{}),...(etag?{ETag:etag}:{})}});
  if(path==='/api/v1/telemetry/traces')return reply({});
  if(path==='/api/v1/auth/context')return reply({csrf_token:'csrf',session:{user_id:actorID,login:'Editor',role:options.role??'editor',restricted:false}});
  if(path==='/api/v1/slos/overview'){sloRequests++;if(options.sloDelay)await options.sloDelay;return options.sloFailure?reply({code:'internal_error'},500):reply({...reference,items:[{...reference.items[0],slo:alertSlo}],total:1,next_cursor:''});}
  if(path===`/api/v1/slos/${alertSlo.id}`)return reply(alertSlo,200,`"slo:${alertSlo.id}:1"`);
  requests.push({path:url.pathname+url.search,method,body:request.postDataJSON(),key:request.headers()['idempotency-key']??null,etag:request.headers()['if-match']??null});
  if(path==='/api/v1/alert-commands')return reply(options.expiredReceipt?{status:'expired'}:{status:'committed',result:command});
  if(path==='/api/v1/alert-policies'&&method==='POST'){
   if(options.createDelay)await options.createDelay;
   createCount++;createKey=request.headers()['idempotency-key'];const body=request.postDataJSON();policy={...policy,...body};command={operation:'alert-policy.create',request_id:'original-create',policy};
   return options.uncertainCreate?reply({code:'internal_error',request_id:'original-create'},500):reply(command,201,`"alert-policy:${policy.id}:${policy.version}"`);
  }
  if(path==='/api/v1/alert-policies')return reply({items:[{policy,slo_name:alertSlo.display_name,processing:processing()}],total:'1'});
  if(path===`/api/v1/alert-policies/${policyID}`){
   if(method==='PATCH'){if(options.conflictUpdate)return reply({code:'version_conflict',request_id:'conflict'},409);policy={...policy,...request.postDataJSON(),version:String(Number(policy.version)+1)};return reply({operation:'alert-policy.update',request_id:'update',policy},200,`"alert-policy:${policy.id}:${policy.version}"`);}
   return reply(policy,200,`"alert-policy:${policy.id}:${policy.version}"`);
  }
  if(path===`/api/v1/alert-policies/${policyID}/disable`||path===`/api/v1/alert-policies/${policyID}/enable`){const enable=path.endsWith('/enable');policy={...policy,enabled:enable,version:String(Number(policy.version)+1)};return reply({operation:enable?'alert-policy.enable':'alert-policy.disable',request_id:'enabled',policy},200,`"alert-policy:${policy.id}:${policy.version}"`);}
  if(path==='/api/v1/alert-processing')return reply(processing());
  if(path==='/api/v1/alerts'&&options.listDelay)await options.listDelay;
  if(path==='/api/v1/alerts')return reply({items:[{alert:occurrence(),policy_name:policy.name,severity:policy.severity,slo_id:alertSlo.id,slo_name:alertSlo.display_name,service_id:alertSlo.service_id,service_name:'Checkout',evaluation_id:latest.id,evaluation:latest.evaluation,scheduled_at:at,committed_at:at,processing:processing()}],groups:url.searchParams.get('group_by')==='slo'?[{slo_id:alertSlo.id,slo_name:alertSlo.display_name,total:'1',firing:acknowledged?'0':'1'}]:[],total:'1'});
  if(path===`/api/v1/alerts/${alertID}`&&options.occurrenceFailure)return reply({code:'internal_error'},500);
  if(path===`/api/v1/alerts/${alertID}`)return reply(occurrence(),200,`"alert-occurrence:${alertID}:${acknowledged?'2':'1'}"`);
  if(path===`/api/v1/alerts/${alertID}/acknowledge`){acknowledged=true;return reply({operation:'alert.acknowledge',request_id:'ack',alert:occurrence()},200,`"alert-occurrence:${alertID}:2"`);}
  if(path===`/api/v1/alerts/${alertID}/evidence`)return reply(url.searchParams.get('selection')==='firing'?original:latest);
  if(path===`/api/v1/alerts/${alertID}/series`){const id=url.searchParams.get('condition_id')!,index=alertPolicy.conditions.findIndex(condition=>condition.id===id);return reply({policy_id:policyID,evaluation_revision:'1',objective:latest.objective,condition:alertPolicy.conditions[index],start:'2026-11-15T10:00:00Z',end:at,points:[original,latest].map(value=>({evaluation_id:value.id,snapshot:value.evaluation.snapshot,scheduled_at:value.scheduled_at,committed_at:value.committed_at,condition:value.evaluation.conditions[index]})),gaps:[],sampling:options.sampledSeries?{total:3,returned:2,reduced:true,omitted_extrema:true,method:'extrema_priority'}:{total:2,returned:2,reduced:false,omitted_extrema:false,method:'exact'}});}
  if(path===`/api/v1/alerts/${alertID}/transitions`)return reply({items:[{id:'90000000-0000-4000-8000-000000000006',to:'firing',cause:'condition_matched',request_id:'evaluate',sequence:'1',effective_at:earlier,detected_at:earlier},...(acknowledged?[{id:'90000000-0000-4000-8000-000000000007',from:'firing',to:'acknowledged',cause:'acknowledged',actor_id:actorID,request_id:'ack',sequence:'2',effective_at:at,detected_at:at}]:[])]});
  return reply({code:'not_found',request_id:'fixture'},404);
 });
 return {requests,sloRequests:()=>sloRequests,created:()=>({count:createCount,key:createKey})};
}
