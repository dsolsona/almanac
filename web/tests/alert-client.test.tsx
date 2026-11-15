import {afterEach, describe, expect, it, vi} from 'vitest';
import {alertClient, AlertFailure} from '../src/alerts/client.ts';
import type {AuthContext} from '../src/auth/client.ts';
import {clearQueryCache,sharedRead} from '../src/queryCache.ts';

const id='11111111-1111-4111-8111-111111111111', slo='22222222-2222-4222-8222-222222222222';
const at='2026-11-15T12:00:00Z';
const context={csrf_token:'private-csrf'} as AuthContext;
const condition={id:'fast',short_window_seconds:300,long_window_seconds:3600,threshold:'14.4'};
const policy={id,name:'Burn',slo_id:slo,severity:'high' as const,enabled:true,version:'9007199254740993',evaluation_revision:'1',conditions:[condition],created_at:at,updated_at:at};
const etag=`"alert-policy:${id}:${policy.version}"`;
const write={name:'Burn',slo_id:slo,severity:'high' as const,enabled:true,confirm_non_30_day_presets:false,conditions:[condition]};
const command={operation:'alert-policy.create',request_id:'original-request',policy};
const snapshot={slo_id:slo,slo_revision:'1',sli_revision:'1',source_generation:'9007199254740993',exclusion_revision:'0',calculation_version:'occurrences-operational-v1',cutoff:at};
const window={snapshot,start:'2026-11-15T11:55:00Z',end:at,quality:'partial',good:'9856.0000000000000001',total:'10000',bad:'143.9999999999999999',observed_error_rate:'0.0144',allowed_error_rate:'0.001',burn:'14.4'};
const evidence={id,scheduled_at:at,committed_at:at,evaluation:{policy_id:id,evaluation_revision:'1',snapshot,truth:'indeterminate',conditions:[{id:'fast',threshold:'14.4',outcome:'unavailable',short:window,long:{...window,start:'2026-11-15T11:00:00Z'}}]},objective:{target:'0.999',cadence_seconds:60,definition_valid_from:at,measurement_valid_from:at,retention_start:at}};
const processing={policy_id:id,revision:'1',schedulable:true,exhausted:false,next_boundary:at,available_at:at,failure_attempts:0,lag_seconds:0};
function reply(value:unknown,status=200,tag=''){return new Response(JSON.stringify(value),{status,headers:{'Content-Type':'application/json',...(tag?{ETag:tag}:{}),Date:'Sun, 15 Nov 2026 12:00:00 GMT'}});}
function fetchReply(value:unknown,status=200,tag=''){const fetcher=vi.fn().mockImplementation(()=>Promise.resolve(reply(value,status,tag)));vi.stubGlobal('fetch',fetcher);return fetcher;}
afterEach(()=>{vi.unstubAllGlobals();vi.useRealTimers();clearQueryCache();});

