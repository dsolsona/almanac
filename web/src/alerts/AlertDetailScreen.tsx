import { useEffect, useRef, useState } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, PageHeader } from '../components/ui/index.ts';
import { AlertOccurrenceDetail, type AlertOccurrenceDetailData } from './AlertOccurrenceDetail.tsx';
import { alertClient, type Series } from './client.ts';
import { occurrenceDetail } from './detailAdapters.ts';
import { journeyContentReady } from '../telemetry/client.ts';

type Props={id:string;context:AuthContext;refresh:number;now:string;canAcknowledge:boolean;onAcknowledge:(id:string,version:string)=>void;pending:boolean;error:string;onRefresh:()=>void};
const message=(error:unknown)=>error instanceof Error?error.message:'Alert evidence could not be loaded.';
export function AlertDetailScreen(props:Props){
  const [data,setData]=useState<AlertOccurrenceDetailData|null>(null),[error,setError]=useState(''),[warnings,setWarnings]=useState<string[]>([]);
  const [settledID,setSettledID]=useState('');
  const [busy,setBusy]=useState(true),[now,setNow]=useState(props.now),[transitionCursor,setTransitionCursor]=useState(''),[moreBusy,setMoreBusy]=useState(false);
  const lifecycleController=useRef<AbortController|null>(null);
  useEffect(()=>{
    const controller=new AbortController();setBusy(true);setSettledID('');setError('');setWarnings([]);setData(null);setTransitionCursor('');setMoreBusy(false);lifecycleController.current?.abort();
    async function load(){
      try{
        const [record,original,latest]=await Promise.all([alertClient.occurrence(props.id,controller.signal),alertClient.evidence(props.id,'firing',controller.signal),alertClient.evidence(props.id,'latest',controller.signal)]);
        const [processing,transitions]=await Promise.all([alertClient.processing(record.value.policy_id,controller.signal),alertClient.transitions(props.id,{limit:20},controller.signal)]);
        const series=new Map<string,Series>(),notices:string[]=[];
        await Promise.all(latest.value.evaluation.conditions.map(async condition=>{
          try{const result=await alertClient.series(props.id,condition.id,{},controller.signal);
            if(result.value.policy_id!==record.value.policy_id||result.value.evaluation_revision!==record.value.evaluation_revision)throw new Error('Chart revision changed.');
            series.set(condition.id,result.value);
            if(result.value.gaps.some(gap=>gap.basis==='effective_cutoff'&&gap.kind!=='unavailable'))notices.push('Some evaluation history is not retained. Gaps are left open in the chart.');
          }catch(failure){if(!controller.signal.aborted)notices.push('Chart history could not be loaded. Exact saved evidence remains available.');}
        }));
        if(controller.signal.aborted)return;
        setData(occurrenceDetail(record.value,original.value,latest.value,processing.value,transitions.value,series,props.context.session?.user_id));
        setTransitionCursor(transitions.value.next_cursor??'');setWarnings([...new Set(notices)]);if(record.serverTime)setNow(record.serverTime);
      }catch(failure){if(!controller.signal.aborted)setError(message(failure));}
      finally{if(!controller.signal.aborted){setSettledID(props.id);setBusy(false);}}
    }
    void load();return()=>{controller.abort();lifecycleController.current?.abort();};
  },[props.id,props.refresh,props.context.session?.user_id]);
  useEffect(()=>{if(!busy&&settledID===props.id&&(!!error||data?.id===props.id))journeyContentReady('alert.detail',!!error||warnings.some(value=>value.startsWith('Chart history could not')));},[busy,error,warnings,data,props.id,settledID]);
  async function more(){
    if(moreBusy||!transitionCursor)return;
    const controller=new AbortController();lifecycleController.current=controller;setMoreBusy(true);
    try{const result=await alertClient.transitions(props.id,{limit:20,cursor:transitionCursor},controller.signal);if(controller.signal.aborted)return;
      setData(current=>current?{...current,lifecycle:[...current.lifecycle,...result.value.items.map(item=>({id:item.id,at:item.detected_at,effectiveAt:item.effective_at,title:item.to[0]!.toUpperCase()+item.to.slice(1),description:item.reason??item.cause.replaceAll('_',' '),actor:item.actor_id===props.context.session?.user_id?'You':item.actor_id}))]}:current);
      setTransitionCursor(result.value.next_cursor??'');
    }catch(failure){if(!controller.signal.aborted)setError(message(failure));}finally{if(!controller.signal.aborted)setMoreBusy(false);}
  }
  return <div aria-busy={busy||undefined}>{warnings.map(value=><p className="alert-list-notice" key={value}>{value}</p>)}{error&&<p role="alert">{error} <Button variant="link" onClick={props.onRefresh}>Refresh alert</Button></p>}
    {data?.id===props.id?<><AlertOccurrenceDetail data={data} now={now} backHref="/alerts" canAcknowledge={props.canAcknowledge} onAcknowledge={props.onAcknowledge} onRefresh={props.onRefresh} acknowledgement={{pending:props.pending,error:props.error||undefined}}/>{transitionCursor&&<Button variant="outline" pending={moreBusy} onClick={()=>void more()}>More lifecycle events</Button>}</>:<><PageHeader title="Alert details"/>{busy&&<span className="sr-only" role="status">Opening alert details</span>}</>}
  </div>;
}
