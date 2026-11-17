import { useCallback, useEffect, useRef, useState } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, PageHeader, Pager, Panel, RefreshButton, SearchField, StatusChip, Toast, Toolbar, ToolbarSpacer, resourceRowClick } from '../components/ui/index.ts';
import { journeyContentReady, routeName, beginJourney } from '../telemetry/client.ts';
import { sloClient, type Slo } from '../slos/client.ts';
import { percentExact } from '../slos/detailFormat.ts';
import { AlertPolicyForm, type AlertSloOption } from './AlertPolicyForm.tsx';
import { AlertOccurrenceList, emptyAlertListFilters, type AlertListFilters } from './AlertOccurrenceList.tsx';
import { alertClient, type Policy, type PolicyPage, type AlertPage, type Processing, type CommandResult } from './client.ts';
import { occurrencePage, policyBody, policyDraft, exactPageCount } from './adapters.ts';
import type { AlertPolicyDraft } from './policyDraft.ts';
import { ModuleView } from '../components/ModuleView.tsx';
const loadAlertDetail=()=>import('./AlertDetailScreen.tsx').then(module=>({default:module.AlertDetailScreen}));
import './alerts-page.css';

import type { AlertIntent, useAlertCommands } from './useAlertCommands.ts';
type Commands=ReturnType<typeof useAlertCommands>;
type Intent=AlertIntent extends infer T ? T extends AlertIntent ? Omit<T,'actorID'|'origin'> : never : never;
const message=(error:unknown)=>error instanceof Error ? error.message : 'The request could not be completed.';
const href=()=>location.pathname+location.search;
function navigate(path:string,state:unknown={}) { if(href()!==path){history.pushState(state,'',path);dispatchEvent(new PopStateEvent('popstate'));} }
function sloOption(slo:Slo):AlertSloOption {
  return {id:slo.id,label:slo.display_name,detail:`${slo.service_display_name} · ${percentExact(slo.target)} target · ${slo.rolling_window_seconds/86400} days`,target:slo.target,cadenceSeconds:slo.cadence_seconds,windowSeconds:slo.rolling_window_seconds};
}
const emptyPage:AlertPage={items:[],groups:[],total:'0'};
const filterParams={policyName:'q',sloId:'slo_id',severity:'severity',state:'state',evidence:'evidence',minimumAge:'minimum_duration_seconds'} as const;

