import { reasonLabel, vocabulary } from './statusVocabulary.ts';
import { useEffect, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react';
import type { components } from '../api/generated.ts';
import { Button, FilterSelect, PageHeader, Pager, RefreshButton, SearchField, StatusChip, StatusMark, Toolbar, ToolbarSpacer } from '../components/ui/index.ts';
import { SloBudgetSparkline } from '../components/charts/SloBudgetSparkline.tsx';
import { sloClient, SloFailure, sameDerivedInputs, type OperationalDetail, type OperationalOverview } from './client.ts';
import type { CurrentStatus, ServerClock } from './client.ts';
import { clearQueryCache } from '../queryCache.ts';
import { beginJourney, journeyContentReady } from '../telemetry/client.ts';
import './slos.css';
import './slo-reference.css';
import './slo-detail.css';
import { SloHistoryChart } from '../components/charts/SloHistoryChart.tsx';
import { SloCompletedDays, SloDefinitionPanels, SloSummary, SloVerdict } from './SloDetailPanels.tsx';
import { durationText, percentExact } from './detailFormat.ts';

type OverviewItem=OperationalOverview['items'][number];
type Slo=components['schemas']['Slo'];
type Urgency=OverviewItem['urgency'];
const groups:[Urgency,string,string][]=[
  ['needs_attention','Needs attention','breached, burning fast, low on budget, or not reporting'],
  ['watch','Watch','under half the budget left, incomplete data, or unavailable burn'],
  ['healthy','Healthy','on track'],
];
const clockFormat=new Intl.DateTimeFormat('en-GB',{hour:'2-digit',minute:'2-digit',second:'2-digit',timeZone:'UTC',hour12:false});
const lastEvaluationFormat=new Intl.DateTimeFormat('en-GB',{day:'2-digit',month:'short',hour:'2-digit',minute:'2-digit',timeZone:'UTC',hour12:false});
function decimal(value:string|null|undefined):string {
  if(value==null)return '—';
  return value.replace(/(\.\d*?)0+$/,'$1').replace(/\.$/,'');
}
function shift(value:string,places:number):string {
  const negative=value.startsWith('-'),raw=negative?value.slice(1):value,[whole,frac='']=raw.split('.'),digits=whole+frac,point=whole.length+places;
  let moved=point<=0?`0.${'0'.repeat(-point)}${digits}`:point>=digits.length?`${digits}${'0'.repeat(point-digits.length)}`:`${digits.slice(0,point)}.${digits.slice(point)}`;
  moved=moved.replace(/^0+(?=\d)/,'').replace(/(\.\d*?)0+$/,'$1').replace(/\.$/,'');
  return `${negative?'-':''}${moved}`;
}
function budgetPercent(value:string|null,maximumFractionDigits=3):string { return value===null?'—':`${(Number(value)*100).toLocaleString(undefined,{maximumFractionDigits})}%`; }
function multiple(value:string|null):string {return value===null?'—':`${Number(value).toLocaleString(undefined,{maximumFractionDigits:2})}×`;}
type LegacyStatus='healthy'|'warning'|'breached'|'no-data'|'stale';
function statusFor(item:OverviewItem['current']):LegacyStatus {
  if(item.quality.freshness==='stale')return 'stale';
  if(item.performance.provisional)return 'warning';
  if(item.performance.state==='met')return 'healthy';
  if(item.performance.state==='violated')return 'breached';
  if(['no_data','expired_history','zero_traffic','not_applicable','not_yet_eligible'].includes(item.quality.state))return 'no-data';
  return 'warning';
}
function qualityWarning(quality:components['schemas']['SloOperationalQuality'],performance?:components['schemas']['SloEvaluationPerformance'],window?:components['schemas']['SloEvaluationWindow']):string {
  if(quality.freshness==='stale')return 'Observed values may be stale.';
  if(quality.state==='zero_traffic')return 'No requests were recorded.';
  if(quality.state==='not_applicable')return window&&Date.parse(window.end)<=Date.parse(window.evaluation_valid_from)?'No intervals are due yet. Waiting for the first SLO evaluation.':'No measurements apply to this period.';
  if(quality.state==='no_data')return 'No measurements are available yet.';
  if(quality.state==='expired_history')return 'Required history has expired.';
  if(quality.state==='not_yet_eligible')return 'No intervals are due yet. Waiting for the first completed collection interval.';
  if(quality.state==='unavailable')return quality.reason?reasonLabel(quality.reason):'This evaluation is unavailable.';
  if(performance?.provisional) {
    if(quality.known_empty_intervals>0)return `${quality.known_empty_intervals} interval${quality.known_empty_intervals===1?' returned':'s returned'} no measurement; figures cover observed traffic only.`;
    if(window&&Date.parse(window.effective_start)>Date.parse(window.requested_start))return 'This objective has a shortened eligible window; the result is observed only.';
    if(quality.state==='partial')return 'Observed traffic only; this result is provisional, not definitive.';
    return 'This result is provisional and must not be treated as definitive success or violation.';
  }
  if(quality.known_empty_intervals>0)return `${quality.known_empty_intervals} interval${quality.known_empty_intervals===1?' returned':'s returned'} no measurement.`;
  if(quality.state==='partial')return 'Observed traffic only; the full window is not definitive.';
  if(quality.coverage===null)return 'Collection coverage is unknown.';
  return '';
}
function formatExhaustion(item:OverviewItem['exhaustion']):string {
  if(item.state==='exhausted')return 'Exhausted';
  if(item.state==='unavailable')return 'Unavailable';
  return item.hours_until_exhaustion===null?'No finite estimate':`${decimal(item.hours_until_exhaustion)} h at the observed 1 h bad-event rate`;
}
function shortExhaustion(item:OverviewItem['exhaustion']):string {
  if(item.state==='exhausted')return 'exhausted';
  if(item.state!=='estimated'||item.hours_until_exhaustion===null)return '—';
  const hours=Number(item.hours_until_exhaustion);
  return hours<1?`≈ ${Math.ceil(hours*60)} min`:hours>=48?`≈ ${Math.ceil(hours/24)} d`:`≈ ${Math.ceil(hours)} h`;
}
function windowDuration(seconds:number):string {
  return seconds%86400===0?`${seconds/86400} d`:seconds%3600===0?`${seconds/3600} h`:`${seconds} s`;
}
function queryFrom(locationKey:string):URLSearchParams { return new URLSearchParams(locationKey.split('?')[1]??''); }
function updateQuery(locationKey:string,changes:Record<string,string>,remove:string[]=[]):string {
  const params=queryFrom(locationKey);
  for(const key of remove)params.delete(key);
  for(const [key,value] of Object.entries(changes)){if(value)params.set(key,value);else params.delete(key);}
  const query=params.toString();return `/slos${query?`?${query}`:''}`;
}
async function allServices(signal:AbortSignal) {
  const result=[] as Awaited<ReturnType<typeof sloClient.services>>['items'];let cursor='';const seen=new Set<string>();
  do {const page=await sloClient.services(cursor,signal);result.push(...page.items);cursor=page.next_cursor;if(cursor&&seen.has(cursor))throw new Error('Service pagination did not advance.');if(cursor)seen.add(cursor);}while(cursor);
  return result;
}

// A row opens its SLO on click unless the click selected text or hit a link inside the row (ADR-0032); the name
// stays a real link for keyboard, middle-click and screen readers, and there is no overlay, so text can be selected.
function opensRow(event:ReactMouseEvent<HTMLElement>):boolean {
  if(event.button!==0||event.metaKey||event.ctrlKey||event.shiftKey||event.altKey)return false;
  if(event.target instanceof Element&&event.target.closest('a,button,input,select,textarea,summary'))return false;
  return !window.getSelection()?.toString();
}
function OverviewRow({item,onChartReady,onOpen}:{item:OverviewItem;onChartReady:(item:OverviewItem,ready:boolean)=>void;onOpen:(path:string)=>void}) {
  useEffect(()=>()=>onChartReady(item,false),[item]);
  const lastKnown=item.current_state==='stale'||item.current_state==='failed';
  const slo=item.slo,p=item.current.performance,q=item.current.quality,status=lastKnown?'stale':statusFor(item.current);
  const reasons=item.reasons.map(reason=>reasonLabel(reason.code)+(reason.provisional?' · observed only':'')).join('; ');
  const words=vocabulary(item.urgency,item.leading_reason,lastKnown,item.current_state);
  const asOf=`${lastEvaluationFormat.format(new Date(item.current.evaluation_at))} UTC`;
  // The status cell is the chip only; its reason, the other reasons and any last-known time are in the tooltip.
  const statusTitle=lastKnown?`Last known values as of ${asOf}; current calculation ${item.current_state}. ${reasons}`:reasons||words.reason;
  const partial=p.provisional||q.state==='partial';
  const budget=p.remaining_fraction===null?null:Number(p.remaining_fraction),burn=item.burn_1h.performance.burn;
  const estimateNote=lastKnown?'last known rate':status==='stale'?'No estimate while not reporting':partial?'Partial window':status==='no-data'?'No samples':'';
  const exhaustionTitle=[formatExhaustion(item.exhaustion),estimateNote].filter(Boolean).join(' · ');
  const burnTitle=[`6 h: ${item.burn_6h.performance.burn===null?'no samples':multiple(item.burn_6h.performance.burn)}`,qualityWarning(item.burn_1h.quality)].filter(Boolean).join(' · ');
  const runsOut=shortExhaustion(item.exhaustion);
  const path=`/slos/${slo.id}`;
  return <article className="app-slo-row" role="row" onClick={event=>{if(opensRow(event))onOpen(path);}}>
    <div className="app-slo-name" role="cell"><a data-app-route className="app-slo-link" href={path} title={slo.display_name}>{slo.display_name}</a><small><a data-app-route href={`/services/${slo.service_id}`}>{slo.service_display_name}</a><span>·</span><a data-app-route className="app-slo-indicator" href={`/slis/${slo.sli_id}`}>{slo.sli_display_name}</a></small></div>
    <div className="app-slo-status" role="cell"><span className="slo-reference-status" data-status={status} title={statusTitle}><StatusChip tone={words.tone} label={words.label}/></span><span className="sr-only">{lastKnown?<>{item.current_state==='failed'?'Update failed · ':''}last known values as of <time dateTime={item.current.evaluation_at}>{asOf}</time>. {reasons}</>:statusTitle}</span></div>
    <div className="app-slo-objective" role="cell">{shift(slo.target,2)}% · {windowDuration(slo.rolling_window_seconds)}<span className="sr-only">Target {shift(slo.target,2)}% over {windowDuration(slo.rolling_window_seconds)}</span></div>
    <div className="app-slo-spark" role="cell" title={item.budget_history_generation?`History evaluated through ${item.budget_history_generation.evaluated_through}`:undefined}>{item.budget_sparkline.length===0&&item.budget_history_state?<span className="app-slo-history-empty">{item.budget_history_state==='unavailable'?'History unavailable':item.budget_history_state==='preparing'?'Preparing history…':'No budget history'}</span>:<SloBudgetSparkline samples={item.budget_sparkline} label={`${slo.display_name} rolling remaining budget`} onReady={()=>onChartReady(item,true)}/>} {item.budget_sparkline.length>0&&item.budget_history_state==='preparing'&&<small>Updating history</small>}{item.budget_sparkline.length>0&&item.budget_history_state==='unavailable'&&<small>History may be out of date</small>}</div>
    <div className="app-slo-budget" role="cell" title={[p.remaining_fraction===null?'':`${shift(p.remaining_fraction,2)}%`,partial?'Observed only; collection coverage is incomplete':'',p.remaining_budget===null?'':`${decimal(p.remaining_budget)} bad events left`].filter(Boolean).join(' · ')||undefined}><b className={budget===null||lastKnown?'muted':budget<0?'crit':budget<.2?'warn':''}>{budgetPercent(p.remaining_fraction,1)}</b><span className="sr-only">{partial?'Observed only; ':''}{p.remaining_budget===null?'Budget unavailable':`${decimal(p.remaining_budget)} bad events left`}</span></div>
    <div className="app-slo-burn" role="cell" title={burnTitle}><b className={burn===null?'muted':Number(burn)>=10?'hot':Number(burn)>=2?'warm':''}>{multiple(burn)}</b><span className="sr-only">{burnTitle}</span></div>
    <div className="app-slo-exhaustion" role="cell" title={exhaustionTitle}><b className={runsOut==='—'?'muted':item.exhaustion.state==='exhausted'||(item.exhaustion.hours_until_exhaustion!==null&&Number(item.exhaustion.hours_until_exhaustion)<24)?'hot':''}>{runsOut==='—'||runsOut==='exhausted'?runsOut:`in ${runsOut}`}</b><span className="sr-only">{exhaustionTitle}</span></div><span className="app-slo-chevron" role="cell" aria-hidden="true">›</span>
  </article>;
}

export function OperationalOverviewView({locationKey,canWrite,onCreate,onLocationChange}:{locationKey:string;canWrite:boolean;onCreate:()=>void;onLocationChange:(path:string,state?:unknown)=>void}) {
  const params=queryFrom(locationKey),query=params.get('q')??'',serviceID=params.get('service_id')??'',urgency=(params.get('urgency')??'') as ''|Urgency;
  const cursor=params.get('cursor')??'';
  const pageSize=params.get('page_size')==='10'?10:20;
  const [draftQuery,setDraftQuery]=useState(query),[page,setPage]=useState<OperationalOverview|null>(null),[services,setServices]=useState<Awaited<ReturnType<typeof sloClient.services>>['items']>([]);
  const [error,setError]=useState(''),[busy,setBusy]=useState(false),[refreshCount,setRefreshCount]=useState(0),[servicesError,setServicesError]=useState(''),[updatedAt,setUpdatedAt]=useState('');
  const lastKey=useRef('');
  const filtersKey=JSON.stringify([query,serviceID,urgency,cursor,pageSize]);
  const renderedKey=useRef('');
  const requestedKey=JSON.stringify([filtersKey,refreshCount]);
  const readyCharts=useRef(new WeakSet<OverviewItem>());
  const [chartRevision,setChartRevision]=useState(0);
  function chartReady(item:OverviewItem,ready:boolean){if(!ready){readyCharts.current.delete(item);return;}if(!readyCharts.current.has(item)){readyCharts.current.add(item);setChartRevision(value=>value+1);}}
  const pendingCharts=page?.items.some(item=>item.budget_sparkline.some(sample=>sample.remaining_fraction!==null)&&!readyCharts.current.has(item));
  useEffect(()=>{if(!busy&&renderedKey.current===requestedKey&&(error||page&&!pendingCharts))journeyContentReady('slo.list',!!error);},[page,busy,error,requestedKey,pendingCharts,chartRevision]);
  useEffect(()=>setDraftQuery(query),[query]);
  useEffect(()=>{
    const abort=new AbortController();
    void allServices(abort.signal).then(items=>{if(!abort.signal.aborted){setServices(items);setServicesError('');}}).catch(failure=>{if(!abort.signal.aborted){if(failure instanceof SloFailure&&(failure.status===401||failure.status===403)){setPage(null);setServices([]);setError('Your session can no longer access these SLOs. Sign in again to continue.');}else setServicesError('Service filters could not load. Refresh to try again.');}});
    return()=>abort.abort();
  },[refreshCount]);
  useEffect(()=>{
    const abort=new AbortController();
    const sameView=lastKey.current===filtersKey;
    if(!sameView){setPage(null);setError('');}
    lastKey.current=filtersKey;setBusy(true);
    const read=async()=>{
      try {
        const overview=await sloClient.overview({q:query,service_id:serviceID||undefined,urgency:urgency||undefined,limit:pageSize,cursor:cursor||undefined},abort.signal);
        if(!abort.signal.aborted){
          renderedKey.current=requestedKey;
          setPage(overview);setError('');setUpdatedAt(clockFormat.format(new Date()));
        }
      } catch(failure){if(!abort.signal.aborted){renderedKey.current=requestedKey;if(failure instanceof SloFailure&&(failure.status===401||failure.status===403))setPage(null);setError(failure instanceof Error?failure.message:'The operational overview could not be loaded.');}}
      finally{if(!abort.signal.aborted)setBusy(false);}
    };
    void read();return()=>abort.abort();
  },[locationKey,filtersKey,refreshCount]);
  function apply(changes:Record<string,string>) {onLocationChange(updateQuery(locationKey,changes,['cursor','previous_cursor']));}
  function changeUrgency(value:string) {apply({urgency:value});}
  function nextPage() {
    if(!page?.next_cursor)return;
    const params=queryFrom(locationKey);params.set('cursor',page.next_cursor);params.delete('previous_cursor');
    const queryString=params.toString();onLocationChange(`/slos?${queryString}`);
  }
  function previousPage() {
    if(!page?.previous_cursor)return;
    const params=queryFrom(locationKey);params.set('cursor',page.previous_cursor);params.delete('previous_cursor');
    const queryString=params.toString();onLocationChange(`/slos?${queryString}`);
  }
  function restartSnapshot(){apply({});}
  function refresh(){beginJourney(locationKey,false,true);if(cursor){onLocationChange(updateQuery(locationKey,{},['cursor','previous_cursor']));return;}setRefreshCount(value=>value+1);}
  const snapshotExpired=error.includes('snapshot expired')||error.includes('SLO list changed');
  const hasPrevious=Boolean(page?.previous_cursor);
  const lastConfirmedAt=page?.items.map(item=>item.current.evaluation_at).sort().at(-1);
  const totalCount=page?Object.values(page.group_counts).reduce((sum,count)=>sum+count,0):0;
  const matchingCount=page?(urgency?page.group_counts[urgency]:totalCount):0;
  // Triage callouts describe the loaded page: the projection does not expose global extremes.
  const soonest=page?.items.filter(item=>item.exhaustion.state==='estimated'&&item.exhaustion.hours_until_exhaustion!==null).sort((a,b)=>Number(a.exhaustion.hours_until_exhaustion)-Number(b.exhaustion.hours_until_exhaustion))[0];
  const exhausted=page?.items.filter(item=>item.exhaustion.state==='exhausted')??[];
  const notReporting=page?.items.filter(item=>item.reasons.some(reason=>reason.code==='stale_observations'))??[];
  const withoutData=page?.items.filter(item=>!item.reasons.some(reason=>reason.code==='stale_observations')&&item.reasons.some(reason=>['no_data','expired_history','evaluation_unavailable'].includes(reason.code)))??[];
  const scope=page?.next_cursor||hasPrevious?' on this page':'';
  return <section className="slo-operational-overview" aria-label="Operational SLO overview">
    <PageHeader title="SLOs" actions={canWrite?<Button className="app-slo-new" onClick={onCreate}>New SLO</Button>:undefined}/>
    <Toolbar className="app-slo-list-actions">
      <form className="app-slo-search" role="search" onSubmit={event=>{event.preventDefault();apply({q:draftQuery.trim()});}}><SearchField id="slo-search" label="Search SLOs" placeholder="Search by name" maxLength={200} value={draftQuery} onChange={event=>setDraftQuery(event.target.value)} shortcut/></form>
      <FilterSelect id="slo-service-filter" label="Service" value={serviceID} onChange={value=>apply({service_id:value})} options={[{value:'',label:'Any'},...services.map(service=>({value:service.id,label:service.display_name}))]}/>
      <ToolbarSpacer/><RefreshButton label="Refresh SLOs" busy={busy} lastRefreshed={updatedAt} onClick={refresh}/>
    </Toolbar>
    {servicesError&&<p role="alert">{servicesError}</p>}
    {busy&&<span className="sr-only" role="status">Refreshing operational SLOs</span>}
    {error&&<div className="slo-error" role="alert"><p>{error}</p>{page&&<p>Previously loaded values are retained and may be stale.{lastConfirmedAt&&` Last confirmed evaluation: ${lastConfirmedAt}.`}</p>}</div>}
    {snapshotExpired&&<Button variant="outline" onClick={restartSnapshot}>Restart from first page</Button>}
    {page&&totalCount>0&&<div className="app-slo-triage" role="group" aria-label="Filter by urgency">
      {groups.map(([group,label,explanation])=><button key={group} type="button" className={`app-slo-tile app-slo-tile--${group}`} aria-pressed={urgency===group} onClick={()=>changeUrgency(urgency===group?'':group)}><span className="app-slo-tile__count">{page.group_counts[group]}</span><span className="app-slo-tile__label"><StatusMark tone={group==='needs_attention'?'breached':group==='watch'?'warning':'healthy'} label={label}/></span><span className="app-slo-tile__why">{explanation}</span></button>)}
      <div className="app-slo-tile app-slo-tile--info">
        <span className="app-slo-tile__key">Soonest exhaustion{scope}</span>
        <span className="app-slo-tile__value">{exhausted.length?<><b>exhausted</b> · {exhausted.length===1?exhausted[0]!.slo.display_name:`${exhausted.length} SLOs`}</>:soonest?<><b>{shortExhaustion(soonest.exhaustion)}</b> · {soonest.slo.display_name}{soonest.burn_1h.performance.burn!==null&&` · burning ${multiple(soonest.burn_1h.performance.burn)}`}</>:'none estimated'}</span>
        <span className="app-slo-tile__key">Not reporting{scope}</span>
        <span className="app-slo-tile__value">{notReporting.length?<>{notReporting.length===1?notReporting[0]!.slo.display_name:`${notReporting.length} SLOs`} · last sample {lastEvaluationFormat.format(new Date(notReporting[0]!.current.quality.last_observation_at??notReporting[0]!.current.evaluation_at))} UTC</>:withoutData.length?<>{withoutData.length===1?withoutData[0]!.slo.display_name:`${withoutData.length} SLOs`} · no evaluable data</>:'no stale observations'}</span>
      </div>
    </div>}
    {page&&<div className="app-slo-groups">{groups.map(([group,label,explanation])=>{
      const items=page.items.filter(item=>item.urgency===group),count=page.group_counts[group];
      if(!items.length||urgency&&group!==urgency)return null;
      return <section className="app-slo-group" key={group} aria-labelledby={`slo-group-${group}`}>
        <header><h2 id={`slo-group-${group}`} aria-label={`${label}, ${count} SLOs`}>{label}</h2><span>{count}</span><small>{explanation}</small></header>
        <div role="table" className="app-slo-table" aria-label={`${label} SLOs`}><div role="row" className="app-slo-columns"><span role="columnheader">SLO</span><span role="columnheader">Status</span><span role="columnheader">Objective</span><span role="columnheader">Budget · 7 d</span><span role="columnheader">Budget left</span><span role="columnheader">Burn rate</span><span role="columnheader" className="app-slo-exhaustion-head">Budget runs out</span><span role="columnheader"><span className="sr-only">Open</span></span></div><div className="app-slo-rows" role="rowgroup">{items.map(item=><OverviewRow item={item} key={item.slo.id} onChartReady={chartReady} onOpen={onLocationChange}/>)}</div></div>
      </section>;
    })}</div>}
    {page&&!page.items.length&&<div className="slo-empty">{!query&&!serviceID&&!urgency&&matchingCount===0?<><h2>No SLOs yet</h2><p>Create an SLO from an existing SLI to start tracking its target.</p></>:<p>{matchingCount===0?'No SLOs match these filters.':'No SLOs are on this page.'}</p>}</div>}
    {page&&(matchingCount>0||hasPrevious||page.next_cursor)&&<Pager id="slo-page-size" label="SLO result pages" position={`Showing ${page.items.length} of ${matchingCount}`} pageSize={pageSize} onPageSizeChange={size=>apply({page_size:String(size)})} hasPrevious={hasPrevious} hasNext={Boolean(page.next_cursor)} busy={busy} onPrevious={previousPage} onNext={nextPage}/>}
    {!page&&!error&&<span className="sr-only" role="status">Loading operational SLOs</span>}
  </section>;
}

export function OperationalDetailView({id,definition:providedDefinition,locationKey,onLocationChange,refreshVersion=0,onSessionFailure=()=>{},onEdit}:{id:string;definition?:Slo;serverClock?:ServerClock;locationKey:string;onLocationChange:(path:string)=>void;refreshVersion?:number;onSessionFailure?:()=>void;onEdit?:()=>void}) {
  const [detailValue,setDetail]=useState<OperationalDetail|null>(null),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const detail=detailValue?.objective.id===id?detailValue:null;
  const [currentStatusValue,setCurrentStatus]=useState<CurrentStatus|null>(null),[statusBusy,setStatusBusy]=useState(false),[statusError,setStatusError]=useState('');
  const currentStatus=currentStatusValue?.slo.id===id?currentStatusValue:null;
  const definition=currentStatus?.slo??providedDefinition??detail?.objective;
  const authorityFailed=useRef(false);
  useEffect(()=>{authorityFailed.current=false;},[id]);
  const [refreshCount,setRefreshCount]=useState(0);
  const params=queryFrom(locationKey);
  const windowDays=definition?Math.max(1,Math.min(365,Math.round(definition.rolling_window_seconds/86400))):null;
  const rangeOptions=Array.from(new Set(['7d',...(windowDays===null?[]:[`${windowDays}d`]),'90d','1y']));
  const selectedRange=params.get('range');
  const period=selectedRange==='7d'||selectedRange==='90d'||selectedRange==='1y'?selectedRange:'window';
  const range=period==='window'?`${windowDays}d`:period;
  const zone=(params.get('tz')==='local'?'local':'UTC') as 'UTC'|'local';
  const burnWindow=(params.get('burn_window')==='21600'?21600:3600) as 3600|21600;
  const renderedKey=useRef('');
  const requestedKey=JSON.stringify([id,period,burnWindow,refreshVersion,refreshCount]);
  useEffect(()=>{if(!busy&&!statusBusy&&renderedKey.current===requestedKey&&((detail&&currentStatus)||error||statusError))journeyContentReady('slo.detail',!!error||!!statusError);},[detail,currentStatus,busy,statusBusy,error,statusError,requestedKey,zone]);
  const generation=useRef(0);
  const sessionFailure=useRef(onSessionFailure);sessionFailure.current=onSessionFailure;
  useEffect(()=>{
    const abort=new AbortController();let running=false;
    const read=async()=>{
      if(running)return;running=true;setStatusBusy(true);
      try{const value=await sloClient.current(id,abort.signal);if(!abort.signal.aborted&&!authorityFailed.current){setCurrentStatus(value);setStatusError('');}}
      catch(failure){if(!abort.signal.aborted){setStatusError('Current status could not refresh.');if(failure instanceof SloFailure&&(failure.status===401||failure.status===403)){authorityFailed.current=true;setCurrentStatus(null);setDetail(null);sessionFailure.current();}}}
      finally{running=false;if(!abort.signal.aborted)setStatusBusy(false);}
    };
    void read();const timer=setInterval(()=>void read(),5000);
    return()=>{clearInterval(timer);abort.abort();};
  },[id,refreshCount,refreshVersion]);
  function changeView(values:Record<string,string>) {
    const next=queryFrom(locationKey);
    for(const [key,value] of Object.entries(values)){if(value)next.set(key,value);else next.delete(key);}
    const search=next.toString();onLocationChange(`/slos/${id}${search?`?${search}`:''}`);
  }
  useEffect(()=>{
    const abort=new AbortController(),requestID=++generation.current;setBusy(true);
    setError('');
    const read=async()=>{
      return sloClient.operational(id,{period,burn_window_seconds:burnWindow},abort.signal);
    };
    void read().then(value=>{
      if(!abort.signal.aborted&&!authorityFailed.current&&requestID===generation.current){renderedKey.current=requestedKey;setDetail(value);setError('');}
    }).catch(failure=>{
      if(!abort.signal.aborted&&requestID===generation.current){
        renderedKey.current=requestedKey;
        const problem=failure instanceof Error?failure.message:'The operational detail could not be loaded.';
        if(failure instanceof SloFailure&&(failure.status===401||failure.status===403)){authorityFailed.current=true;setDetail(null);setCurrentStatus(null);setError(problem);sessionFailure.current();}
        else setError(problem);
      }
    }).finally(()=>{if(!abort.signal.aborted&&requestID===generation.current)setBusy(false);});
    return()=>abort.abort();
  },[id,period,burnWindow,refreshVersion,refreshCount]);

  const activeBurnWindow=detail?.burn_series.burn_window_seconds??3600;
  const activeBurnLabel=activeBurnWindow===3600?'1 h':'6 h';
  const refresh=()=>{beginJourney(locationKey,false,true);clearQueryCache();setRefreshCount(value=>value+1);};
  if(!definition)return <section className="app-slo-detail" aria-label="Operational SLO detail"><a className="app-detail-back" href="/slos" data-app-route>← All SLOs</a>{error||statusError?<p role="alert">{error||statusError}</p>:<span className="sr-only" role="status">Opening SLO details</span>}</section>;
  const source=currentStatus??detail;
  const heading=<><nav className="app-breadcrumbs" aria-label="Breadcrumb"><a href="/slos" data-app-route>SLOs</a><span aria-hidden="true">/</span><span aria-current="page">{definition.display_name}</span></nav><header className="app-detail-heading"><div><h1 className="app-page-heading" aria-label={definition.display_name} tabIndex={-1}>{definition.display_name}</h1><p className="slo-detail-intro">{definition.sli_display_name} · {percentExact(definition.target)} over a rolling {durationText(definition.rolling_window_seconds)}</p></div><div className="app-detail-actions"><RefreshButton label="Refresh objective history" busy={busy||statusBusy} onClick={refresh}/><Button variant="outline" onClick={()=>{const el=document.getElementById('definition') as HTMLDetailsElement|null;if(el){el.open=true;el.scrollIntoView?.({block:'start'});el.querySelector('summary')?.focus({preventScroll:true});}}}>View definition ↓</Button>{onEdit&&<Button onClick={onEdit}>Edit SLO</Button>}</div></header></>;
  const controls=<div className="slo-history-controls"><div role="group" aria-label="Objective history period">{rangeOptions.map(value=><Button key={value} variant={range===value?'selected':'outline'} aria-pressed={range===value} onClick={()=>changeView({range:value})}>{value==='1y'?'1 y':value.replace('d',' d')}</Button>)}</div><div role="group" aria-label="Timezone">{(['UTC','local'] as const).map(value=><Button key={value} variant={zone===value?'selected':'outline'} aria-pressed={zone===value} onClick={()=>changeView({tz:value})}>{value==='UTC'?'UTC':'Local'}</Button>)}</div></div>;
  const warning=source?qualityWarning(source.current.quality,source.current.performance,source.current.window):'';
  const chartGeneration=detail?.generation?.generation_id;
  const plot=(kind:'budget'|'burn'|'attainment',compact=false)=>detail&&<SloHistoryChart kind={kind} samples={kind==='budget'?detail.budget_series.samples:kind==='burn'?detail.burn_series.samples:detail.attainment_series.samples} start={detail.range.effective_start} end={detail.range.effective_end} target={definition.target} compact={compact} zone={zone} generation={chartGeneration} burnLabel={activeBurnLabel}/>;
  return <section className="app-slo-detail slo-detail-b" aria-label="Operational SLO detail">{heading}<div className="slo-detail-stack">
    {source&&<SloVerdict value={source} warning={warning}/>}
    {statusError&&<p role="status" className="slo-detail-notice">Current status could not refresh. Showing the last confirmed evaluation{source?` at ${source.current.evaluation_at}`:''}.</p>}
    {error&&<div role="alert" className="slo-error"><p>{error}</p><p>{detail?`Showing the last confirmed evaluation at ${detail.effective_at}; values may be stale.`:'Refresh or choose a shorter period to try again.'}</p></div>}
    {busy&&<span role="status" className="sr-only">Refreshing operational evaluation</span>}
    <section className="slo-detail-panel" id="budget" aria-label="Your error budget"><div className="slo-plot-head"><h2>Your error budget</h2>{controls}</div>{source&&<SloSummary value={source} definition={definition}/>}{plot('budget')}
    <div className="slo-plot-foot"><span>{source?.current.quality.state.replaceAll('_',' ')} · {source?.current.quality.freshness}</span>{detail&&<span>{durationText(detail.range.resolution_seconds)} resolution · {detail.range.reduced?'reduced history':'full resolution'}</span>}</div></section>
    {detail?.history_preparation&&<div className="slo-detail-notice" role="status"><p>{detail.history_preparation.ready_seconds===0?'No history is ready for this period yet.':`${Math.floor(100*detail.history_preparation.ready_seconds/detail.history_preparation.total_seconds)}% of this period’s history is ready.`} Available results are shown; unfinished intervals are left blank.</p><p>{detail.history_preparation.failed?'Some historical calculations failed.':'Historical processing is incomplete.'} Use Refresh to check progress.</p></div>}
    {currentStatus?.generation&&detail?.generation&&!sameDerivedInputs(currentStatus.generation,detail.generation)&&<p className="slo-detail-notice" role="status">These graphs and the status panel show different evaluations. Refresh the timeline to align them.</p>}
    {activeBurnWindow!==burnWindow&&<p className="slo-detail-notice" role="status">{busy?`Showing ${activeBurnLabel} burn while the selected window loads.`:`Showing the last confirmed ${activeBurnLabel} burn; the selected result is unavailable.`}</p>}
    {detail?<><section className="slo-detail-panel" id="burn"><div className="slo-plot-head"><h2>Recent burn</h2><div className="slo-burn-controls" role="group" aria-label="Burn window">{([3600,21600] as const).map(value=><Button key={value} variant={burnWindow===value?'selected':'outline'} aria-pressed={burnWindow===value} onClick={()=>changeView({burn_window:String(value)})}>{value===3600?'1 h':'6 h'}</Button>)}</div></div>{plot('burn')}<div className="slo-plot-foot"><span>{activeBurnLabel} window · 1× reference</span></div></section>
    <section className="slo-detail-panel" id="attainment"><div className="slo-plot-head"><h2>Attainment</h2><small>Good ÷ total per plotted interval</small></div>{plot('attainment',true)}<div className="slo-plot-foot"><span>{percentExact(definition.target)} target</span></div></section><SloCompletedDays detail={detail}/></>:<p role="status">{busy?'Loading objective history…':'Measurements unavailable'}</p>}
    {source&&<SloDefinitionPanels value={source} definition={definition} context={detail?.context} warning={warning}/>}
  </div></section>;
}
