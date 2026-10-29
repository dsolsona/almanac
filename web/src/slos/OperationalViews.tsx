import { Component, Suspense, lazy, useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import type { components } from '../api/generated.ts';
import { Button, Panel, SelectField, StatusBadge, TextField } from '../components/ui/index.ts';
import { ExactValuesTable, type ExactValueRow } from '../components/charts/ExactValuesTable.tsx';
import { SloBudgetSparkline } from '../components/charts/SloBudgetSparkline.tsx';
import { TimelineChartHeader } from '../components/charts/TimelineChartHeader.tsx';
import type { Coverage, TimelineChart } from '../components/charts/timelineModel.ts';
import { sloClient, SloFailure, type OperationalDetail, type OperationalOverview } from './client.ts';
import './slos.css';

class OperationalChartBoundary extends Component<{children:ReactNode},{failed:boolean}> {
  state={failed:false};
  static getDerivedStateFromError(){return {failed:true};}
  render(){return this.state.failed?<p role="alert">The timeline could not be drawn. Its exact values remain available.</p>:this.props.children;}
}

type OverviewItem=OperationalOverview['items'][number];
type Slo=components['schemas']['Slo'];
type Urgency=OverviewItem['urgency'];
const groups:[Urgency,string,string][]=[
  ['needs_attention','Needs attention','Stale observations, unavailable history, violations, low budget or high 1 h burn'],
  ['watch','Watch','Partial eligibility, zero traffic, an unavailable short burn or less than half the budget'],
  ['healthy','Healthy','Fresh, complete results with at least half the budget remaining and 1 h burn below 1×'],
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
function budgetPercent(value:string|null):string { return value===null?'—':`${shift(value,2)}%`; }
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
  if(quality.state==='zero_traffic')return 'No traffic; success and burn are unavailable, not zero.';
  if(quality.state==='not_applicable')return 'This evaluation does not apply to the selected range.';
  if(quality.state==='no_data')return 'No evaluable data; this is not zero.';
  if(quality.state==='expired_history')return 'Required history has expired.';
  if(quality.state==='not_yet_eligible')return 'The objective is not eligible for this interval yet.';
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
function coveragePercent(value:string|null):string {return value===null?'Unknown':`${shift(value,2)}%`;}
function meterFraction(value:string|null):number {
  if(value===null)return 0;
  const numeric=Number(value);
  return Number.isFinite(numeric)?Math.max(0,Math.min(1,numeric)):0;
}
function urgencyLabel(value:Urgency):string { return value==='needs_attention'?'Needs attention':value==='watch'?'Watch':'Healthy'; }
function formatExhaustion(item:OverviewItem['exhaustion']):string {
  if(item.state==='exhausted')return 'Exhausted';
  if(item.state==='unavailable')return item.reason?reasonLabel(item.reason):'Unavailable';
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

function RemainingMeter({fraction,label}:{fraction:string|null;label:string}) {
  return <div className="slo-budget-meter">
    <span className="slo-budget-meter__value">{budgetPercent(fraction)}</span>
    <span className="slo-budget-meter__track" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={fraction===null?undefined:meterFraction(fraction)*100} aria-valuetext={fraction===null?'Unavailable':budgetPercent(fraction)}>
      <span style={{width:`${meterFraction(fraction)*100}%`}}/>
    </span>
  </div>;
}

function OverviewRow({item}:{item:OverviewItem}) {
  const slo=item.slo,p=item.current.performance,q=item.current.quality;
  const first=qualityWarning(q),oneHour=qualityWarning(item.burn_1h.quality),sixHour=qualityWarning(item.burn_6h.quality);
  const leadingIndex=item.reasons.findIndex(reason=>reason.code===item.leading_reason.code&&reason.provisional===item.leading_reason.provisional);
  const secondaryReasons=item.reasons.filter((_,index)=>index!==leadingIndex);
  return <article className="slo-operational-row" role="row">
    <div role="cell" className="slo-operational-row__status"><span className="slo-operational-row__label">Status</span><StatusBadge status={statusFor(item.current)}/><span className="slo-reason">{reasonLabel(item.leading_reason.code)}{item.leading_reason.provisional?' · observed only':''}</span>{secondaryReasons.length>0&&<details className="slo-reasons-disclosure"><summary>More reasons ({secondaryReasons.length})</summary><ul>{secondaryReasons.map((reason,index)=><li key={`${reason.code}-${reason.provisional}-${index}`}>{reasonLabel(reason.code)}{reason.provisional?' · observed only':''}</li>)}</ul></details>}</div>
    <div role="cell" className="slo-operational-row__objective">
      <span className="slo-operational-row__label">Objective and context</span>
      <a data-app-route className="slo-operational-row__name" href={`/slos/${slo.id}`}>{slo.display_name}</a>
      <span><a data-app-route href={`/services/${slo.service_id}`}>{slo.service_display_name}</a> · <a data-app-route href={`/slis/${slo.sli_id}`}>{slo.sli_display_name}</a></span>
      <span className="mono">{shift(slo.target,2)}% over {slo.rolling_window_seconds%86400===0?`${slo.rolling_window_seconds/86400} d`:`${slo.rolling_window_seconds} s`}</span>
    </div>
    <div role="cell" className="slo-operational-row__sparkline"><span className="slo-operational-row__label">Budget · 7 d history</span><SloBudgetSparkline samples={item.budget_sparkline} label={`${slo.display_name} rolling remaining budget`}/><span>{item.budget_sparkline.length?`${item.budget_sparkline.length} samples · 7 d`: 'No history'}</span></div>
    <div role="cell" className="slo-operational-row__remaining"><span className="slo-operational-row__label">Remaining</span><RemainingMeter fraction={p.remaining_fraction} label={`${slo.display_name} budget remaining`}/>{p.remaining_budget!==null&&<span className="mono">{decimal(p.remaining_budget)} events</span>}{first&&<span className="slo-qualification">{first}</span>}</div>
    <div role="cell" className="slo-operational-row__burn"><span className="slo-operational-row__label">Burn 1 h · 6 h</span><strong>{item.burn_1h.performance.burn===null?'—':`${decimal(item.burn_1h.performance.burn)}×`}</strong><span>1 h burn</span>{oneHour&&<span className="slo-qualification">{oneHour}</span>}<small>{item.burn_6h.performance.burn===null?'—':`${decimal(item.burn_6h.performance.burn)}×`} · 6 h{sixHour&&<span className="slo-qualification">{sixHour}</span>}</small></div>
    <div role="cell" className="slo-operational-row__exhaustion"><span className="slo-operational-row__label">Exhaustion</span><strong>{formatExhaustion(item.exhaustion)}</strong><span>Qualified estimate</span>{item.exhaustion.state==='estimated'&&<small>Rolling-window expiry and future traffic can change this.</small>}</div>
    <div role="cell" className="slo-operational-row__open"><a data-app-route href={`/slos/${slo.id}`} aria-label={`Open ${slo.display_name}`}>›</a></div>
  </article>;
}

export function OperationalOverviewView({locationKey,canWrite,onCreate,onLocationChange}:{locationKey:string;canWrite:boolean;onCreate:()=>void;onLocationChange:(path:string,state?:unknown)=>void}) {
  const params=queryFrom(locationKey),query=params.get('q')??'',serviceID=params.get('service_id')??'',urgency=(params.get('urgency')??'') as ''|Urgency;
  const cursor=params.get('cursor')??'';
  const [draftQuery,setDraftQuery]=useState(query),[page,setPage]=useState<OperationalOverview|null>(null),[services,setServices]=useState<Awaited<ReturnType<typeof sloClient.services>>['items']>([]);
  const [error,setError]=useState(''),[busy,setBusy]=useState(false),[refreshCount,setRefreshCount]=useState(0);
  const lastKey=useRef('');
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
  return <section className="slo-operational-overview" aria-label="Operational SLO overview">
    <div className="slo-operational-tools">
      <form className="slo-search" onSubmit={event=>{event.preventDefault();apply({q:draftQuery.trim()});}}>
        <TextField id="slo-search" label="Search SLOs" value={draftQuery} onChange={event=>setDraftQuery(event.target.value)} hint="Search objective names"/>
        <Button type="submit" variant="outline">Search</Button>
      </form>
      <SelectField id="slo-service-filter" label="Service" value={serviceID} onChange={event=>apply({service_id:event.target.value})} options={[{value:'',label:'All services'},...services.map(service=>({value:service.id,label:service.display_name}))]}/>
      <SelectField id="slo-urgency-filter" label="Urgency" value={urgency} onChange={event=>changeUrgency(event.target.value)} options={[{value:'',label:'All urgency groups'},...groups.map(([value,label])=>({value,label}))]}/>
      {canWrite&&<Button onClick={onCreate}>Create SLO</Button>}
      <Button variant="outline" onClick={()=>{
        if(cursor){onLocationChange(updateQuery(locationKey,{},['cursor','previous_cursor']));return;}
        setRefreshCount(value=>value+1);
      }} pending={busy}>Refresh</Button>
    </div>
    <p className="slo-overview-meta">{page?`${Object.values(page.group_counts).reduce((a,b)=>a+b,0)} matching SLOs · counts apply to search and service before urgency filter`:'Operational values are calculated by the server.'}</p>
    {busy&&<span className="sr-only" role="status">Refreshing operational SLOs</span>}
    {error&&<div className="slo-error" role="alert"><p>{error}</p>{page&&<p>Previously loaded values are retained and may be stale.{lastConfirmedAt&&` Last confirmed evaluation: ${lastConfirmedAt}.`}</p>}</div>}
    {snapshotExpired&&<Button variant="outline" onClick={restartSnapshot}>Restart from first page</Button>}
    {page&&<div className="slo-urgency-groups">{groups.map(([group,label,explanation])=>{
      const items=page.items.filter(item=>item.urgency===group);
      const count=page.group_counts[group];
      return <section className="slo-urgency-group" key={group} aria-labelledby={`slo-group-${group}`}>
        <header><h2 id={`slo-group-${group}`} aria-label={`${label}, ${count} SLOs`}>{label} <span className="slo-count">{count}</span></h2><p>{explanation}</p></header>
        <div className="slo-operational-table" role="table" aria-label={`${label} SLOs`}>
          <div role="row" className="slo-operational-columns"><span role="columnheader">Status</span><span role="columnheader">Objective and context</span><span role="columnheader">Budget · 7 d history</span><span role="columnheader">Remaining</span><span role="columnheader">Burn 1 h · 6 h</span><span role="columnheader">Exhaustion</span></div>
          {items.map(item=><OverviewRow item={item} key={item.slo.id}/>)}
          {!items.length&&count>0&&urgency===group&&<p className="slo-muted">No items from this group are on the current page.</p>}
          {!items.length&&count===0&&<p className="slo-muted">No SLOs in this group match these filters.</p>}
        </div>
      </section>;
    })}</div>}
    {page&&!page.items.length&&Object.values(page.group_counts).every(count=>count===0)&&<p className="slo-empty">No SLOs match these filters.</p>}
    {page&&<nav className="slo-page-controls" aria-label="SLO result pages"><Button variant="outline" disabled={!hasPrevious||busy} onClick={previousPage}>Previous page</Button><span className="mono">{cursor?'Snapshot page':'First snapshot page'}</span><Button variant="outline" disabled={!page.next_cursor||busy} onClick={nextPage}>Next page</Button></nav>}
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
  return samples.map(sample=>({timestamp:sample.evaluation_at,value:sample[field]===null?null:asPercent?shift(sample[field]!,2):sample[field],coverage:chartCoverage(sample.state)}));
}
function exactRows(samples:components['schemas']['SloOperationalSample'][],lane:string,field:'attainment'|'remaining_fraction'|'burn',unit:string):ExactValueRow[] {
  return samples.map(sample=>({timestamp:sample.evaluation_at,lane,series:'Backend evaluation',unit,value:sample[field],coverage:chartCoverage(sample.state)}));
}
function LaneTable({title,rows,page,onPageChange}:{title:string;rows:ExactValueRow[];page:number;onPageChange:(value:number)=>void}) {
  return <section className="slo-operational-exact"><h3>{title} exact values</h3><ExactValuesTable rows={rows.slice((page-1)*50,page*50)} page={page} totalRows={rows.length} onPageChange={onPageChange}/></section>;
}
function burnValue(value:components['schemas']['SloBurnValue']) {return value.performance.burn===null?'—':`${decimal(value.performance.burn)}×`;}
function railStatus(detail:OperationalDetail) {return <StatusBadge status={statusFor(detail.current)}/>;}
function TimelineControls({range,zone,burnWindow,onChange}:{range:'7d'|'90d'|'1y';zone:'UTC'|'local';burnWindow:3600|21600;onChange:(values:Record<string,string>)=>void}) {
  return <div className="slo-timeline-controls" aria-label="Timeline controls">
    <fieldset><legend>Range</legend>{(['7d','90d','1y'] as const).map(value=><Button key={value} variant={range===value?'selected':'outline'} aria-pressed={range===value} onClick={()=>onChange({range:value})}>{value}</Button>)}</fieldset>
    <fieldset><legend>Time zone</legend>{(['UTC','local'] as const).map(value=><Button key={value} variant={zone===value?'selected':'outline'} aria-pressed={zone===value} onClick={()=>onChange({tz:value})}>{value==='UTC'?'UTC':'Local'}</Button>)}</fieldset>
    <SelectField id="slo-burn-window" label="Burn window" value={String(burnWindow)} onChange={event=>onChange({burn_window:event.target.value})} options={[{value:'3600',label:'1 hour'},{value:'21600',label:'6 hours'}]}/>
  </div>;
}

export function OperationalDetailView({id,definition,locationKey,onLocationChange,refreshVersion=0,onSessionFailure=()=>{}}:{id:string;definition:Slo;locationKey:string;onLocationChange:(path:string)=>void;refreshVersion?:number;onSessionFailure?:()=>void}) {
  const [detail,setDetail]=useState<OperationalDetail|null>(null),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const [refreshCount,setRefreshCount]=useState(0);
  const params=queryFrom(locationKey);
  const range=(['7d','90d','1y'].includes(params.get('range')??'')?params.get('range'):'7d') as '7d'|'90d'|'1y';
  const zone=(params.get('tz')==='local'?'local':'UTC') as 'UTC'|'local';
  const burnWindow=(params.get('burn_window')==='21600'?21600:3600) as 3600|21600;
  const [tablePages,setTablePages]=useState([1,1,1,1]);
  const [tableOpen,setTableOpen]=useState([false,false,false,false]);
  const generation=useRef(0);
  const sessionFailure=useRef(onSessionFailure);sessionFailure.current=onSessionFailure;
  function changeView(values:Record<string,string>) {
    const next=queryFrom(locationKey);
    for(const [key,value] of Object.entries(values)){if(value)next.set(key,value);else next.delete(key);}
    const search=next.toString();onLocationChange(`/slos/${id}${search?`?${search}`:''}`);
  }
  useEffect(()=>{setTablePages([1,1,1,1]);setTableOpen([false,false,false,false]);},[range,burnWindow]);
  useEffect(()=>{
    const abort=new AbortController(),requestID=++generation.current;setBusy(true);
    setError('');
    const read=async()=>{
      const fresh=await sloClient.operational(id,{burn_window_seconds:burnWindow},abort.signal);
      if(range==='7d')return fresh;
      const cadence=definition.cadence_seconds*1000,rangeMs=(range==='90d'?90:365)*86_400_000;
      const end=Math.floor(Date.parse(fresh.effective_at)/cadence)*cadence;
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
  const rangeMatches=detail?Math.abs((Date.parse(detail.range.requested_end)-Date.parse(detail.range.requested_start))-(range==='7d'?7:range==='90d'?90:365)*86_400_000)<=definition.cadence_seconds*1000:false;
  const chart=useMemo<TimelineChart|null>(()=>{
    if(!detail)return null;
    const start=detail.range.effective_start,end=detail.range.effective_end;
    const attainment=detail.attainment_series.samples,budget=detail.budget_series.samples,burn=detail.burn_series.samples,days=detail.compliance_series.days;
    const selectedBurn=detail.burn_series.burn_window_seconds===21600?'6 h':'1 h';
    return {title:'Operational SLO timeline',summary:`Backend-evaluated attainment, rolling budget remaining, ${selectedBurn} burn, and rolling attainment at each completed UTC day end. Daily outcome and qualification are shown separately. Gaps are explicit and are not treated as zero.`,start,end,showLaneNames:false,
      stepMs:Math.max(1,detail.range.resolution_seconds)*1000,fillExpectedGaps:false,timeZone:zone,
      referenceLines:[{lane:'attainment',value:shift(definition.target,2),label:`Target ${shift(definition.target,2)}%`},{lane:'budget',value:'0',label:'0% remaining'},{lane:'burn',value:'1',label:'1×'},{lane:'compliance',value:shift(definition.target,2),label:`Target ${shift(definition.target,2)}%`}],
      lanes:[
        {id:'attainment',label:'SLI attainment',unit:'%',series:[{id:'attainment-series',label:'Observed',points:seriesPoints(attainment,'attainment',true)}]},
        {id:'budget',label:'Rolling budget remaining',unit:'%',series:[{id:'budget-series',label:'Remaining',points:seriesPoints(budget,'remaining_fraction',true)}]},
        {id:'burn',label:`${selectedBurn} burn`,unit:'×',series:[{id:'burn-series',label:'Observed burn',points:seriesPoints(burn,'burn',false)}]},
        {id:'compliance',label:'Daily compliance',unit:'%',series:[{id:'compliance-series',label:'Rolling attainment at UTC day end',points:days.map(day=>({timestamp:day.day_end,value:day.performance.attainment===null?null:shift(day.performance.attainment,2),coverage:day.status==='unknown'&&day.performance.attainment!==null?'partial':dailyChartCoverage(day)}))}]},
      ]};
  },[detail,definition.target,zone]);
  if(!detail)return <section className="slo-operational-detail" aria-label="Operational SLO detail">
    <div className="slo-detail-heading"><h2>Operational SLO timeline</h2><div className="slo-detail-tools"><Button variant="outline" pending={busy} onClick={()=>setRefreshCount(value=>value+1)}>Refresh timeline</Button></div></div>
    {error&&<div className="slo-error" role="alert"><p>{error}</p><p>Refresh or select a shorter range to retry the operational evaluation.</p></div>}
    {busy&&<span className="sr-only" role="status">Loading operational evaluation</span>}
    <Panel heading="Timeline" className="slo-timeline-panel"><TimelineControls range={range} zone={zone} burnWindow={burnWindow} onChange={changeView}/></Panel>
  </section>;
  const current=detail.current,p=current.performance,q=current.quality;
  const metricWarning=qualityWarning(q,p,current.window),oneHourWarning=qualityWarning(detail.burn_1h.quality,detail.burn_1h.performance),sixHourWarning=qualityWarning(detail.burn_6h.quality,detail.burn_6h.performance);
  const activeBurn=activeBurnWindow===3600?detail.burn_1h:detail.burn_6h;
  const daily=detail.compliance_series;
  const lanes=[
    {title:'SLI attainment',summary:'Exact observed ratios',rows:exactRows(detail.attainment_series.samples,'SLI attainment','attainment','ratio')},
    {title:'Rolling budget remaining',summary:'Exact rolling objective budget fractions',rows:exactRows(detail.budget_series.samples,'Rolling budget remaining','remaining_fraction','fraction')},
    {title:`${activeBurnLabel} burn`,summary:'Exact selected-window burn ratios',rows:exactRows(detail.burn_series.samples,'Burn rate','burn','ratio')},
    {title:'Daily compliance',summary:'Completed UTC days; each rolling objective is evaluated at its canonical effective end',rows:daily.days.map(day=>({timestamp:day.day_end,lane:'Daily compliance',series:`${dailyStatusLabel(day.status)}${day.performance.provisional?' · provisional':''}`,unit:'%',value:day.performance.attainment===null?null:shift(day.performance.attainment,2),coverage:day.status==='unknown'&&day.performance.attainment!==null?'partial':dailyChartCoverage(day),details:`${day.explanation.replaceAll('_',' ')} · requested ${day.requested_at} · effective ${day.effective_at} · target ${shift(day.target,2)}% · attainment ${day.performance.attainment===null?'unavailable':`${shift(day.performance.attainment,2)}%`}`}))},
  ];
  const laneSummaries=[
    {value:p.attainment===null?'Unavailable':`${shift(p.attainment,2)}%`,detail:'',tableOpen:tableOpen[0]!,onTableToggle:()=>setTableOpen(values=>values.map((value,index)=>index===0?!value:value))},
    {value:budgetPercent(p.remaining_fraction),detail:'',tableOpen:tableOpen[1]!,onTableToggle:()=>setTableOpen(values=>values.map((value,index)=>index===1?!value:value))},
    {value:burnValue(activeBurn),detail:'',tableOpen:tableOpen[2]!,onTableToggle:()=>setTableOpen(values=>values.map((value,index)=>index===2?!value:value))},
    {value:`${detail.compliance_summary.met_days} / ${detail.compliance_summary.evaluable_days}`,detail:`${detail.compliance_summary.violated_days} violated · ${detail.compliance_summary.unknown_days} unknown · ${detail.compliance_summary.not_applicable_days} N/A`,tableOpen:tableOpen[3]!,onTableToggle:()=>setTableOpen(values=>values.map((value,index)=>index===3?!value:value))},
  ];
  return <section className="slo-operational-detail" aria-label="Operational SLO detail">
    <div className="slo-detail-heading"><div><p className="slo-subtitle"><a href={`/services/${definition.service_id}`} data-app-route>{definition.service_display_name}</a> · {shift(definition.target,2)}% over {definition.rolling_window_seconds/86400} d rolling · <a href={`/slis/${detail.context.sli_id}`} data-app-route>{detail.context.sli_display_name}</a></p><p className="slo-reasons">{urgencyLabel(detail.urgency)} · {detail.reasons.map(reason=>`${reasonLabel(reason.code)}${reason.provisional?' (observed only)':''}`).join(' · ')}</p></div><div className="slo-detail-tools"><Button variant="outline" pending={busy} onClick={()=>setRefreshCount(value=>value+1)}>Refresh timeline</Button></div></div>
    {error&&<div className="slo-error" role="alert"><p>{error}</p>{detail&&<p className="slo-qualification">Showing retained values from the last confirmed evaluation at {detail.effective_at}; the current result may be stale.</p>}</div>}{busy&&<span className="sr-only" role="status">Refreshing operational evaluation</span>}
    <div className="slo-operational-layout">
      <div className="slo-operational-timeline">
        <Panel heading="Timeline" className="slo-timeline-panel">
          <TimelineControls range={range} zone={zone} burnWindow={burnWindow} onChange={changeView}/>
          {detail&&(!rangeMatches||activeBurnWindow!==burnWindow)&&<p className="slo-qualification" role={busy?'status':'alert'}>Showing the last confirmed {Math.round((Date.parse(detail.range.requested_end)-Date.parse(detail.range.requested_start))/86_400_000)} d range and {activeBurnLabel} burn{busy?`; loading ${range} and ${burnWindow===3600?'1 h':'6 h'} results.`:`; the requested ${range} and ${burnWindow===3600?'1 h':'6 h'} result has not replaced it.`}</p>}
          {chart&&chart.lanes.every(lane=>lane.series[0]?.points.length===0)&&<p className="slo-empty">No chart samples are available in this range. Missing data is not drawn as zero.</p>}
          {chart&&<>
            <div className="timeline-chart__header"><TimelineChartHeader chart={chart}/></div>
            <div className="timeline-chart__frame" data-lanes={chart.lanes.length} style={{'--timeline-lane-count':chart.lanes.length} as CSSProperties}>
              <div className="timeline-chart__lane-controls" data-lanes={chart.lanes.length} aria-label="Lane values and tables">{chart.lanes.map((lane,index)=><div className="timeline-chart__lane-control" key={lane.id} style={{gridRow:2+index*2}}><strong>{lane.label}</strong><span className="mono">{laneSummaries[index]?.value??'Unavailable'}</span>{laneSummaries[index]?.detail&&<span className="slo-qualification">{laneSummaries[index].detail}</span>}<Button variant="outline" aria-expanded={tableOpen[index]} onClick={laneSummaries[index]?.onTableToggle}>{tableOpen[index]?'Hide table':'Table'}</Button></div>)}</div>
              <OperationalChartBoundary><Suspense fallback={<span className="sr-only" role="status">Opening timeline</span>}><TimelineChartViewLazy chart={chart} showHeader={false}/></Suspense></OperationalChartBoundary>
            </div>
          </>}
          <div className="slo-detail-lanes">{lanes.map((lane,index)=>tableOpen[index]&&<LaneTable key={lane.title} title={lane.title} rows={lane.rows} page={tablePages[index]!} onPageChange={value=>setTablePages(current=>current.map((page,laneIndex)=>laneIndex===index?value:page))}/>)}</div>
          <p className="slo-timeline-range">{detail.range.effective_start} to {detail.range.effective_end} (half-open source buckets; end-aligned evaluation points shown) · {detail.range.reduced?'extrema-preserving reduced samples':'source-resolution samples'} · {detail.range.resolution_seconds}s resolution · source cadence {detail.range.source_cadence_seconds}s</p>
          <p className="slo-timeline-range">Daily compliance shows met / evaluable rolling attainment. Unknown and not-applicable days remain separate; exact rows include status, target and effective evaluation time.</p>
        </Panel>
      </div>
      <aside className="slo-operational-rail" aria-label="Current SLO status and definition">
        <Panel heading="Status" className="slo-status-panel"><div className="slo-status-hero"><strong>{budgetPercent(p.remaining_fraction)}</strong><span>error budget remaining</span><span className="slo-budget-meter__track" aria-hidden="true"><span style={{width:`${meterFraction(p.remaining_fraction)*100}%`}}/></span></div>
          {metricWarning&&<p className="slo-qualification">{metricWarning}</p>}
          <dl className="slo-rail-metrics">
            <div><dt>Performance</dt><dd>{railStatus(detail)}</dd></div>
            <div><dt>Attainment</dt><dd>{p.attainment===null?'Unavailable':`${shift(p.attainment,2)}%`}</dd></div>
            {p.provisional&&<div><dt>Qualification</dt><dd>Observed only; outcome is not definitive</dd></div>}
            <div><dt>Collection coverage</dt><dd>{coveragePercent(q.coverage)}</dd></div>
            <div><dt>Target</dt><dd>{shift(definition.target,2)}%</dd></div>
            <div><dt>Burn 1 h</dt><dd>{burnValue(detail.burn_1h)}</dd></div>{oneHourWarning&&<p className="slo-qualification">{oneHourWarning}</p>}
            <div><dt>Burn 6 h</dt><dd>{burnValue(detail.burn_6h)}</dd></div>{sixHourWarning&&<p className="slo-qualification">{sixHourWarning}</p>}
            <div><dt>Bad events left</dt><dd>{p.remaining_budget===null?'Unavailable':decimal(p.remaining_budget)}</dd></div>
            <div><dt>Exhaustion</dt><dd>{formatExhaustion(detail.exhaustion)}</dd></div>
          </dl>
          {detail.exhaustion.state==='estimated'&&<p className="slo-muted">Estimate assumes the current allowed budget stays fixed and the observed 1 h bad-event rate continues. Window expiry and future traffic can change it.</p>}
          {detail.exhaustion.state==='unavailable'&&<p className="slo-muted">{detail.exhaustion.reason??'The estimate is unavailable.'}</p>}
        </Panel>
        <details className="slo-calculation-disclosure"><summary>Calculation and coverage details</summary>
          <p>Evaluation {detail.evaluation_id} · {detail.calculation_version} · effective {detail.effective_at}.</p>
          <p>{q.observed_intervals} observed, {q.known_empty_intervals} known empty, {q.missing_intervals} missing, {q.zero_traffic_intervals} zero traffic and {q.expired_intervals} expired of {q.expected_intervals} expected intervals. Collection coverage: {coveragePercent(q.coverage)}. Freshness: {q.freshness}.</p>
          <p>Allowed budget: {p.allowed_budget??'Unavailable'} events · remaining: {p.remaining_budget??'Unavailable'} events · calculation state: {p.state}{p.provisional?' (observed only)':''}.</p>
          <p>Exhaustion inputs: {detail.exhaustion.assumptions.join('; ').replaceAll('_',' ')}.</p>
          <p>Last observation: {q.last_observation_at??'Not recorded'} · receipt metadata: {q.latest_receipt_at??'Not recorded'}.</p>
        </details>
        <Panel heading="Definition" className="slo-definition-panel">
          <dl><div><dt>SLI</dt><dd><a href={`/slis/${detail.context.sli_id}`} data-app-route>{detail.context.sli_display_name}</a></dd></div><div><dt>Datasource</dt><dd><a href={`/datasources/${detail.context.datasource_id}`} data-app-route>{detail.context.datasource_display_name}</a></dd></div><div><dt>Measurement revision</dt><dd>{detail.context.measurement_revision}</dd></div><div><dt>Collection interval</dt><dd>{detail.context.polling_interval_seconds} s</dd></div><div><dt>Settling delay</dt><dd>{detail.context.settling_delay_seconds} s</dd></div></dl>
          <details><summary>SLI queries</summary><h3>Good events</h3><pre>{detail.context.good_query}</pre><h3>Total events</h3><pre>{detail.context.total_query}</pre></details>
          <p>Service: <a href={`/services/${definition.service_id}`} data-app-route>{definition.service_display_name}</a></p><p>SLO identifier: <span className="mono">{definition.name}</span> · revision {definition.definition_revision}</p>
        </Panel>
      </aside>
    </div>
  </section>;
}

const TimelineChartViewLazy=lazy(()=>import('../components/charts/TimelineChartView.tsx').then(module=>({default:module.TimelineChartView})));
