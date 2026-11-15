import {act,cleanup,render,renderHook} from '@testing-library/react';
import {afterEach,expect,it,vi} from 'vitest';
import {useAlertCommands,type AlertIntent} from '../src/alerts/useAlertCommands.ts';
import type {AuthContext} from '../src/auth/client.ts';

const actor='11111111-1111-4111-8111-111111111111',id='22222222-2222-4222-8222-222222222222';
const context:AuthContext={csrf_token:'private-csrf',session:{user_id:actor,login:'owner',role:'administrator',restricted:false}};
const body={name:'Policy',slo_id:id,conditions:[{id:'fast',short_window_seconds:60,long_window_seconds:120,threshold:'1'}]};
const intent:AlertIntent={key:'original-key',etag:'',id:'',actorID:actor,origin:'/alerts/policies/new',operation:'alert-policy.create',body};
const policy={...body,id,severity:'medium',enabled:true,version:'1',evaluation_revision:'1',created_at:'2026-11-15T12:00:00Z',updated_at:'2026-11-15T12:00:00Z'};
const committed={operation:'alert-policy.create',request_id:'original-request',policy};
const response=(value:unknown,status=200)=>new Response(JSON.stringify(value),{status,headers:{'Content-Type':'application/json',ETag:`"alert-policy:${id}:1"`}});
function deferred<T>(){let resolve!:(v:T)=>void;const promise=new Promise<T>(done=>{resolve=done;});return {promise,resolve};}
afterEach(()=>{cleanup();vi.unstubAllGlobals();});