describe('alert client',()=>{
 it('serializes filters and shares bounded protected reads',async()=>{
  const fetcher=fetchReply({items:[],groups:[],total:'0'});
  const query={scope:'history' as const,state:'resolved,disabled',severity:'high',slo_id:slo,service_id:id,q:'x & y',group_by:'slo' as const,sort:'severity' as const,evidence:'unavailable' as const,minimum_duration_seconds:900,limit:20,cursor:'a+b/='};
  await Promise.all([alertClient.list(query),alertClient.list(query)]);
  expect(fetcher).toHaveBeenCalledTimes(1);
  const [path,init]=fetcher.mock.calls[0];const url=new URL(path,'https://example.test');
  for(const [key,value]of Object.entries(query))expect(url.searchParams.get(key)).toBe(String(value));
  expect(init).toMatchObject({credentials:'same-origin',mode:'same-origin',cache:'no-store',redirect:'error'});
 });
 it('sends original command key, csrf, body and strong etag without retries',async()=>{
  const fetcher=fetchReply({...command,operation:'alert-policy.update'},200,etag);
  const body={name:'Renamed',confirm_non_30_day_presets:false};
  const result=await alertClient.update(id,body,etag,context,'original-key');
  expect(result).toMatchObject({value:{request_id:'original-request',policy:{version:'9007199254740993'}},etag,serverTime:'Sun, 15 Nov 2026 12:00:00 GMT'});
  const [path,init]=fetcher.mock.calls[0];expect(path).toBe(`/api/v1/alert-policies/${id}`);expect(init.method).toBe('PATCH');
  expect(JSON.parse(init.body)).toEqual(body);const headers=new Headers(init.headers);expect(headers.get('X-CSRF-Token')).toBe('private-csrf');expect(headers.get('Idempotency-Key')).toBe('original-key');expect(headers.get('If-Match')).toBe(etag);
 });
 it('accepts exact partial, missing and zero counts independently of quality',async()=>{
  for(const numeric of [window,{...window,good:undefined,total:undefined,bad:undefined,observed_error_rate:undefined,allowed_error_rate:undefined,burn:undefined,quality:'missing'},{...window,good:'0',total:'0',bad:'0',observed_error_rate:undefined,burn:undefined,quality:'zero_traffic'}]){
   clearQueryCache();fetchReply({...evidence,evaluation:{...evidence.evaluation,conditions:[{...evidence.evaluation.conditions[0],short:numeric}]}});
   const result=await alertClient.evidence(id,'latest');expect(result.value.evaluation.conditions[0].short).toEqual(numeric);
  }
 });
 it.each(['01','1e3','9223372036854775808',1])('rejects malformed version %s',async(version)=>{
  fetchReply({...policy,version},200,etag);await expect(alertClient.policy(id)).rejects.toMatchObject({uncertain:false,code:'invalid_response'});
 });
 it.each(['NaN','-1','1e5',0])('rejects malformed exact count %s',async(good)=>{
  fetchReply({...evidence,evaluation:{...evidence.evaluation,conditions:[{...evidence.evaluation.conditions[0],short:{...window,good}}]}});await expect(alertClient.evidence(id,'latest')).rejects.toMatchObject({code:'invalid_response'});
 });
 it('rejects one-sided counts and mismatched snapshot identities',async()=>{
  for(const short of [{...window,total:undefined},{...window,snapshot:{...snapshot,slo_id:id}}]){
   clearQueryCache();fetchReply({...evidence,evaluation:{...evidence.evaluation,conditions:[{...evidence.evaluation.conditions[0],short}]}});await expect(alertClient.evidence(id,'latest')).rejects.toMatchObject({code:'invalid_response'});
  }
 });
 it('keeps known rejection distinct from uncertainty and does not expose server messages',async()=>{
  const fetcher=fetchReply({code:'version_conflict',message:'secret-query',request_id:'request-409',field_errors:[]},409);
  const error=await alertClient.update(id,{name:'Next',confirm_non_30_day_presets:false},etag,context,'key').catch(e=>e);
  expect(error).toBeInstanceOf(AlertFailure);expect(error).toMatchObject({status:409,code:'version_conflict',uncertain:false,requestId:'request-409'});expect(error.message).not.toContain('secret-query');expect(fetcher).toHaveBeenCalledTimes(1);
 });
 it('only returns allowlisted field guidance',async()=>{
  fetchReply({code:'invalid_request',message:'private',request_id:'r',field_errors:[{field:'confirm_non_30_day_presets',code:'preset_confirmation_required',message:'secret'},{field:'secret-field',code:'private',message:'secret'}]},400);
  const error=await alertClient.create(write,context,'key').catch(e=>e);expect(error.fields).toHaveLength(1);expect(error.fields[0]).toMatchObject({field:'confirm_non_30_day_presets',code:'preset_confirmation_required'});expect(JSON.stringify(error)).not.toContain('secret');
 });
 it('preserves actual indexed condition field guidance',async()=>{
  fetchReply({code:'invalid_request',message:'secret',request_id:'r',field_errors:[{field:'conditions.2.threshold',code:'condition_invalid',message:'secret'}]},400);
  const error=await alertClient.create(write,context,'key').catch(e=>e);expect(error.fields).toEqual([{field:'conditions.2.threshold',code:'condition_invalid',message:'Use positive exact thresholds and paired whole-minute windows, with one to eight unique conditions.'}]);
 });
 it.each(['503','204','network','malformed','etag'])('marks mutation %s unknown once',async(kind)=>{
  const fetcher=kind==='network'?vi.fn().mockRejectedValue(new Error('secret-transport')):vi.fn().mockResolvedValue(kind==='204'?new Response(null,{status:204}):kind==='503'?reply({code:'unavailable',message:'secret',request_id:'r'},503):kind==='malformed'?reply({bad:true},201,etag):reply(command,201,`W/${etag}`));vi.stubGlobal('fetch',fetcher);
  const error=await alertClient.create(write,context,'key').catch(e=>e);expect(error).toMatchObject({uncertain:true});expect(error.message).not.toContain('secret');expect(fetcher).toHaveBeenCalledTimes(1);
 });
 it('validates resource identity and never upgrades a replayed receipt version',async()=>{
  fetchReply({...command,policy:{...policy,version:'1'}},201,`"alert-policy:${id}:1"`);expect((await alertClient.create(write,context,'key')).value.policy?.version).toBe('1');
  fetchReply({...command,operation:'alert-policy.update',policy:{...policy,id:slo}},200,`"alert-policy:${slo}:${policy.version}"`);await expect(alertClient.update(id,{name:'X',confirm_non_30_day_presets:false},etag,context,'next')).rejects.toMatchObject({uncertain:true});
 });
 it('preserves expired and unknown lookup meanings and verifies operation',async()=>{
  for(const status of ['unknown','expired']){clearQueryCache();const fetcher=fetchReply({status});expect((await alertClient.commandResult('alert-policy.create','key /?')).value.status).toBe(status);expect(new URL(fetcher.mock.calls[0][0],'https://example.test').searchParams.get('idempotency_key')).toBe('key /?');}
  clearQueryCache();fetchReply({status:'committed',result:{...command,operation:'alert-policy.disable'}});await expect(alertClient.commandResult('alert-policy.create','key')).rejects.toMatchObject({code:'invalid_response'});
 });
 it('reads explicit pre-activation scheduled history',async()=>{
  const page={items:[{id,kind:'before_policy_activation',reason:'before_policy_activation',start:at,end:'2026-11-15T12:01:00Z',recorded_at:at,cadence_seconds:60}]};
  fetchReply(page);expect((await alertClient.ranges(id,'1',{limit:20})).value).toEqual(page);
 });
 it('routes policy, evidence and bounded history reads',async()=>{
  const cases:[()=>Promise<unknown>,unknown,string,string?][]=[
   [()=>alertClient.policies({enabled:false,q:'x'}),{items:[],total:'0'},'/api/v1/alert-policies?enabled=false&q=x'],
   [()=>alertClient.policy(id),policy,`/api/v1/alert-policies/${id}`,etag],
   [()=>alertClient.evidence(id,'firing'),evidence,`/api/v1/alerts/${id}/evidence?selection=firing`],
   [()=>alertClient.transitions(id,{limit:10,cursor:'next'}),{items:[]},`/api/v1/alerts/${id}/transitions?limit=10&cursor=next`],
   [()=>alertClient.ranges(id,'2',{limit:20}),{items:[]},`/api/v1/alert-policies/${id}/ranges?revision=2&limit=20`],
   [()=>alertClient.processing(id),processing,`/api/v1/alert-processing?policy_id=${id}`],
  ];
  for(const [call,value,path,tag]of cases){const fetcher=fetchReply(value,200,tag);await call();expect(fetcher.mock.calls[0][0]).toBe(path);}
 });
 it('requires exact group firing subtotals and rejects malformed pages',async()=>{
  for(const firing of [undefined,'01','2',1]){clearQueryCache();fetchReply({items:[],groups:[{slo_id:slo,slo_name:'SLO',total:'1',firing}],total:'1'});await expect(alertClient.list()).rejects.toMatchObject({code:'invalid_response'});}
 });
 it('reads and acknowledges the persisted occurrence with the occurrence ETag',async()=>{
  const record={id,policy_id:slo,evaluation_revision:'1',state:'firing',version:'1',sequence:'1',firing_snapshot:snapshot,latest_snapshot:snapshot,latest_breaching_snapshot:snapshot,opened_detected_at:at,updated_at:at,original_policy:{name:'Saved',severity:'high',slo_name:'SLO',service_name:'Service',target:'0.999',rolling_window_seconds:2592000}};
  const originalTag=`"alert-occurrence:${id}:1"`;let fetcher=fetchReply(record,200,originalTag);
  expect((await alertClient.occurrence(id)).value.state).toBe('firing');expect(fetcher.mock.calls[0][0]).toBe(`/api/v1/alerts/${id}`);
  const acknowledged={...record,state:'acknowledged',version:'2',sequence:'2',acknowledged_by:id,acknowledged_at:at};
  fetcher=fetchReply({operation:'alert.acknowledge',request_id:'ack-request',alert:acknowledged},200,`"alert-occurrence:${id}:2"`);
  expect((await alertClient.acknowledge(id,originalTag,context,'ack-key')).value.alert?.state).toBe('acknowledged');
  expect(fetcher.mock.calls[0][0]).toBe(`/api/v1/alerts/${id}/acknowledge`);expect(fetcher.mock.calls[0][1].body).toBe('{}');expect(new Headers(fetcher.mock.calls[0][1].headers).get('If-Match')).toBe(originalTag);
 });
 it('rejects retry reasons locally and keeps the retry body empty',async()=>{
  const fetcher=fetchReply({...command,operation:'alert-policy.retry'},200,etag);
  await expect(alertClient.policyCommand(id,'retry',etag,context,'key',{reason:'not supported'})).rejects.toMatchObject({uncertain:false,code:'invalid_request'});expect(fetcher).not.toHaveBeenCalled();
  await alertClient.policyCommand(id,'retry',etag,context,'key');expect(fetcher.mock.calls[0][1].body).toBe('{}');
 });
 it('bounds streaming body bytes and cancels the reader',async()=>{
  const cancel=vi.fn();const body=new ReadableStream<Uint8Array>({start(c){c.enqueue(new Uint8Array(8*1024*1024+1));},cancel});vi.stubGlobal('fetch',vi.fn().mockResolvedValue(new Response(body,{headers:{'Content-Type':'application/json'}})));
  await expect(alertClient.list()).rejects.toMatchObject({uncertain:false,code:'invalid_response'});expect(cancel).toHaveBeenCalled();
 });
 it('keeps the deadline through stalled response bodies and cancels mutation once',async()=>{
  vi.useFakeTimers();const cancel=vi.fn();const body=new ReadableStream<Uint8Array>({cancel});const fetcher=vi.fn().mockResolvedValue(new Response(body,{status:201,headers:{'Content-Type':'application/json',ETag:etag}}));vi.stubGlobal('fetch',fetcher);
  const result=alertClient.create(write,context,'key').catch(e=>e);await vi.advanceTimersByTimeAsync(30000);expect(await result).toMatchObject({uncertain:true});expect(cancel).toHaveBeenCalled();expect(fetcher).toHaveBeenCalledTimes(1);
 });
 it('caller cancellation remains bounded even if fetch ignores abort',async()=>{
  const controller=new AbortController();const fetcher=vi.fn().mockImplementation(()=>new Promise(()=>{}));vi.stubGlobal('fetch',fetcher);
  const result=alertClient.create(write,context,'key',controller.signal).catch(e=>e);controller.abort();expect(await result).toMatchObject({uncertain:true});expect(fetcher).toHaveBeenCalledTimes(1);
 });
 it('invalidates cached authority on denial and reads after writes',async()=>{
  const protectedLoader=vi.fn().mockResolvedValue('protected');const cached=()=>sharedRead('other-protected-read',protectedLoader);await cached();await cached();expect(protectedLoader).toHaveBeenCalledTimes(1);
  const fetcher=fetchReply(policy,200,etag);await alertClient.policy(id);fetcher.mockResolvedValueOnce(reply({code:'forbidden',request_id:'r',message:'private'},403));await expect(alertClient.processing(id)).rejects.toMatchObject({status:403,uncertain:false});fetcher.mockImplementation(()=>Promise.resolve(reply(policy,200,etag)));await alertClient.policy(id);expect(fetcher).toHaveBeenCalledTimes(3);await cached();expect(protectedLoader).toHaveBeenCalledTimes(2);
  fetcher.mockResolvedValueOnce(reply(command,201,etag));await alertClient.create(write,context,'key');await alertClient.policy(id);expect(fetcher).toHaveBeenCalledTimes(5);await cached();expect(protectedLoader).toHaveBeenCalledTimes(3);
 });
});