export function AlertsPage({context,commands}:{context:AuthContext;commands:Commands}) {
  const [route,setRoute]=useState(href),[refresh,setRefresh]=useState(0),[completedRoute,setCompletedRoute]=useState(''),[sloReadyRoute,setSloReadyRoute]=useState('');
  const [alerts,setAlerts]=useState<AlertPage>(emptyPage),[policies,setPolicies]=useState<PolicyPage>({items:[],total:'0'});
  const [policy,setPolicy]=useState<Policy|null>(null),[etag,setEtag]=useState(''),[processing,setProcessing]=useState<Processing|null>(null);
  const [slos,setSlos]=useState<AlertSloOption[]>([]),[search,setSearch]=useState('');
  const [busy,setBusy]=useState(true),[readError,setError]=useState(''),[notice,setNotice]=useState('');
  const [receiptBusy,setReceiptBusy]=useState(false),[currentReview,setCurrentReview]=useState('');
  const {intent,status}=commands.state;
  const pending=status==='sending'||receiptBusy,uncertain=status==='uncertain'||status==='expired',error=readError||commands.state.error;
  const [serverTime,setServerTime]=useState(()=>new Date().toISOString());
  const mounted=useRef(true);
  const url=new URL(route,location.origin),path=url.pathname, params=url.searchParams;
  const newPolicy=path==='/alerts/policies/new',policyID=/^\/alerts\/policies\/([0-9a-f-]{36})$/.exec(path)?.[1];
  const occurrenceID=/^\/alerts\/([0-9a-f-]{36})$/.exec(path)?.[1];
  const policyList=path==='/alerts/policies', form=newPolicy||!!policyID, view=params.get('view')==='history'?'history':'active';
  const initialSloRoute=newPolicy?route:'';
  const pageSize=params.get('limit')==='10'?10:20,cursor=params.get('cursor')??'';
  const rawPage=Number(params.get('page')??'0'),pageIndex=Number.isSafeInteger(rawPage)&&rawPage>=0?rawPage:0;
  const grouped=params.get('group_by')==='slo';
  const filters=emptyAlertListFilters();
  for(const [key,param] of Object.entries(filterParams))filters[key as keyof AlertListFilters]=params.get(param)??'';
  const canManage=context.session?.role==='administrator'||context.session?.role==='editor';

  useEffect(()=>{mounted.current=true;const change=()=>setRoute(href());addEventListener('popstate',change);return()=>{mounted.current=false;removeEventListener('popstate',change);};},[]);
  useEffect(()=>{
    const controller=new AbortController();setBusy(true);setError('');setPolicy(null);setProcessing(null);setAlerts(emptyPage);setPolicies({items:[],total:'0'});
    async function read(){
      try {
        if(form){
          if(policyID){const result=await alertClient.policy(policyID,controller.signal);if(controller.signal.aborted)return;setPolicy(result.value);setEtag(result.etag);
            const selected=await sloClient.get(result.value.slo_id,controller.signal);if(controller.signal.aborted)return;
            setSlos(current=>[sloOption(selected.value),...current.filter(value=>value.id!==selected.value.id)]);
            const health=await alertClient.processing(policyID,controller.signal);if(!controller.signal.aborted)setProcessing(health.value);
          }
        } else if(policyList){
          const result=await alertClient.policies({q:params.get('q')||undefined,limit:pageSize,cursor:cursor||undefined},controller.signal);
          if(!controller.signal.aborted){setPolicies(result.value);if(result.serverTime)setServerTime(result.serverTime);}
        } else if(!occurrenceID){
          const result=await alertClient.list({scope:view,q:filters.policyName||undefined,slo_id:filters.sloId||undefined,severity:filters.severity||undefined,state:filters.state||undefined,
            evidence:['matching','unavailable','waiting'].includes(filters.evidence)?filters.evidence as 'matching'|'unavailable'|'waiting':undefined,minimum_duration_seconds:filters.minimumAge?Number(filters.minimumAge):undefined,group_by:grouped?'slo':'none',limit:pageSize,cursor:cursor||undefined},controller.signal);
          if(!controller.signal.aborted){setAlerts(result.value);if(result.serverTime)setServerTime(result.serverTime);}
        }
      } catch(failure){if(!controller.signal.aborted)setError(message(failure));}
      finally{if(!controller.signal.aborted){setCompletedRoute(route);setBusy(false);}}
    }
    void read();return()=>controller.abort();
  // Route owns the exact filter/cursor identity; aborted reads never publish into a newer route.
  },[route,refresh]);
  useEffect(()=>{if(!busy&&completedRoute===route&&!occurrenceID&&(!newPolicy||sloReadyRoute===route||!!readError))journeyContentReady(routeName(path),!!readError);},[busy,readError,route,completedRoute,newPolicy,sloReadyRoute]);
  useEffect(()=>{
    if(occurrenceID)return;
    const controller=new AbortController(),selectedID=policy?.slo_id??filters.sloId,timer=setTimeout(()=>{
      void sloClient.overview({q:search||undefined,limit:20},controller.signal).then(async result=>{
        if(selectedID && !result.items.some(item=>item.slo.id===selectedID)){const selected=await sloClient.get(selectedID,controller.signal);if(!controller.signal.aborted)setSlos(current=>[sloOption(selected.value),...current.filter(value=>value.id!==selectedID)]);}
        if(controller.signal.aborted)return;
        setSlos(current=>{const selected=current.find(value=>value.id===(policy?.slo_id??filters.sloId));const values=result.items.map(item=>sloOption(item.slo));return selected&&!values.some(value=>value.id===selected.id)?[selected,...values]:values;});
        if(newPolicy)setSloReadyRoute(route);
      }).catch(failure=>{if(!controller.signal.aborted)setError(message(failure));});
    },search?250:0);
    return()=>{clearTimeout(timer);controller.abort();};
  },[search,form,policy?.slo_id,filters.sloId,occurrenceID,initialSloRoute]);

  function changeQuery(values:Record<string,string>,resetPage=true){const next=new URLSearchParams(params);if(resetPage){next.delete('cursor');next.delete('page');}for(const [key,value]of Object.entries(values)){if(value)next.set(key,value);else next.delete(key);}navigate(`${path}${next.size?'?'+next:''}`);}
  function refreshPage(){beginJourney(route,false,true);setRefresh(value=>value+1);}
  function nextPage(next?:string){if(next){const query=new URLSearchParams(params);query.set('cursor',next);query.set('page',String(pageIndex+1));navigate(`${path}?${query}`,{alertPrevious:route});}}
  function previousPage(){if(typeof history.state?.alertPrevious==='string')history.back();else changeQuery({cursor:'',page:''});}
  function accepted(result:CommandResult){
    commands.clearResult();setError('');
    const saved=result.operation==='alert-policy.create'||result.operation==='alert-policy.update';
    setNotice(saved?'Alert policy saved.':result.operation==='alert.acknowledge'?'Alert acknowledged.':result.operation==='alert-policy.disable'?'Alert policy disabled.':result.operation==='alert-policy.enable'?'Alert policy enabled.':'Evaluation retry requested.');
    if(saved){if(href()==='/alerts/policies')setRefresh(value=>value+1);else navigate('/alerts/policies');}
    else setRefresh(value=>value+1);
  }
  useEffect(()=>{if(commands.state.status==='committed'&&commands.state.result&&commands.state.intent?.actorID===context.session?.user_id)accepted(commands.state.result);},[commands.state.status,commands.state.result]);
  const dismissNotice=useCallback(()=>setNotice(''),[]);
  const toast=<Toast message={notice} onDismiss={dismissNotice}/>;
  function perform(command:Intent){if(!context.session)return;void commands.perform({...command,actorID:context.session.user_id,origin:route} as AlertIntent);}
  async function inspectCurrent(){
    if(!intent||intent.actorID!==context.session?.user_id)return;
    setReceiptBusy(true);setCurrentReview('');
    try{
      if(intent.operation==='alert.acknowledge'){const result=await alertClient.occurrence(intent.id);setCurrentReview(`Current alert: ${result.value.state}, version ${result.value.version}.`);}
      else if(intent.operation==='alert-policy.create'){const result=await alertClient.policies({q:intent.body.name,limit:20});setCurrentReview(`${result.value.total} policies match the original name search. A name search cannot prove that an expired create failed. Review the saved policies before releasing this command.`);}
      else{const result=await alertClient.policy(intent.id);setCurrentReview(`Current policy: ${result.value.name}, version ${result.value.version}, ${result.value.enabled?'enabled':'disabled'}.`);}
    }catch(failure){setError(message(failure));}finally{if(mounted.current)setReceiptBusy(false);}
  }
  function save(draft:AlertPolicyDraft){if(intent)return;const body=policyBody(draft);void perform(policy?{operation:'alert-policy.update',id:policy.id,etag,key:crypto.randomUUID(),body}:{operation:'alert-policy.create',id:'',etag:'',key:crypto.randomUUID(),body});}
  function policyCommand(action:'enable'|'disable'|'retry'){if(!policy||intent)return;void perform({operation:`alert-policy.${action}`,id:policy.id,etag,key:crypto.randomUUID()});}
  function acknowledge(id:string,version:string){if(intent)return;void perform({operation:'alert.acknowledge',id,etag:`"alert-occurrence:${id}:${version}"`,key:crypto.randomUUID()});}
  const commandNotice=uncertain&&intent?<Panel heading="Command outcome needs confirmation"><p role="alert">{error}</p><p>The original command is retained. Check its result before starting another change.</p><div className="alert-page-actions"><Button pending={pending} onClick={()=>void commands.reconcile()}>Check command result</Button>{commands.state.canRetry&&<Button variant="outline" disabled={pending} onClick={()=>void commands.perform(intent)}>Retry same command</Button>}{status==='expired'&&<Button variant="outline" pending={receiptBusy} onClick={()=>void inspectCurrent()}>Read current resource</Button>}</div>{status==='expired'&&currentReview&&<div><p>{currentReview}</p><a href={intent.operation==='alert.acknowledge'?`/alerts/${intent.id}`:intent.operation==='alert-policy.create'?`/alerts/policies?q=${encodeURIComponent(intent.body.name)}`:`/alerts/policies/${intent.id}`} data-app-route>Review saved resource</a><p><Button variant="outline" onClick={()=>{commands.releaseExpired();setCurrentReview('');}}>I have reviewed the current state; release command</Button></p></div>}</Panel>:null;
  const crumbs=<nav className="app-breadcrumbs" aria-label="Breadcrumb"><a href="/alerts" data-app-route>Alerts</a>{path!=='/alerts'&&<><span aria-hidden="true">/</span><a href="/alerts/policies" data-app-route>Policies</a>{form&&<><span aria-hidden="true">/</span><span aria-current="page">{newPolicy?'New policy':policy?.name??'Policy'}</span></>}</>}</nav>;
  if(form)return <div className="alerts-page">{toast}{crumbs}<PageHeader title={newPolicy?'New alert policy':policy?.name??'Alert policy'} actions={policy&&canManage?<div className="alert-page-actions">{processing?.exhausted&&<Button variant="outline" disabled={pending||!!intent} onClick={()=>policyCommand('retry')}>Retry evaluation</Button>}<Button variant="outline" disabled={pending||!!intent} onClick={()=>policyCommand(policy.enabled?'disable':'enable')}>{policy.enabled?'Disable policy':'Enable policy'}</Button></div>:undefined}/>
    {commandNotice}{policy&&commands.state.status==='rejected'&&<Button variant="link" onClick={()=>{commands.clearResult();setRefresh(value=>value+1);}}>Reload saved policy and discard edits</Button>}{policy&&<p className="alert-page-context">{policy.enabled?'Enabled':'Disabled'} · Revision {policy.evaluation_revision}{processing&&!processing.last_cutoff?' · Awaiting first evaluation':''}{processing?.waiting_reason?` · ${processing.waiting_reason.replaceAll('_',' ')}`:''}</p>}
    {!canManage?<Panel heading="Policy is read only"><p>Your role can view alert policies. An administrator or editor can change them.</p>{policy&&<PolicySummary policy={policy}/>}</Panel>:newPolicy||policy?<AlertPolicyForm key={policy?`${policy.id}:${policy.version}`:'new'} slos={slos} onSearch={setSearch} initialDraft={policy?policyDraft(policy):undefined} onSave={save} onCancel={()=>navigate('/alerts/policies')} pending={pending||!!intent} error={!uncertain?error:undefined}/>:busy?<span role="status" className="sr-only">Opening alert policy</span>:<p role="alert">{error}</p>}
  </div>;
  if(occurrenceID)return <div className="alerts-page">{toast}{commandNotice}<ModuleView load={loadAlertDetail} componentProps={{id:occurrenceID,context,refresh,now:serverTime,canAcknowledge:!!canManage&&!intent,onAcknowledge:acknowledge,pending,error,onRefresh:refreshPage}} fallback={<><PageHeader title="Alert details"/><span className="sr-only" role="status">Opening alert details</span></>}/></div>;
  if(policyList){const total=exactPageCount(policies.total);return <div className="alerts-page">{toast}{crumbs}<PageHeader title="Alert policies" actions={<div className="alert-page-actions"><a className="ui-button ui-button--outline" href="/alerts" data-app-route>View alerts</a>{canManage&&<a className="ui-button ui-button--primary" href="/alerts/policies/new" data-app-route>New alert policy</a>}</div>}/>{commandNotice}
    <Toolbar><SearchField id="alert-policy-search" label="Search policies" value={params.get('q')??''} onChange={event=>changeQuery({q:event.target.value})}/><ToolbarSpacer/><RefreshButton label="Refresh policies" busy={busy} onClick={refreshPage}/></Toolbar>
    {error&&!uncertain&&<p role="alert">{error}</p>}<div className="ui-resource-panel" role="region" aria-label="Alert policies" tabIndex={0} aria-busy={busy||undefined}><table className="ui-list-table ui-resource-table alert-policy-table"><thead><tr><th scope="col">Policy</th><th scope="col">SLO</th><th scope="col">Conditions</th><th scope="col">Status</th></tr></thead><tbody>{policies.items.map(item=><tr key={item.policy.id} className="ui-resource-row" onClick={event=>resourceRowClick(event,()=>navigate(`/alerts/policies/${item.policy.id}`))}><td data-label="Policy"><a href={`/alerts/policies/${item.policy.id}`} data-app-route>{item.policy.name}</a><span className="ui-list-table__sub">{item.policy.severity[0]!.toUpperCase()+item.policy.severity.slice(1)} severity</span></td><td data-label="SLO"><a className="ui-resource-wrap" href={`/slos/${item.policy.slo_id}`} data-app-route>{item.slo_name}</a></td><td data-label="Conditions">{item.policy.conditions.length} {item.policy.conditions.length===1?'condition':'conditions'}</td><td data-label="Status"><StatusChip tone={!item.policy.enabled?'none':item.processing.exhausted?'warning':item.processing.waiting_reason||!item.processing.last_cutoff||item.processing.lag_seconds>0?'stale':'healthy'} label={!item.policy.enabled?'Disabled':item.processing.exhausted?'Evaluation stopped':item.processing.waiting_reason?'Waiting for calculation':!item.processing.last_cutoff?'Awaiting first evaluation':item.processing.lag_seconds>0?'Behind schedule':'Enabled'}/>{item.open_occurrence_id&&<span className="ui-list-table__sub"><a href={`/alerts/${item.open_occurrence_id}`} data-app-route>View active alert</a></span>}</td></tr>)}{!policies.items.length&&<tr><td colSpan={4}>{busy?<span className="sr-only" role="status">Opening policies</span>:error?'Policies could not be loaded.':params.get('q')?'No policies match this search.':'No alert policies yet.'}</td></tr>}</tbody></table>
    <Pager id="policy-page-size" label="Policy pages" position={`${policies.items.length?pageIndex*pageSize+1:0}–${pageIndex*pageSize+policies.items.length} of ${total}`} pageSize={pageSize} onPageSizeChange={size=>changeQuery({limit:String(size)})} hasPrevious={pageIndex>0} hasNext={!!policies.next_cursor} onPrevious={previousPage} onNext={()=>nextPage(policies.next_cursor)} busy={busy}/></div>
  </div>;}
  return <div className="alerts-page">{toast}{commandNotice}
    <AlertOccurrenceList page={occurrencePage(alerts,grouped,pageIndex,pageSize,context.session?.user_id)} pageSize={pageSize} filters={filters} view={view} onViewChange={value=>changeQuery({view:value==='history'?'history':'',state:''})} policiesHref="/alerts/policies" grouped={grouped} now={serverTime} slos={slos} canAcknowledge={!!canManage&&!intent} newPolicyHref="/alerts/policies/new" canCreate={!!canManage} onFiltersChange={values=>changeQuery(Object.fromEntries(Object.entries(filterParams).map(([key,param])=>[param,values[key as keyof AlertListFilters]])))} onGroupChange={value=>changeQuery({group_by:value?'slo':''})} onPageSizeChange={size=>changeQuery({limit:String(size)})} onPrevious={previousPage} onNext={()=>nextPage(alerts.next_cursor)} onRefresh={refreshPage} onSloSearch={setSearch} onOpen={navigate} onAcknowledge={acknowledge} acknowledgement={intent?.operation==='alert.acknowledge'?{id:intent.id,pending,error}:undefined} busy={busy} error={!uncertain?error:undefined}/>
  </div>;
}
function PolicySummary({policy}:{policy:Policy}){return <dl><dt>SLO</dt><dd><a href={`/slos/${policy.slo_id}`} data-app-route>Open SLO</a></dd><dt>Severity</dt><dd>{policy.severity}</dd><dt>Conditions</dt><dd>{policy.conditions.map(value=><p key={value.id}>{value.short_window_seconds/60} min and {value.long_window_seconds/60} min at {value.threshold}×</p>)}</dd></dl>;}
