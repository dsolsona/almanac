import {useEffect,useRef,useState} from 'react';
import type {AuthContext} from '../auth/client.ts';
import {alertClient,AlertFailure,type CommandResult,type PolicyPatch,type PolicyWrite} from './client.ts';

type IntentIdentity={key:string;etag:string;id:string;actorID:string;origin:string};
export type AlertIntent=IntentIdentity&(
 {operation:'alert-policy.create';body:PolicyWrite}|
 {operation:'alert-policy.update';body:PolicyPatch}|
 {operation:'alert-policy.enable'|'alert-policy.disable'|'alert-policy.retry'|'alert.acknowledge'}
);
export type AlertCommandState={
 intent:AlertIntent|null;
 status:'idle'|'sending'|'uncertain'|'expired'|'committed'|'rejected';
 error:string;
 result?:CommandResult;
 canRetry:boolean;
};
const idle=():AlertCommandState=>({intent:null,status:'idle',error:'',canRetry:false});
const authorityMessage='Sign in as the original actor with permission for this operation before reconciling or retrying it.';
function authorized(context:AuthContext|null,intent:AlertIntent,write=false):context is AuthContext{
 const session=context?.session;
 return !!session&&!session.restricted&&session.user_id===intent.actorID&&(write?['administrator','editor']:['administrator','editor','viewer']).includes(session.role);
}
function sameIntent(a:AlertIntent,b:AlertIntent):boolean{
 return a.key===b.key&&a.id===b.id&&a.etag===b.etag&&a.actorID===b.actorID&&a.origin===b.origin&&a.operation===b.operation&&JSON.stringify('body'in a?a.body:null)===JSON.stringify('body'in b?b.body:null);
}
const safeError=(error:unknown,fallback:string)=>error instanceof AlertFailure?error.message:fallback;

/** Own this hook above route consumers. It retains only safe command inputs in
 * memory, never credentials. A full host/page unmount aborts its request; a route
 * consumer unmount does not. Nothing generates keys or automatically retries. */
export function useAlertCommands(context:AuthContext|null){
 const [state,setState]=useState<AlertCommandState>(idle);
 const stateRef=useRef(state),contextRef=useRef(context),mounted=useRef(true),active=useRef<AbortController|null>(null);
 contextRef.current=context;
 useEffect(()=>{mounted.current=true;return()=>{mounted.current=false;active.current?.abort();};},[]);
 function publish(value:AlertCommandState){stateRef.current=value;if(mounted.current)setState(value);}
 function deny(){const current=stateRef.current;publish({...current,status:current.intent?current.status:'rejected',error:authorityMessage,canRetry:false,result:undefined});}
 function uncertain(intent:AlertIntent,error:string,status:'uncertain'|'expired'='uncertain',canRetry=false){publish({intent,status,error,canRetry});}
 async function perform(requested:AlertIntent):Promise<CommandResult|undefined>{
  if(active.current||!mounted.current)return;
  const current=stateRef.current;
  if(!authorized(contextRef.current,requested,true)){deny();return;}
  let intent:AlertIntent;
  if(current.status==='uncertain'||current.status==='expired'||current.status==='sending'){
   if(current.status!=='uncertain'||!current.canRetry||!current.intent||!sameIntent(current.intent,requested))return;
   intent=current.intent;
  }else{
   // A caller can continue editing its draft while this immutable input is in
   // flight; such edits must never change a later same-key replay.
   intent=structuredClone(requested);
  }
  const auth=contextRef.current;
  const controller=new AbortController();active.current=controller;
  publish({intent,status:'sending',error:'',canRetry:false});
  try{
   let response;
   switch(intent.operation){
    case 'alert-policy.create':response=await alertClient.create(intent.body,auth,intent.key,controller.signal);break;
    case 'alert-policy.update':response=await alertClient.update(intent.id,intent.body,intent.etag,auth,intent.key,controller.signal);break;
    case 'alert.acknowledge':response=await alertClient.acknowledge(intent.id,intent.etag,auth,intent.key,controller.signal);break;
    default:response=await alertClient.policyCommand(intent.id,intent.operation==='alert-policy.enable'?'enable':intent.operation==='alert-policy.disable'?'disable':'retry',intent.etag,auth,intent.key,{},controller.signal);break;
   }
   if(!mounted.current)return;
   if(!authorized(contextRef.current,intent)){uncertain(intent,authorityMessage);return;}
   publish({intent,status:'committed',error:'',result:response.value,canRetry:false});return response.value;
  }catch(error){
   if(!mounted.current)return;
   if(!authorized(contextRef.current,intent)){uncertain(intent,authorityMessage);return;}
   if(error instanceof AlertFailure&&!error.uncertain){publish({intent:null,status:'rejected',error:error.message,canRetry:false});}
   else uncertain(intent,safeError(error,'The operation may have completed. Check the original command result before deciding what to do next.'));
  }finally{if(active.current===controller)active.current=null;}
 }
 async function reconcile():Promise<CommandResult|undefined>{
  const current=stateRef.current,intent=current.intent;
  if(active.current||!mounted.current||!intent||!['uncertain','expired'].includes(current.status))return;
  if(!authorized(contextRef.current,intent)){deny();return;}
  const controller=new AbortController();active.current=controller;
  // Keep uncertainty/expiry visible while reading: no new mutation is admitted.
  publish({...current,error:'',canRetry:false});
  try{
   const response=await alertClient.commandResult(intent.operation,intent.key,controller.signal);
   if(!mounted.current)return;
   if(!authorized(contextRef.current,intent)){uncertain(intent,authorityMessage,current.status==='expired'?'expired':'uncertain');return;}
   if(response.value.status==='committed'&&response.value.result){publish({intent,status:'committed',error:'',result:response.value.result,canRetry:false});return response.value.result;}
   if(response.value.status==='expired')uncertain(intent,'The command receipt has expired. Inspect the current resource before explicitly releasing this command.','expired');
   else uncertain(intent,'No retained result is available. This does not prove the command failed. Check again or deliberately retry the original command.','uncertain',authorized(contextRef.current,intent,true));
  }catch(error){
   if(mounted.current)uncertain(intent,safeError(error,'The command result could not be checked. The original command is retained.'),current.status==='expired'?'expired':'uncertain');
  }finally{if(active.current===controller)active.current=null;}
 }
 /** Caller must first provide explicit human inspection/reconciliation of the
  * current resource. In particular, a bounded name search cannot prove that an
  * expired create never committed. This is a deliberate release, not a retry. */
 function releaseExpired(){const current=stateRef.current;if(active.current||current.status!=='expired'||!current.intent)return;if(!authorized(contextRef.current,current.intent)){deny();return;}publish(idle());}
 function clearResult(){if(!active.current&&(stateRef.current.status==='committed'||stateRef.current.status==='rejected'))publish(idle());}
 // Never expose an old actor's result or retry affordance through a new context.
 const visible=state.intent&&!authorized(context,state.intent)?{...state,result:undefined,canRetry:false}:state.intent&&!authorized(context,state.intent,true)?{...state,canRetry:false}:state;
 return {state:visible,perform,reconcile,releaseExpired,clearResult};
}
