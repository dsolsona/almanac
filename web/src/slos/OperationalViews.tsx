import { Component, Suspense, lazy, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import type { components } from '../api/generated.ts';
import { Button, SelectField } from '../components/ui/index.ts';
import { SloExactValuesTable, type SloExactRow } from '../components/charts/SloExactValuesTable.tsx';
import { SloBudgetSparkline } from '../components/charts/SloBudgetSparkline.tsx';
import type { Coverage, TimelineChart } from '../components/charts/timelineModel.ts';
import { sloClient, SloFailure, type OperationalDetail, type OperationalOverview } from './client.ts';
import './slos.css';
import './slo-reference.css';

class OperationalChartBoundary extends Component<{children:ReactNode},{failed:boolean}> {
  state={failed:false};
  static getDerivedStateFromError(){return {failed:true};}
  render(){return this.state.failed?<p role="alert">The timeline could not be drawn. Its exact values remain available.</p>:this.props.children;}
}

// The same RefreshCw outline used by slo-tracker, without another icon dependency.
function RefreshIcon(){return <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/><path d="M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16"/><path d="M8 16H3v5"/></svg>;}

type OverviewItem=OperationalOverview['items'][number];
type Slo=components['schemas']['Slo'];
type Urgency=OverviewItem['urgency'];
const groups:[Urgency,string,string][]=[
  ['needs_attention','Needs attention','breached, burning fast, low on budget, or not reporting'],
  ['watch','Watch','under half the budget left, burning above 1×, or a partial window'],
  ['healthy','Healthy','on track'],
];
const reasonText:Record<string,string>={
  on_track:'On track',stale_observations:'Observations are stale',no_data:'No evaluable data',expired_history:'History has expired',
  complete_window_violation:'Complete window is below target',negative_observed_budget:'Observed budget is negative',low_observed_budget:'Less than 20% of budget remains',
  high_1h_burn:'1 h burn is at least 1×',evaluation_unavailable:'Current evaluation unavailable',not_yet_eligible:'Not yet eligible',zero_traffic:'No traffic',
  incomplete_coverage:'Coverage is incomplete',short_burn_unavailable:'Short-window burn unavailable',budget_below_half:'Less than half of budget remains',
  incomplete_or_stale_inputs:'Input coverage is incomplete or stale',cadence_mismatch:'Measurement cadence does not match the SLI',
};
function reasonLabel(code:string):string {return reasonText[code]??code.replaceAll('_',' ').replace(/^./,letter=>letter.toUpperCase());}
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
// Display formatting copied from slo-tracker; tables retain the decimal strings.
function budgetPercent(value:string|null,maximumFractionDigits=3):string { return value===null?'—':`${(Number(value)*100).toLocaleString(undefined,{maximumFractionDigits})}%`; }
function multiple(value:string|null):string {return value===null?'—':`${Number(value).toLocaleString(undefined,{maximumFractionDigits:2})}×`;}
function statusFor(item:OverviewItem['current']):'healthy'|'warning'|'breached'|'no-data'|'stale' {
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
  if(quality.state==='not_applicable')return window&&Date.parse(window.end)<=Date.parse(window.evaluation_valid_from)?'Waiting for the first SLO evaluation.':'No measurements apply to this period.';
  if(quality.state==='no_data')return 'No measurements are available yet.';
  if(quality.state==='expired_history')return 'Required history has expired.';
  if(quality.state==='not_yet_eligible')return 'Waiting for the first completed collection interval.';
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
function coveragePercent(value:string|null):string {return value===null?'Unknown':budgetPercent(value);}
function meterFraction(value:string|null):number {
  if(value===null)return 0;
  const numeric=Number(value);
  return Number.isFinite(numeric)?Math.max(0,Math.min(1,numeric)):0;
}
function formatExhaustion(item:OverviewItem['exhaustion']):string {
  if(item.state==='exhausted')return 'Exhausted';
  if(item.state==='unavailable')return 'Unavailable';
  return item.hours_until_exhaustion===null?'No finite estimate':`${decimal(item.hours_until_exhaustion)} h at the observed 1 h bad-event rate`;
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

function ReferenceStatus({status,title}:{status:ReturnType<typeof statusFor>;title?:string}) {
  const labels={healthy:'Healthy',warning:'Warning',breached:'Breached','no-data':'No data',stale:'Stale'};
  return <span className="slo-reference-status" data-status={status} title={title}><svg viewBox="0 0 16 16" width="15" height="15" aria-hidden="true">{status==='warning'?<path d="M8 1L15 14H1Z" fill="currentColor"/>:status==='breached'?<rect x="2" y="2" width="12" height="12" fill="currentColor"/>:<circle cx="8" cy="8" r="5" fill="none" stroke="currentColor" strokeWidth="2"/>}</svg>{labels[status]}</span>;
}
function windowDuration(seconds:number):string {
  return seconds%86400===0?`${seconds/86400} d`:seconds%3600===0?`${seconds/3600} h`:`${seconds} s`;
}
function shortExhaustion(item:OverviewItem['exhaustion']):string {
  if(item.state==='exhausted')return 'exhausted';
  if(item.state!=='estimated'||item.hours_until_exhaustion===null)return '—';
  const hours=Number(item.hours_until_exhaustion);
  return hours<1?`${Math.ceil(hours*60)} min`:hours>=48?`${Math.ceil(hours/24)} d`:`${Math.ceil(hours)} h`;
}
function OverviewRow({item}:{item:OverviewItem}) {
  const slo=item.slo,p=item.current.performance,q=item.current.quality,status=statusFor(item.current);
  const reason=item.reasons.map(reason=>reasonLabel(reason.code)+(reason.provisional?' · observed only':'')).join('; ');
  const partial=p.provisional||q.state==='partial';
  const budget=p.remaining_fraction===null?null:Number(p.remaining_fraction),burn=item.burn_1h.performance.burn;
  const caption=status==='stale'?'not reporting':partial?'partial window':status==='no-data'?'no samples':'at 1 h rate';
  return <article className="app-slo-row" role="row">
    <div className="app-slo-status" role="cell"><ReferenceStatus status={status} title={reason}/>{item.reasons.length>1?<details><summary>Reasons ({item.reasons.length})</summary><ul>{item.reasons.map(value=><li key={value.code}>{reasonLabel(value.code)}{value.provisional?' · observed only':''}</li>)}</ul></details>:<span className="sr-only">{reason}</span>}</div>
    <div className="app-slo-name" role="cell"><a data-app-route className="app-slo-hitarea" href={`/slos/${slo.id}`} title={slo.display_name}>{slo.display_name}</a><small><a data-app-route href={`/services/${slo.service_id}`}>{slo.service_display_name}</a><span>·</span><a data-app-route className="app-slo-indicator" href={`/slis/${slo.sli_id}`}>{slo.sli_display_name}</a></small><span className="sr-only">Target {shift(slo.target,2)}% over {windowDuration(slo.rolling_window_seconds)}</span></div>
    <div className="app-slo-spark" role="cell"><span className="app-slo-window" title="Budget history shown for the last 7 days">{windowDuration(slo.rolling_window_seconds)}</span><SloBudgetSparkline samples={item.budget_sparkline} label={`${slo.display_name} rolling remaining budget`}/></div>
    <div className="app-slo-budget" role="cell" title={partial?'Observed only; collection coverage is incomplete':undefined}><b className={budget===null?'muted':budget<0?'crit':budget<.2?'warn':''} title={p.remaining_fraction===null?undefined:`${shift(p.remaining_fraction,2)}%`}>{budgetPercent(p.remaining_fraction,1)}</b><span className="slo-reference-meter" role="progressbar" aria-label={`${slo.display_name} budget remaining`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={budget===null?undefined:meterFraction(p.remaining_fraction)*100} aria-valuetext={budget===null?'Unavailable':budgetPercent(p.remaining_fraction)}><span style={{width:`${meterFraction(p.remaining_fraction)*100}%`,backgroundColor:`var(--status-${status})`}}/></span><span className="sr-only">{partial?'Observed only; ':''}{p.remaining_budget===null?'Budget unavailable':`${decimal(p.remaining_budget)} bad events left`}</span></div>
    <div className="app-slo-burn" role="cell" title={qualityWarning(item.burn_1h.quality)}><b className={burn!==null&&Number(burn)>=10?'hot':burn!==null&&Number(burn)>=2?'warm':''}>{multiple(burn)}</b><small>{item.burn_6h.performance.burn===null?'no samples':`6 h ${multiple(item.burn_6h.performance.burn)}`}</small></div>
    <div className="app-slo-exhaustion" role="cell" title={formatExhaustion(item.exhaustion)}><b className={item.exhaustion.state==='exhausted'?'hot':''}>{shortExhaustion(item.exhaustion)}</b><small>{caption}</small></div><span className="app-slo-chevron" role="cell" aria-hidden="true">›</span>
  </article>;
}

export function OperationalOverviewView({locationKey,canWrite,onCreate,onLocationChange}:{locationKey:string;canWrite:boolean;onCreate:()=>void;onLocationChange:(path:string,state?:unknown)=>void}) {
  const params=queryFrom(locationKey),query=params.get('q')??'',serviceID=params.get('service_id')??'',urgency=(params.get('urgency')??'') as ''|Urgency;
  const cursor=params.get('cursor')??'';
  const [draftQuery,setDraftQuery]=useState(query),[page,setPage]=useState<OperationalOverview|null>(null),[services,setServices]=useState<Awaited<ReturnType<typeof sloClient.services>>['items']>([]);
  const [error,setError]=useState(''),[busy,setBusy]=useState(false),[refreshCount,setRefreshCount]=useState(0);
  const lastKey=useRef('');
  const searchInput=useRef<HTMLInputElement>(null);
  useEffect(()=>{const focusSearch=(event:KeyboardEvent)=>{if(event.key==='/'&&!(event.target instanceof HTMLElement&&event.target.closest('input,textarea,select,[contenteditable="true"]'))){event.preventDefault();searchInput.current?.focus();}};window.addEventListener('keydown',focusSearch);return()=>window.removeEventListener('keydown',focusSearch);},[]);
  const filtersKey=JSON.stringify([query,serviceID,urgency,cursor]);
  useEffect(()=>setDraftQuery(query),[query]);
  useEffect(()=>{
    const abort=new AbortController();
    const sameView=lastKey.current===filtersKey;
    if(!sameView){setPage(null);setError('');}
    lastKey.current=filtersKey;setBusy(true);
    const read=async()=>{
      try {
        const [overview,serviceList]=await Promise.all([
          sloClient.overview({q:query,service_id:serviceID||undefined,urgency:urgency||undefined,limit:50,cursor:cursor||undefined},abort.signal),
          services.length?Promise.resolve(services):allServices(abort.signal),
        ]);
        if(!abort.signal.aborted){setPage(overview);setServices(serviceList);setError('');}
      } catch(failure){if(!abort.signal.aborted){if(failure instanceof SloFailure&&(failure.status===401||failure.status===403))setPage(null);setError(failure instanceof Error?failure.message:'The operational overview could not be loaded.');}}
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
  const snapshotExpired=error.includes('snapshot expired');
  const hasPrevious=Boolean(page?.previous_cursor);
  const lastConfirmedAt=page?.items.map(item=>item.current.evaluation_at).sort().at(-1);
  const matchingCount=page?(urgency?page.group_counts[urgency]:Object.values(page.group_counts).reduce((sum,count)=>sum+count,0)):0;
  return <section className="slo-operational-overview" aria-label="Operational SLO overview">
    <header className="app-slo-list-head"><h1 className="app-page-heading" tabIndex={-1}>SLOs</h1><span className="app-slo-list-sub">{page?`${matchingCount} SLO${matchingCount===1?'':'s'} · grouped by urgency · UTC`:'Loading SLOs…'}</span><div className="app-slo-list-actions">
      <form className="app-slo-search" role="search" onSubmit={event=>{event.preventDefault();apply({q:draftQuery.trim()});}}><svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true"><circle cx="8" cy="8" r="5.5" fill="none" stroke="currentColor" strokeWidth="1.5"/><path d="m12 12 5 5" stroke="currentColor" strokeWidth="1.5"/></svg><input ref={searchInput} aria-label="Search SLOs" placeholder="Search by name" maxLength={200} value={draftQuery} onChange={event=>setDraftQuery(event.target.value)}/><kbd>/</kbd></form>
      <div className="app-slo-group-select" role="group" aria-label="Group by"><Button variant="selected" aria-pressed={true}>Urgency</Button></div>
      <details className="app-slo-filters"><summary>Filters</summary><div><SelectField id="slo-service-filter" label="Service" value={serviceID} onChange={event=>apply({service_id:event.target.value})} options={[{value:'',label:'All services'},...services.map(service=>({value:service.id,label:service.display_name}))]}/><SelectField id="slo-urgency-filter" label="Urgency" value={urgency} onChange={event=>changeUrgency(event.target.value)} options={[{value:'',label:'All urgency groups'},...groups.map(([value,label])=>({value,label}))]}/></div></details>
      {canWrite&&<Button className="app-slo-new" onClick={onCreate}>New SLO</Button>}
      <button className="app-slo-refresh" aria-label="Refresh" title="Refresh SLOs" disabled={busy} onClick={()=>{if(cursor){onLocationChange(updateQuery(locationKey,{},['cursor','previous_cursor']));return;}setRefreshCount(value=>value+1);}}><RefreshIcon/></button>
    </div></header>
    {busy&&<span className="sr-only" role="status">Refreshing operational SLOs</span>}
    {error&&<div className="slo-error" role="alert"><p>{error}</p>{page&&<p>Previously loaded values are retained and may be stale.{lastConfirmedAt&&` Last confirmed evaluation: ${lastConfirmedAt}.`}</p>}</div>}
    {snapshotExpired&&<Button variant="outline" onClick={restartSnapshot}>Restart from first page</Button>}
    {page&&<div className="app-slo-groups">{groups.map(([group,label,explanation])=>{
      const items=page.items.filter(item=>item.urgency===group),count=page.group_counts[group];
      if(!items.length||urgency&&group!==urgency)return null;
      return <section className="app-slo-group" key={group} aria-labelledby={`slo-group-${group}`}>
        <header><h2 id={`slo-group-${group}`} aria-label={`${label}, ${count} SLOs`}>{label}</h2><span>{count}</span><small>{explanation}</small></header>
        <div role="table" aria-label={`${label} SLOs`}><div role="row" className="app-slo-columns"><span role="columnheader">Status</span><span role="columnheader">SLO</span><span role="columnheader">Budget · window</span><span role="columnheader">Remaining</span><span role="columnheader">Burn 1 h</span><span role="columnheader" className="app-slo-exhaustion-head">Exhaustion</span></div><div className="app-slo-rows" role="rowgroup">{items.map(item=><OverviewRow item={item} key={item.slo.id}/>)}</div></div>
      </section>;
    })}</div>}
    {page&&!page.items.length&&<div className="slo-empty">{!query&&!serviceID&&!urgency&&matchingCount===0?<><h2>No SLOs yet</h2><p>Create an SLO from an existing SLI to start tracking its target.</p></>:<p>{matchingCount===0?'No SLOs match these filters.':'No SLOs are on this page.'}</p>}</div>}
    {page&&(hasPrevious||page.next_cursor)&&<nav className="slo-page-controls" aria-label="SLO result pages"><Button variant="outline" disabled={!hasPrevious||busy} onClick={previousPage}>Previous page</Button><Button variant="outline" disabled={!page.next_cursor||busy} onClick={nextPage}>Next page</Button></nav>}
    {!page&&!error&&<span className="sr-only" role="status">Loading operational SLOs</span>}
  </section>;
}

function chartCoverage(state:components['schemas']['SloOperationalSample']['state']):Coverage {
  return ({observed:'complete',partial:'partial',stale:'stale',missing:'missing',zero_traffic:'zero-traffic',known_empty:'known-empty',expired:'expired',not_applicable:'not-applicable'} as const)[state];
}
function dailyChartCoverage(day:components['schemas']['SloDailyComplianceDay']):Coverage {
  if(day.status==='met'||day.status==='violated')return 'complete';
  if(day.status==='not_applicable')return 'not-applicable';
  if(day.quality.zero_traffic_intervals>0)return 'zero-traffic';
  if(day.quality.known_empty_intervals>0)return 'known-empty';
  if(day.quality.expired_intervals>0||day.quality.state==='expired_history')return 'expired';
  if(day.quality.state==='partial')return 'partial';
  return 'missing';
}
function dailyStatusLabel(status:components['schemas']['SloDailyComplianceDay']['status']):string {
  return ({met:'Met',violated:'Violated',unknown:'Unknown',not_applicable:'Not applicable'} as const)[status];
}
function seriesPoints(samples:components['schemas']['SloOperationalSample'][],field:'attainment'|'remaining_fraction'|'burn',asPercent:boolean) {
  return samples.map(sample=>({timestamp:sample.evaluation_at,value:sample[field]===null?null:asPercent?shift(sample[field]!,2):sample[field],coverage:chartCoverage(sample.state),good:sample.good,total:sample.total,samples:sample.quality.observed_intervals}));
}
function exactRows(samples:components['schemas']['SloOperationalSample'][],lane:string,field:'attainment'|'remaining_fraction'|'burn',unit:string):SloExactRow[] {
  return samples.map(sample=>({timestamp:sample.evaluation_at,lane,series:'Backend evaluation',unit,value:sample[field],coverage:chartCoverage(sample.state),good:sample.good,total:sample.total,samples:sample.quality.observed_intervals}));
}
function LaneTable({title,titleId,rows,page,onPageChange,onClose,timeZone}:{title:string;titleId:string;rows:SloExactRow[];page:number;onPageChange:(value:number)=>void;onClose:()=>void;timeZone:'UTC'|'local'}) {
  return <section className="slo-operational-exact"><header><h3 id={titleId} aria-label={`${title} exact values`}>{title} · exact values</h3><Button variant="outline" aria-label="Close table" onClick={onClose}>Close</Button></header>{rows.length===0?<p role="status">No samples are available for this range.</p>:<SloExactValuesTable rows={rows.slice((page-1)*50,page*50)} title={title} page={page} totalRows={rows.length} onPageChange={onPageChange} timeZone={timeZone}/>}</section>;
}
function burnValue(value:components['schemas']['SloBurnValue']) {return multiple(value.performance.burn);}
export function OperationalDetailView({id,definition,locationKey,onLocationChange,refreshVersion=0,onSessionFailure=()=>{}}:{id:string;definition:Slo;locationKey:string;onLocationChange:(path:string)=>void;refreshVersion?:number;onSessionFailure?:()=>void}) {
  const [detail,setDetail]=useState<OperationalDetail|null>(null),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const [refreshCount,setRefreshCount]=useState(0);
  const params=queryFrom(locationKey);
  const windowDays=Math.max(1,Math.min(365,Math.round(definition.rolling_window_seconds/86400)));
  const rangeOptions=Array.from(new Set(['7d',`${windowDays}d`,'90d','1y']));
  const range=rangeOptions.includes(params.get('range')??'')?params.get('range')!:`${windowDays}d`;
  const zone=(params.get('tz')==='local'?'local':'UTC') as 'UTC'|'local';
  const burnWindow=(params.get('burn_window')==='21600'?21600:3600) as 3600|21600;
  const [tablePages,setTablePages]=useState([1,1,1,1]);
  const [tableOpen,setTableOpen]=useState([false,false,false,false]);
  const tableRegion=useRef<HTMLDivElement>(null);
  const generation=useRef(0);
  const sessionFailure=useRef(onSessionFailure);sessionFailure.current=onSessionFailure;
  function changeView(values:Record<string,string>) {
    const next=queryFrom(locationKey);
    for(const [key,value] of Object.entries(values)){if(value)next.set(key,value);else next.delete(key);}
    const search=next.toString();onLocationChange(`/slos/${id}${search?`?${search}`:''}`);
  }
  useEffect(()=>{setTablePages([1,1,1,1]);setTableOpen([false,false,false,false]);},[range,burnWindow]);
  useEffect(()=>{const index=tableOpen.findIndex(Boolean);if(index>=0)requestAnimationFrame(()=>tableRegion.current?.querySelector<HTMLElement>(`[data-lane-table="${index}"]`)?.scrollIntoView?.({block:'nearest'}));},[tableOpen]);
  function toggleTable(index:number){setTableOpen(values=>values.map((value,laneIndex)=>laneIndex===index?!value:false));}
  function closeTable(index:number){setTableOpen([false,false,false,false]);requestAnimationFrame(()=>tableRegion.current?.querySelectorAll<HTMLButtonElement>('[data-table-toggle]')[index]?.focus());}
  useEffect(()=>{
    const abort=new AbortController(),requestID=++generation.current;setBusy(true);
    setError('');
    const read=async()=>{
      if(range==='7d')return sloClient.operational(id,{burn_window_seconds:burnWindow},abort.signal);
      const serverTime=await sloClient.serverTime(id,abort.signal);
      const cadence=definition.cadence_seconds*1000,rangeMs=(range==='1y'?365:Number.parseInt(range,10))*86_400_000;
      const end=Math.floor(serverTime/cadence)*cadence;
      const start=Math.ceil((end-rangeMs)/cadence)*cadence;
      return sloClient.operational(id,{start:new Date(start).toISOString(),end:new Date(end).toISOString(),burn_window_seconds:burnWindow},abort.signal);
    };
    void read().then(value=>{
      if(!abort.signal.aborted&&requestID===generation.current){setDetail(value);setError('');}
    }).catch(failure=>{
      if(!abort.signal.aborted&&requestID===generation.current){
        const problem=failure instanceof Error?failure.message:'The operational detail could not be loaded.';
        if(failure instanceof SloFailure&&(failure.status===401||failure.status===403)){setDetail(null);setError(problem);sessionFailure.current();}
        else setError(problem);
      }
    }).finally(()=>{if(!abort.signal.aborted&&requestID===generation.current)setBusy(false);});
    return()=>abort.abort();
  },[id,range,burnWindow,refreshVersion,refreshCount]);

  const activeBurnWindow=detail?.burn_series.burn_window_seconds??3600;
  const activeBurnLabel=activeBurnWindow===3600?'1 h':'6 h';
  const chart=useMemo<TimelineChart|null>(()=>{
    if(!detail)return null;
    const start=detail.range.effective_start,end=detail.range.effective_end;
    const attainment=detail.attainment_series.samples,budget=detail.budget_series.samples,burn=detail.burn_series.samples,days=detail.compliance_series.days;
    const selectedBurn=detail.burn_series.burn_window_seconds===21600?'6 h':'1 h';
    return {title:'Timeline',summary:`Attainment, remaining budget, ${selectedBurn} burn and daily compliance. Missing measurements are shown as gaps.`,start,end,showLaneNames:false,
      stepMs:Math.max(1,detail.range.resolution_seconds)*1000,fillExpectedGaps:false,timeZone:zone,
      referenceLines:[{lane:'attainment',value:shift(definition.target,2),label:`Target ${shift(definition.target,2)}%`},{lane:'budget',value:'0',label:'0%'},{lane:'burn',value:'1',label:'1×'},{lane:'compliance',value:shift(definition.target,2),label:`${shift(definition.target,2)}%`}],
      lanes:[
        {id:'attainment',label:'SLI',height:162,unit:'%',series:[{id:'attainment-series',label:'Observed',points:seriesPoints(attainment,'attainment',true)}]},
        {id:'budget',label:'Budget remaining',height:202,unit:'%',series:[{id:'budget-series',label:'Remaining',points:seriesPoints(budget,'remaining_fraction',true)}]},
        {id:'burn',label:`Burn rate ${selectedBurn}`,height:202,unit:'×',series:[{id:'burn-series',label:'Observed burn',points:seriesPoints(burn,'burn',false)}]},
        {id:'compliance',label:'Compliance',height:142,bars:true,decimals:Number(definition.target)>=.999?3:2,colors:days.map(day=>day.status==='met'?'healthy':'breached'),range:[Math.min(Number(shift(definition.target,2))-(100-Number(shift(definition.target,2)))*1.5,...days.flatMap(day=>day.performance.attainment===null?[]:[Number(shift(day.performance.attainment,2))])),100],unit:'%',series:[{id:'compliance-series',label:'Rolling attainment at UTC day end',points:days.map(day=>({timestamp:day.day_end,value:day.performance.attainment===null?null:shift(day.performance.attainment,2),coverage:day.status==='unknown'&&day.performance.attainment!==null?'partial':dailyChartCoverage(day)}))}]},
      ]};
  },[detail,definition.target,zone]);
  const plotChart=useMemo<TimelineChart|null>(()=>chart?{...chart,lanes:chart.lanes.map((lane,index)=>({...lane,extra:tableOpen[index]?340:0}))}:null,[chart,tableOpen]);
  const refresh=()=>setRefreshCount(value=>value+1);
  const heading=<><a className="app-detail-back" href="/slos" data-app-route>← All SLOs</a><header className="app-detail-heading"><div><h1 className="app-page-heading" aria-label={definition.display_name} tabIndex={-1}>{definition.display_name}{detail&&<ReferenceStatus status={statusFor(detail.current)} title={detail.reasons.map(reason=>reasonLabel(reason.code)).join('; ')}/>}</h1><div className="app-detail-meta"><a href={`/services/${definition.service_id}`} data-app-route>{definition.service_display_name}</a><span>·</span><span>{shift(definition.target,2)}% over {windowDuration(definition.rolling_window_seconds)} rolling</span><span>·</span><span>SLI <a href={`/slis/${definition.sli_id}`} data-app-route>{definition.sli_display_name}</a></span></div></div></header></>;
  const controls=<div className="app-timeline-controls"><div role="group" aria-label="Objective history period">{rangeOptions.map(value=><Button key={value} variant={range===value?'selected':'outline'} aria-pressed={range===value} onClick={()=>changeView({range:value})}>{value==='1y'?'1 y':value.replace('d',' d')}</Button>)}</div><div role="group" aria-label="Timezone">{(['UTC','local'] as const).map(value=><Button key={value} variant={zone===value?'selected':'outline'} aria-pressed={zone===value} onClick={()=>changeView({tz:value})}>{value==='UTC'?'UTC':'Local'}</Button>)}</div><Button variant="outline" aria-label="Refresh objective history" pending={busy} onClick={refresh}><RefreshIcon/></Button></div>;
  const toolbar=<div className="app-timeline-bar"><h2>Timeline</h2><span className="app-timeline-hint">one time axis for every panel</span>{controls}</div>;
  if(!detail)return <section className="app-slo-detail" aria-label="Operational SLO detail">{heading}<div className="app-detail-columns"><section className="app-timeline" aria-label="Objective history">{toolbar}{error?<div role="alert" className="slo-error"><p>{error}</p><p>Refresh or choose a shorter period to try again.</p></div>:<p className="app-timeline-loading" role="status">{busy?'Loading objective history…':'Measurements unavailable'}</p>}</section></div></section>;
  const current=detail.current,p=current.performance,q=current.quality;
  const activeBurn=activeBurnWindow===3600?detail.burn_1h:detail.burn_6h;
  const facts=[
    {id:'attainment',title:'SLI',value:budgetPercent(p.attainment),sub:'good ÷ total',height:162,rows:exactRows(detail.attainment_series.samples,'SLI attainment','attainment','ratio')},
    {id:'budget',title:'Budget remaining',value:budgetPercent(p.remaining_fraction),sub:p.remaining_budget===null?'error budget available':`${decimal(p.remaining_budget)} bad events left`,height:202,rows:exactRows(detail.budget_series.samples,'Rolling budget remaining','remaining_fraction','fraction')},
    {id:'burn',title:'Burn rate',value:burnValue(activeBurn),sub:`${activeBurnLabel} window · 1× is on plan`,height:202,rows:exactRows(detail.burn_series.samples,'Burn rate','burn','ratio')},
    {id:'compliance',title:'Compliance',value:`${detail.compliance_summary.met_days} / ${detail.compliance_summary.evaluable_days}`,sub:'days the window target was met',height:142,rows:detail.compliance_series.days.map(day=>({timestamp:day.day_end,day:day.utc_date,lane:'Daily compliance',series:`${dailyStatusLabel(day.status)}${day.performance.provisional?' · provisional':''}`,unit:'%',good:day.performance.good,total:day.performance.total,samples:day.quality.observed_intervals,effectiveAt:day.effective_at,window:day.performance.provisional?'Partial':'Full',value:day.performance.attainment===null?null:shift(day.performance.attainment,2),coverage:day.status==='unknown'&&day.performance.attainment!==null?'partial' as const:dailyChartCoverage(day),details:`${day.explanation.replaceAll('_',' ')} · requested ${day.requested_at} · effective ${day.effective_at} · target ${shift(day.target,2)}% · attainment ${day.performance.attainment===null?'unavailable':shift(day.performance.attainment,2)+'%'}`}))},
  ];
  return <section className="app-slo-detail" aria-label="Operational SLO detail">{heading}
    <div className="app-detail-columns"><div className="app-detail-main"><section className="app-timeline" aria-label="Objective history">{toolbar}
      {error&&<div role="alert" className="slo-error"><p>{error}</p><p>Showing the last confirmed evaluation at {detail.effective_at}; values may be stale.</p></div>}
      {busy&&<span role="status" className="sr-only">Refreshing operational evaluation</span>}
      {activeBurnWindow!==burnWindow&&<p className="app-coverage-note" role="status">{busy?`Showing ${activeBurnLabel} burn while the selected window loads.`:`Showing the last confirmed ${activeBurnLabel} burn; the selected result is unavailable.`}</p>}
      <div className="app-timeline-body" ref={tableRegion}><div className="app-timeline-labels">{facts.map((fact,index)=><div className="app-lane-label" key={fact.id} style={{height:fact.height+(tableOpen[index]?340:0)}}><h3>{fact.title}</h3><div className="app-lane-value">{fact.value}</div><p>{fact.sub}</p><div className="app-lane-tools">{index===2&&<div role="group" aria-label="Burn window">{([3600,21600] as const).map(value=><Button key={value} variant={burnWindow===value?'selected':'outline'} aria-pressed={burnWindow===value} onClick={()=>changeView({burn_window:String(value)})}>{value===3600?'1 h':'6 h'}</Button>)}</div>}<Button variant="outline" data-table-toggle aria-label={`View ${fact.title} as table`} aria-expanded={tableOpen[index]} aria-controls={`slo-exact-${id}-${fact.id}`} onClick={()=>toggleTable(index)}>▦ <span className={index===2?'sr-only':undefined}>Table</span></Button></div></div>)}</div>
        <div className="app-timeline-plot"><OperationalChartBoundary><Suspense fallback={<p role="status">Loading timeline charts…</p>}><TimelineChartViewLazy chart={plotChart!} showHeader={false}/></Suspense></OperationalChartBoundary></div>
        {facts.map((fact,index)=>{const target=`slo-exact-${id}-${fact.id}`;return <div className="app-lane-insert" id={target} key={fact.id} hidden={!tableOpen[index]} data-lane-table={index} role="region" aria-labelledby={`${target}-title`} style={{top:facts.slice(0,index+1).reduce((sum,lane)=>sum+lane.height,0),height:340}}>{tableOpen[index]&&<div className="app-lane-insert-panel"><LaneTable title={fact.title} titleId={`${target}-title`} rows={fact.rows} page={tablePages[index]!} timeZone={zone} onPageChange={value=>setTablePages(values=>values.map((page,i)=>i===index?value:page))} onClose={()=>closeTable(index)}/></div>}</div>;})}
      </div>
      {qualityWarning(q,p,current.window)&&<p className="app-coverage-note">{qualityWarning(q,p,current.window)}</p>}
      <p className="app-coverage-note">Completed UTC days · {detail.compliance_summary.unknown_days} unknown · {detail.compliance_summary.not_applicable_days} not applicable</p>
    </section></div>
    <aside className="app-detail-rail" aria-label="Current SLO status and definition">
      <section className="app-rail-card"><header><h2>Status</h2><Button variant="outline" aria-label="Refresh objective status" pending={busy} onClick={refresh}><RefreshIcon/></Button></header><div className="app-rail-card-body"><div className="app-budget-hero" style={{color:`var(--status-${statusFor(current)})`}}>{budgetPercent(p.remaining_fraction)}</div><p className="app-budget-caption">error budget remaining</p><div className="app-budget-meter"><span className="slo-reference-meter"><span style={{width:`${meterFraction(p.remaining_fraction)*100}%`,backgroundColor:`var(--status-${statusFor(current)})`}}/></span></div><dl className="app-status-facts">{[['Attainment',budgetPercent(p.attainment)],['Target',`${shift(definition.target,2)}%`],['Burn 1 h',burnValue(detail.burn_1h),detail.burn_1h.performance.burn===null?undefined:`${decimal(detail.burn_1h.performance.burn)}×`],['Burn 6 h',burnValue(detail.burn_6h),detail.burn_6h.performance.burn===null?undefined:`${decimal(detail.burn_6h.performance.burn)}×`],['Bad events left',p.remaining_budget===null?'—':decimal(p.remaining_budget)],['Exhaustion',shortExhaustion(detail.exhaustion)]].map(([label,value,exact])=><div key={label}><dt>{label}</dt><dd title={exact}>{value}</dd></div>)}</dl>{p.provisional&&<p className="app-coverage-note">Observed only · {coveragePercent(q.coverage)} collection coverage</p>}</div></section>
      <section className="app-rail-card"><details className="app-calculation-disclosure"><summary>View calculation details</summary><dl className="app-definition-facts"><div><dt>Source counts</dt><dd>{p.good??'—'} good / {p.total??'—'} total</dd></div><div><dt>Computed at</dt><dd>{detail.effective_at}</dd></div><div><dt>Coverage</dt><dd>{coveragePercent(q.coverage)} · {q.state} · {q.freshness}</dd></div><div><dt>Window</dt><dd>{current.window.effective_start} – {current.window.end}</dd></div></dl><p>{detail.reasons.map(reason=>reasonLabel(reason.code)+(reason.provisional?' · observed only':'')).join('; ')}</p><p>Evaluation {detail.evaluation_id} · {detail.calculation_version} · definition revision {definition.definition_revision}.</p><p>Requested range {detail.range.requested_start} to {detail.range.requested_end}; effective range {detail.range.effective_start} to {detail.range.effective_end}. Resolution {detail.range.resolution_seconds} seconds.</p><p>{q.observed_intervals} observed, {q.known_empty_intervals} known empty, {q.missing_intervals} missing, {q.expired_intervals} expired; {q.expected_intervals} expected intervals.</p><p>Exhaustion: {detail.exhaustion.reason??detail.exhaustion.state}. {detail.exhaustion.assumptions.join('; ')}</p></details></section>
      <section className="app-rail-card"><header><h2>Definition</h2><a href={`/slis/${definition.sli_id}`} data-app-route>View SLI</a></header><div className="app-rail-card-body"><dl className="app-definition-facts">{[['SLI',definition.sli_display_name],['Type','Request count'],['Datasource',detail.context.datasource_display_name],['Step',`${detail.context.polling_interval_seconds} s`],['Query delay',`${detail.context.settling_delay_seconds} s`],['Spec',`v${detail.context.measurement_revision}`]].map(([label,value])=><div key={label}><dt>{label}</dt><dd>{label==='Datasource'?<a href={`/datasources/${detail.context.datasource_id}`} data-app-route>{value}</a>:value}</dd></div>)}</dl><h3 className="app-query-label">Good events</h3><pre>{detail.context.good_query}</pre><h3 className="app-query-label">Total events</h3><pre>{detail.context.total_query}</pre></div></section>
    </aside></div>
  </section>;
}

const TimelineChartViewLazy=lazy(()=>import('../components/charts/TimelineChartView.tsx').then(module=>({default:module.TimelineChartView})));