it('keeps an in-flight command when its route consumer unmounts',async()=>{
 const pending=deferred<Response>();const fetcher=vi.fn().mockReturnValue(pending.promise);vi.stubGlobal('fetch',fetcher);
 let host!:ReturnType<typeof useAlertCommands>;
 function Consumer(){return <span>Route consumer</span>;}
 function Host({show}:{show:boolean}){host=useAlertCommands(context);return <>{show&&<Consumer/>}<output>{host.state.status}</output></>;}
 const view=render(<Host show/>);let completion!:Promise<unknown>;
 act(()=>{completion=host.perform(intent);});expect(host.state.status).toBe('sending');
 view.rerender(<Host show={false}/>);expect(fetcher.mock.calls[0][1].signal.aborted).toBe(false);
 await act(async()=>{pending.resolve(response(committed,201));await completion;});
 expect(host.state).toMatchObject({status:'committed',intent:{key:'original-key',origin:'/alerts/policies/new',actorID:actor},result:{request_id:'original-request'}});expect(fetcher).toHaveBeenCalledTimes(1);
});
it('never automatically writes after a lost response or an unknown lookup',async()=>{
 const fetcher=vi.fn().mockRejectedValueOnce(new Error('private transport')).mockImplementationOnce(()=>Promise.resolve(response({status:'unknown'}))).mockImplementationOnce(()=>Promise.resolve(response(committed,201)));vi.stubGlobal('fetch',fetcher);
 const {result}=renderHook(()=>useAlertCommands(context));
 await act(async()=>{await result.current.perform(intent);});expect(result.current.state).toMatchObject({status:'uncertain',canRetry:false,intent:{key:'original-key'}});expect(result.current.state.error).not.toContain('private');
 await act(async()=>{await result.current.perform(intent);});expect(fetcher).toHaveBeenCalledTimes(1);
 await act(async()=>{await result.current.reconcile();});expect(result.current.state).toMatchObject({status:'uncertain',canRetry:true});expect(fetcher).toHaveBeenCalledTimes(2);expect(fetcher.mock.calls[1][0]).toContain('idempotency_key=original-key');
 await act(async()=>{await result.current.perform({...intent,key:'different-key'});});expect(fetcher).toHaveBeenCalledTimes(2);
 await act(async()=>{await result.current.perform(result.current.state.intent!);});expect(fetcher).toHaveBeenCalledTimes(3);expect(new Headers(fetcher.mock.calls[2][1].headers).get('Idempotency-Key')).toBe('original-key');expect(JSON.parse(fetcher.mock.calls[2][1].body)).toEqual(body);
});
it('snapshots the submitted body and rejects changed-body retries',async()=>{
 const fetcher=vi.fn().mockRejectedValueOnce(new Error()).mockImplementationOnce(()=>Promise.resolve(response({status:'unknown'})));vi.stubGlobal('fetch',fetcher);
 const {result}=renderHook(()=>useAlertCommands(context));const input={...intent,body:{...body}};
 await act(async()=>{await result.current.perform(input);});input.body.name='Edited later';
 expect((result.current.state.intent as typeof intent).body.name).toBe('Policy');
 await act(async()=>{await result.current.reconcile();await result.current.perform(input);});expect(fetcher).toHaveBeenCalledTimes(2);
});
it('accepts a committed lookup without issuing another mutation',async()=>{
 const fetcher=vi.fn().mockRejectedValueOnce(new Error()).mockImplementationOnce(()=>Promise.resolve(response({status:'committed',result:committed})));vi.stubGlobal('fetch',fetcher);
 const {result}=renderHook(()=>useAlertCommands(context));await act(async()=>{await result.current.perform(intent);});
 let value;await act(async()=>{value=await result.current.reconcile();});expect(value).toEqual(committed);expect(result.current.state.status).toBe('committed');expect(fetcher).toHaveBeenCalledTimes(2);
 act(()=>result.current.clearResult());expect(result.current.state).toMatchObject({status:'idle',intent:null});
});
it.each(['different actor','restricted','unsupported role','signed out'])('guards reconciliation and retry with %s context',async(kind)=>{
 const fetcher=vi.fn().mockRejectedValue(new Error());vi.stubGlobal('fetch',fetcher);
 const {result,rerender}=renderHook(({auth}:{auth:AuthContext|null})=>useAlertCommands(auth),{initialProps:{auth:context as AuthContext|null}});
 await act(async()=>{await result.current.perform(intent);});
 const next=kind==='signed out'?null:{...context,session:{...context.session!,...(kind==='different actor'?{user_id:id}:kind==='restricted'?{restricted:true}:{role:'unknown'})}};
 rerender({auth:next});await act(async()=>{await result.current.reconcile();await result.current.perform(intent);});
 expect(fetcher).toHaveBeenCalledTimes(1);expect(result.current.state.intent?.actorID).toBe(actor);expect(result.current.state.status).toBe('uncertain');expect(result.current.state.canRetry).toBe(false);
});
it('allows same-actor viewer receipt reads but never retries its write',async()=>{
 const fetcher=vi.fn().mockRejectedValueOnce(new Error()).mockImplementationOnce(()=>Promise.resolve(response({status:'unknown'})));vi.stubGlobal('fetch',fetcher);
 const {result,rerender}=renderHook(({auth})=>useAlertCommands(auth),{initialProps:{auth:context}});await act(async()=>{await result.current.perform(intent);});
 rerender({auth:{...context,session:{...context.session!,role:'viewer'}}});await act(async()=>{await result.current.reconcile();await result.current.perform(intent);});expect(fetcher).toHaveBeenCalledTimes(2);expect(result.current.state.canRetry).toBe(false);
});
it('requires an explicit release after receipt expiry, and preserves unresolved commands',async()=>{
 const fetcher=vi.fn().mockRejectedValueOnce(new Error()).mockImplementationOnce(()=>Promise.resolve(response({status:'expired'}))).mockImplementationOnce(()=>Promise.resolve(response(committed,201)));vi.stubGlobal('fetch',fetcher);
 const {result}=renderHook(()=>useAlertCommands(context));await act(async()=>{await result.current.perform(intent);});
 act(()=>{result.current.releaseExpired();result.current.clearResult();});expect(result.current.state.status).toBe('uncertain');
 await act(async()=>{await result.current.reconcile();});expect(result.current.state).toMatchObject({status:'expired',canRetry:false,intent:{key:'original-key'}});
 await act(async()=>{await result.current.perform(intent);});expect(fetcher).toHaveBeenCalledTimes(2);
 act(()=>result.current.releaseExpired());expect(result.current.state).toMatchObject({status:'idle',intent:null});
 await act(async()=>{await result.current.perform({...intent,key:'deliberate-new-key'});});expect(fetcher).toHaveBeenCalledTimes(3);
});
it('leaves a known conflict rejected without an automatic refresh or active intent',async()=>{
 const fetcher=vi.fn().mockImplementation(()=>Promise.resolve(response({code:'version_conflict',request_id:'conflict-request',message:'private'},409)));vi.stubGlobal('fetch',fetcher);
 const {result}=renderHook(()=>useAlertCommands(context));await act(async()=>{await result.current.perform(intent);});
 expect(result.current.state).toMatchObject({status:'rejected',intent:null,canRetry:false});expect(result.current.state.error).toContain('changed');expect(fetcher).toHaveBeenCalledTimes(1);
});
it('admits one request at a time and aborts only its owned request on host unmount',async()=>{
 const pending=deferred<Response>();const fetcher=vi.fn().mockReturnValue(pending.promise);vi.stubGlobal('fetch',fetcher);
 const {result,unmount}=renderHook(()=>useAlertCommands(context));let completion!:Promise<unknown>;
 act(()=>{completion=result.current.perform(intent);});await act(async()=>{await result.current.perform(intent);await result.current.reconcile();});expect(fetcher).toHaveBeenCalledTimes(1);
 unmount();expect(fetcher.mock.calls[0][1].signal.aborted).toBe(true);expect(await completion).toBeUndefined();
});
it('does not publish a former actor result after authority changes in flight',async()=>{
 const pending=deferred<Response>();vi.stubGlobal('fetch',vi.fn().mockReturnValue(pending.promise));
 const {result,rerender}=renderHook(({auth})=>useAlertCommands(auth),{initialProps:{auth:context}});let completion!:Promise<unknown>;
 act(()=>{completion=result.current.perform(intent);});rerender({auth:{...context,session:{...context.session!,user_id:id}}});
 await act(async()=>{pending.resolve(response(committed,201));expect(await completion).toBeUndefined();});expect(result.current.state).toMatchObject({status:'uncertain',intent:{actorID:actor}});expect(result.current.state.result).toBeUndefined();
});
