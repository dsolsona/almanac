import type {components} from '../api/generated.ts';
import type {AuthContext} from '../auth/client.ts';
import {clearQueryCache,sharedRead} from '../queryCache.ts';
import {traceJSON} from '../telemetry/client.ts';
import * as valid from './validation.ts';

type Schemas=components['schemas'];
export type Policy=Schemas['AlertPolicy'];
type WriteDefaults='severity'|'enabled'|'confirm_non_30_day_presets';
export type PolicyWrite=Omit<Schemas['AlertPolicyWrite'],WriteDefaults>&Partial<Pick<Schemas['AlertPolicyWrite'],WriteDefaults>>;
export type PolicyPatch=Omit<Schemas['AlertPolicyPatch'],'confirm_non_30_day_presets'>&Partial<Pick<Schemas['AlertPolicyPatch'],'confirm_non_30_day_presets'>>;
export type Occurrence=Schemas['AlertRecord'];
export type AlertPage=Schemas['AlertPage'];
export type PolicyPage=Schemas['AlertPolicyPage'];
export type Evidence=Schemas['AlertEvidence'];
export type Series=Schemas['AlertSeries'];
export type Processing=Schemas['AlertProcessing'];
export type CommandResult=Schemas['AlertCommandResult'];
export type CommandLookup=Schemas['AlertCommandLookup'];
export type TransitionPage=Schemas['AlertTransitionPage'];
export type RangePage=Schemas['AlertRangePage'];
type Operation=Schemas['AlertOperation'];
export type Query={scope?:'active'|'history'|'all';state?:string;severity?:string;slo_id?:string;service_id?:string;q?:string;group_by?:'none'|'slo';sort?:'newest'|'severity';evidence?:''|'matching'|'unavailable'|'waiting';minimum_duration_seconds?:number;limit?:number;cursor?:string};
export type PolicyQuery={q?:string;slo_id?:string;severity?:string;enabled?:boolean;limit?:number;cursor?:string};
type PageQuery={limit?:number;cursor?:string};
export type Result<T>={value:T;etag:string;serverTime?:string};
export type FieldFailure={field:string;code:string;message:string};
export class AlertFailure extends Error {
 constructor(message:string,readonly uncertain=false,readonly status=0,readonly code='',readonly requestId?:string,readonly fields?:FieldFailure[]){super(message);this.name='AlertFailure';}
}
const MAX_BYTES=8*1024*1024;
const messages:Record<string,string>={
 invalid_request:'Check the alert details and try again.',unauthorized:'Sign in again to continue.',unauthenticated:'Sign in again to continue.',forbidden:'You are not allowed to perform this operation.',csrf_invalid:'Refresh the page before trying again.',origin_rejected:'This request was rejected.',secure_transport_required:'A secure connection is required.',not_found:'This alert or policy was not found.',version_conflict:'This item changed. Refresh it before trying again.',name_conflict:'Choose a different policy name.',command_body_conflict:'This request key belongs to a different command. Reconcile the original command.',command_expired:'The saved command result has expired. Check the current resource before deciding what to do next.',method_not_allowed:'This operation is unavailable.',request_too_large:'The request is too large.',unsupported_media_type:'The request format is unsupported.',rate_limited:'Too many requests. Wait before trying again.',
};
const known:Record<number,string[]>={400:['invalid_request'],401:['unauthorized','unauthenticated'],403:['forbidden','csrf_invalid','origin_rejected','secure_transport_required'],404:['not_found'],405:['method_not_allowed'],409:['version_conflict','name_conflict','command_body_conflict'],410:['command_expired'],413:['request_too_large','invalid_request'],415:['unsupported_media_type','invalid_request'],429:['rate_limited']};
const fieldMessages:Record<string,string>={name_invalid:'Enter a policy name of at most 256 characters without control characters.',severity_invalid:'Choose low, medium or high severity.',condition_invalid:'Use positive exact thresholds and paired whole-minute windows, with one to eight unique conditions.',conditions_invalid:'Use windows aligned to the SLI cadence and a reachable positive burn threshold.',preset_confirmation_required:'Confirm these 30-day reference presets for this SLO period, or choose custom conditions.'};
function safeFields(value:unknown):FieldFailure[]{
 if(!Array.isArray(value))return [];
 return value.slice(0,16).flatMap(item=>{
  if(!valid.object(item)||!valid.text(item.field)||!valid.text(item.code)||!Object.hasOwn(fieldMessages,item.code))return [];
  const allowed=item.code==='preset_confirmation_required'?item.field==='confirm_non_30_day_presets':item.code==='name_invalid'?item.field==='name':item.code==='severity_invalid'?item.field==='severity':/^conditions(?:\.[0-7](?:\.(?:id|threshold|short_window_seconds|long_window_seconds))?)?$/.test(item.field);
  return allowed?[{field:item.field,code:item.code,message:fieldMessages[item.code]}]:[];
 });
}
function failure(mutation:boolean,status=0,code='invalid_response',requestId?:string):AlertFailure{
 return new AlertFailure(mutation?'The operation may have completed. Check its original command key before deciding what to do next.':code==='cancelled'?'The read was cancelled.':'Alert data could not be loaded. Try again.',mutation,status,code,requestId);
}
const invalid=()=>new AlertFailure(messages.invalid_request,false,0,'invalid_request');
function pathID(id:string):string{if(!valid.uuid(id))throw invalid();return id;}
function query(path:string,filters:object={}):string{const params=new URLSearchParams();for(const [key,value]of Object.entries(filters))if(value!==undefined&&value!=='')params.set(key,String(value));return path+(params.size?`?${params}`:'');}
function tag(kind:string,value:Record<string,unknown>):string{return `"${kind}:${value.id}:${value.version}"`;}
function expectedTag(value:unknown):string{
 if(!valid.object(value))return '';
 if(valid.object(value.policy))return tag('alert-policy',value.policy);
 if(valid.object(value.alert))return tag('alert-occurrence',value.alert);
 return '';
}
function requireTag(value:string,kind:string,id:string):void{const prefix=`"${kind}:${id}:`;if(!value.startsWith(prefix)||!value.endsWith('"')||!valid.counter(value.slice(prefix.length,-1),true))throw invalid();}
async function jsonBody(response:Response,signal:AbortSignal,aborted:Promise<never>):Promise<unknown>{
 const sizeHeader=response.headers.get('Content-Length');
 if(!response.body||sizeHeader!==null&&(!/^\d+$/.test(sizeHeader)||Number(sizeHeader)>MAX_BYTES)){
  void response.body?.cancel().catch(()=>{});throw new Error('invalid body');
 }
 const reader=response.body.getReader();const chunks:Uint8Array[]=[];let size=0,complete=false;
 try{
  while(true){signal.throwIfAborted();const part=await Promise.race([reader.read(),aborted]);signal.throwIfAborted();if(part.done){complete=true;break;}size+=part.value.byteLength;if(size>MAX_BYTES)throw new Error('body limit');chunks.push(part.value);}
 }finally{if(!complete)void reader.cancel().catch(()=>{});try{reader.releaseLock();}catch{/* Cancellation can still be settling. */}}
 const bytes=new Uint8Array(size);let offset=0;for(const chunk of chunks){bytes.set(chunk,offset);offset+=chunk.byteLength;}
 signal.throwIfAborted();return traceJSON(response,new TextDecoder('utf-8',{fatal:true}).decode(bytes));
}
type Check=(value:unknown)=>boolean;
async function perform<T>(path:string,check:Check,init:RequestInit,mutation:boolean,statusExpected:number,etagFor?:(value:unknown)=>string):Promise<Result<T>>{
 const controller=new AbortController();const onExternalAbort=()=>controller.abort();
 if(init.signal?.aborted)throw failure(mutation,0,'cancelled');
 init.signal?.addEventListener('abort',onExternalAbort,{once:true});
 const timer=setTimeout(()=>controller.abort(),30000);
 let rejectAbort:()=>void=()=>{};
 const aborted=new Promise<never>((_,reject)=>{rejectAbort=()=>reject(new Error('cancelled'));controller.signal.addEventListener('abort',rejectAbort,{once:true});});
 let status=0,requestId:string|undefined;
 const action=async():Promise<Result<T>>=>{
  const response=await fetch(path,{...init,signal:controller.signal,credentials:'same-origin',mode:'same-origin',cache:'no-store',redirect:'error'});
  if(controller.signal.aborted){void response.body?.cancel().catch(()=>{});controller.signal.throwIfAborted();}
  status=response.status;
  const headerID=response.headers.get('X-Request-ID');if(valid.requestID(headerID))requestId=headerID;
  if(status===401||status===403)clearQueryCache(false);
  const media=response.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase();
  if(media!=='application/json'&&(status===statusExpected||media!=='application/problem+json')){void response.body?.cancel().catch(()=>{});throw failure(mutation,status,'invalid_response',requestId);}
  const value=await jsonBody(response,controller.signal,aborted);
  if(status!==statusExpected){
   if(valid.object(value)&&valid.requestID(value.request_id))requestId=value.request_id;
   if(valid.object(value)&&valid.text(value.code)&&known[status]?.includes(value.code))throw new AlertFailure(messages[value.code],false,status,value.code,requestId,safeFields(value.field_errors));
   throw failure(mutation,status,'outcome_unknown',requestId);
  }
  if(!check(value))throw failure(mutation,status,'invalid_response',requestId);
  const etag=response.headers.get('ETag')??'';
  if(etagFor&&etag!==etagFor(value))throw failure(mutation,status,'invalid_response',requestId);
  controller.signal.throwIfAborted();
  const date=response.headers.get('Date');return {value:value as T,etag,...(date&&Number.isFinite(Date.parse(date))?{serverTime:date}:{})};
 };
 try{return await Promise.race([action(),aborted]);}
 catch(error){if(error instanceof AlertFailure)throw error;throw failure(mutation,status,controller.signal.aborted?'cancelled':'invalid_response',requestId);}
 finally{clearTimeout(timer);init.signal?.removeEventListener('abort',onExternalAbort);controller.signal.removeEventListener('abort',rejectAbort);}
}
async function read<T>(path:string,check:Check,signal?:AbortSignal,etagFor?:(value:unknown)=>string):Promise<Result<T>>{
 try{return await sharedRead(`alerts:${path}`,sharedSignal=>perform<T>(path,check,{signal:sharedSignal},false,200,etagFor),signal,0);}
 catch(error){if(error instanceof AlertFailure)throw error;throw failure(false,0,signal?.aborted?'cancelled':'invalid_response');}
}
async function write(path:string,operation:Operation,body:unknown,context:AuthContext,key:string,signal?:AbortSignal,etag?:string,id?:string):Promise<Result<CommandResult>>{
 if(!/^[\x21-\x7e]{1,128}$/.test(key)||!context.csrf_token)throw invalid();
 let encoded:string;try{encoded=JSON.stringify(body);if(!encoded||new TextEncoder().encode(encoded).byteLength>65536)throw invalid();}catch{throw invalid();}
 const headers:Record<string,string>={'Content-Type':'application/json','X-CSRF-Token':context.csrf_token,'Idempotency-Key':key};if(etag)headers['If-Match']=etag;
 clearQueryCache();
 try{return await perform<CommandResult>(path,value=>valid.command(value,operation,id),{method:operation==='alert-policy.update'?'PATCH':'POST',headers,body:encoded,signal},true,operation==='alert-policy.create'?201:200,expectedTag);}
 finally{clearQueryCache();}
}
export const alertClient={
 list:async(filters:Query={},signal?:AbortSignal)=>read<AlertPage>(query('/api/v1/alerts',filters),valid.list,signal),
 policies:async(filters:PolicyQuery={},signal?:AbortSignal)=>read<PolicyPage>(query('/api/v1/alert-policies',filters),valid.policies,signal),
 policy:async(id:string,signal?:AbortSignal)=>read<Policy>(`/api/v1/alert-policies/${pathID(id)}`,value=>valid.policy(value)&&(value as Policy).id===id,signal,value=>tag('alert-policy',value as Record<string,unknown>)),
 occurrence:async(id:string,signal?:AbortSignal)=>read<Occurrence>(`/api/v1/alerts/${pathID(id)}`,value=>valid.occurrence(value)&&value.id===id,signal,value=>tag('alert-occurrence',value as Record<string,unknown>)),
 evidence:async(id:string,selection:'firing'|'latest',signal?:AbortSignal)=>read<Evidence>(query(`/api/v1/alerts/${pathID(id)}/evidence`,{selection}),valid.evidence,signal),
 series:async(id:string,conditionID:string,options:{start?:string;end?:string;limit?:number}={},signal?:AbortSignal)=>read<Series>(query(`/api/v1/alerts/${pathID(id)}/series`,{condition_id:conditionID,...options}),value=>valid.series(value,conditionID),signal),
 transitions:async(id:string,opts:PageQuery={},signal?:AbortSignal)=>read<TransitionPage>(query(`/api/v1/alerts/${pathID(id)}/transitions`,opts),valid.transitions,signal),
 ranges:async(policyId:string,revision:string,opts:PageQuery={},signal?:AbortSignal)=>{if(!valid.counter(revision,true))return Promise.reject(invalid());return read<RangePage>(query(`/api/v1/alert-policies/${pathID(policyId)}/ranges`,{revision,...opts}),valid.ranges,signal);},
 processing:async(policyId:string,signal?:AbortSignal)=>read<Processing>(query('/api/v1/alert-processing',{policy_id:pathID(policyId)}),value=>valid.processing(value)&&value.policy_id===policyId,signal),
 commandResult:async(operation:Operation,key:string,signal?:AbortSignal)=>read<CommandLookup>(query('/api/v1/alert-commands',{operation,idempotency_key:key}),value=>valid.lookup(value,operation),signal),
 create:async(body:PolicyWrite,context:AuthContext,key:string,signal?:AbortSignal)=>write('/api/v1/alert-policies','alert-policy.create',body,context,key,signal),
 update:async(id:string,body:PolicyPatch,etag:string,context:AuthContext,key:string,signal?:AbortSignal)=>{pathID(id);requireTag(etag,'alert-policy',id);return write(`/api/v1/alert-policies/${id}`,'alert-policy.update',body,context,key,signal,etag,id);},
 policyCommand:async(id:string,action:'enable'|'disable'|'retry',etag:string,context:AuthContext,key:string,body:Schemas['AlertPolicyCommand']={},signal?:AbortSignal)=>{pathID(id);requireTag(etag,'alert-policy',id);if(action==='retry'&&Object.keys(body).length!==0)throw invalid();return write(`/api/v1/alert-policies/${id}/${action}`,`alert-policy.${action}`,body,context,key,signal,etag,id);},
 acknowledge:async(id:string,etag:string,context:AuthContext,key:string,signal?:AbortSignal)=>{pathID(id);requireTag(etag,'alert-occurrence',id);return write(`/api/v1/alerts/${id}/acknowledge`,'alert.acknowledge',{},context,key,signal,etag,id);},
};
