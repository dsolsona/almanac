import { Component, lazy, Suspense, useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, Panel, SelectField, TextField } from '../components/ui/index.ts';
import { ExactValuesTable, type ExactValueRow } from '../components/charts/ExactValuesTable.tsx';
import type { Coverage, TimelineChart } from '../components/charts/timelineModel.ts';
import { createSliClient, type Sli } from '../slis/client.ts';
import { sloClient, SloFailure, type CreateSlo, type Evaluation, type Service, type Slo } from './client.ts';
import './slos.css';
export class ChartBoundary extends Component<{children: React.ReactNode}, {failed:boolean}> { state={failed:false}; static getDerivedStateFromError(){return {failed:true};} render(){return this.state.failed ? <p role="alert">The chart could not be opened. Exact values remain available below.</p> : this.props.children;} }
const Chart = lazy(() => import('../components/charts/TimelineChartView.tsx').then(m => ({default:m.TimelineChartView})));
const sliClient = createSliClient();
const namePattern = /^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$/;
const suggest = (s: string) => s.normalize('NFKD').replace(/\p{M}/gu, '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0,63).replace(/-+$/g,'');
const date = (s: string | null) => s ? `${new Date(s).toLocaleString(undefined,{dateStyle:'medium',timeStyle:'medium',timeZone:'UTC'})} UTC` : 'Not recorded';
const key = () => crypto.randomUUID();
const message = (e: unknown) => e instanceof Error ? e.message : 'The request could not be completed.';
function navigate(path: string) { if (`${location.pathname}${location.search}` !== path) { history.pushState({}, '', path); dispatchEvent(new PopStateEvent('popstate')); } }
// Shift the decimal point with strings so that exact targets and values never pass through binary floating point.
function shift(value: string, places: number): string {
  const neg = value.startsWith('-');
  const v = neg ? value.slice(1) : value;
  const [whole, frac = ''] = v.split('.');
  const digits = `${whole}${frac}`;
  const point = whole!.length + places;
  let out = point <= 0 ? `0.${'0'.repeat(-point)}${digits}` : point >= digits.length ? `${digits}${'0'.repeat(point-digits.length)}` : `${digits.slice(0,point)}.${digits.slice(point)}`;
  out = out.replace(/^0+(?=\d)/, '').replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '');
  return `${neg ? '-' : ''}${out}`;
}
function percentTarget(value: string): string | null {
  if (!/^(?:\d+)(?:\.\d{1,10})?$/.test(value)) return null;
  const [whole] = value.split('.'); if (BigInt(whole!) >= 100n) return null;
  return shift(value, -2);
}
const decimal = (value: string) => value.replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '');
function compactPercent(value: string) { const percentage=shift(value,2);
  const [whole,fraction='']=percentage.split('.');
      if(fraction.length<=2 || /^0*$/.test(fraction.slice(2))) return decimal(percentage);
  const hundredths=BigInt(whole!)*100n+BigInt((fraction+'00').slice(0,2))+(Number(fraction[2])>=5?1n:0n);
  return `≈${hundredths/100n}.${(hundredths%100n).toString().padStart(2,'0')}`; }
function windowLabel(seconds: number) { for (const [unit, size] of [['week',604800],['day',86400],['hour',3600],['minute',60]] as const) if (seconds % size === 0) { const n=seconds/size;
  return `${n} ${unit}${n===1?'':'s'}`; } return `${seconds} seconds`; }
function coverage(state: Evaluation['timeline']['items'][number]['state']): Coverage {
  return ({ observed:'complete', zero_traffic:'zero-traffic', known_empty:'known-empty', missing:'missing', expired:'expired', not_applicable:'not-applicable' } as const)[state];
}
function Heading({title, list=false}:{title:string; list?:boolean}) { return <div>
      <nav className="app-breadcrumbs" aria-label="Breadcrumb">{list ? <span aria-current="page">SLOs</span> : <>
      <a href="/slos" data-app-route>SLOs</a>
      <span aria-hidden="true">/</span>
      <span aria-current="page">{title}</span>
      </>}</nav>
      <h1 className="app-page-heading" tabIndex={-1}>{title}</h1>
      </div>; }
async function allPages<T>(fn:(cursor:string)=>Promise<{items:T[];next_cursor:string}>):Promise<T[]> { const items:T[]=[];
  const seen=new Set<string>();
  let cursor=''; do { const page=await fn(cursor); items.push(...page.items); cursor=page.next_cursor; if (cursor && seen.has(cursor)) throw new Error('Pagination did not advance.');
      if(cursor) seen.add(cursor); } while(cursor);
  return items; }
type Intent = {body:string; key:string; uncertain:boolean};
export function SlosPage({context}:{context:AuthContext}) {
  const [locationKey,setLocationKey]=useState(() => location.pathname + location.search);
  const [list,setList]=useState<Slo[]>([]);
  const [cursor,setCursor]=useState('');
  const [services,setServices]=useState<Service[]>([]);
  const [slis,setSlis]=useState<Sli[]>([]);
  const [slo,setSlo]=useState<Slo|null>(null);
  const [service,setService]=useState<Service|null>(null);
  const [etag,setEtag]=useState('');
  const [evaluation,setEvaluation]=useState<Evaluation|null>(null);
  const [loading,setLoading]=useState(true);
  const [evaluationBusy,setEvaluationBusy]=useState(false);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState('');
  const [evalError,setEvalError]=useState('');
  const [formError,setFormError]=useState('');
  const [page,setPage]=useState(1);
  const [metadata,setMetadata]=useState('');
  const [display,setDisplay]=useState('');
  const [name,setName]=useState('');
  const [nameEdited,setNameEdited]=useState(false);
  const [serviceID,setServiceID]=useState('');
  const [sliID,setSliID]=useState('');
  const [target,setTarget]=useState('99.9');
  const [duration,setDuration]=useState('30');
  const [unit,setUnit]=useState('days');
  const [historical,setHistorical]=useState(false);
  const [bound,setBound]=useState('');
  const [boundError,setBoundError]=useState('');
  const [boundRevision,setBoundRevision]=useState(0);
  const [serviceDisplay,setServiceDisplay]=useState('');
  const [serviceName,setServiceName]=useState('');
  const [serviceNameEdited,setServiceNameEdited]=useState(false);
  const [addingService,setAddingService]=useState(false);
  const sloIntent=useRef<Intent|null>(null);
  const serviceIntent=useRef<Intent|null>(null);
  const generation=useRef(0);
  const heading=useRef<HTMLDivElement>(null);
  const canWrite = !context.session?.restricted && ['administrator','editor'].includes(context.session?.role ?? '');
  const path=locationKey.split('?')[0]!;
  const id=path.match(/^\/(?:slos|services)\/([0-9a-f-]{36})$/)?.[1];
  const isService=path.startsWith('/services/');
  const isNew=path==='/slos/new';
  const isList=path==='/slos';
  useEffect(() => {
    const change = () => {
      const next = location.pathname + location.search;
      if (next !== locationKey) {
        generation.current++;
        setLocationKey(next);
      }
    };
    addEventListener('popstate', change);
    return () => removeEventListener('popstate', change);
  }, [locationKey]);

  useEffect(() => {
    const abort = new AbortController();
    let active = true;
    setLoading(true);
    setEvaluationBusy(false);
    setBusy(false);
    setError('');
    setEvalError('');
    setSlo(null);
    setService(null);
    setEvaluation(null);
    setPage(1);
    const run = async () => {
      try {
        if (isList) {
          const data = await sloClient.list('', abort.signal);
          if (active) { setList(data.items); setCursor(data.next_cursor); }
        } else if (isNew) {
          const [availableServices, availableSlis] = await Promise.all([
            allPages(cursor => sloClient.services(cursor, abort.signal)),
            allPages(cursor => sliClient.list(200, cursor, abort.signal)),
          ]);
          if (active) { setServices(availableServices); setSlis(availableSlis); }
        } else if (id && isService) {
          const data = await sloClient.service(id, abort.signal);
          if (active) { setService(data.value); setMetadata(data.value.display_name); setEtag(data.etag); }
        } else if (id) {
          const data = await sloClient.get(id, abort.signal);
          if (active) {
            setSlo(data.value); setMetadata(data.value.display_name); setEtag(data.etag);
            setLoading(false); setEvaluationBusy(true);
          }
          try {
            const result = await sloClient.evaluation(id, abort.signal);
            if (active) setEvaluation(result);
          } catch (failure) {
            if (active) setEvalError(message(failure));
          } finally {
            if (active) setEvaluationBusy(false);
          }
        }
      } catch (failure) {
        if (active) setError(message(failure));
      } finally {
        if (active) {
          setLoading(false);
          requestAnimationFrame(() => {
            if (!active) return;
            const focused = document.activeElement;
            if (focused instanceof HTMLElement && heading.current?.contains(focused) &&
                focused.matches('input, select, textarea, button, a, [contenteditable="true"]')) return;
            heading.current?.querySelector('h1')?.focus();
          });
        }
      }
    };
    void run();
    return () => { active = false; abort.abort(); };
  }, [locationKey]);

  useEffect(() => {
    setBound(''); setBoundError(''); setHistorical(false);
    if (!sliID) return;
    const abort = new AbortController();
    void sliClient.observations(sliID, 1, '', abort.signal)
      .then(result => setBound(result.earliest_queryable_interval_start))
      .catch(() => { if (!abort.signal.aborted) setBoundError('The eligible historical range could not be loaded.'); });
    return () => abort.abort();
  }, [sliID, boundRevision]);

  const selectedSli = slis.find(item => item.id === sliID);
  const seconds = Number(duration) * ({ minutes: 60, hours: 3600, days: 86400, weeks: 604800 }[unit] ?? 0);
  const historyStart = useMemo(() => {
    if (!bound || !selectedSli || !Number.isSafeInteger(seconds) || seconds <= 0) return '';
    const cadence = selectedSli.polling_interval_seconds * 1000;
    const end = Math.floor(Date.now() / cadence) * cadence;
    const start = Math.max(Date.parse(bound), end - seconds * 1000);
    return Number.isFinite(start) && start < end ? new Date(start).toISOString() : '';
  }, [bound, selectedSli, seconds]);
  const frozen = Boolean(sloIntent.current?.uncertain);
  const serviceFrozen = Boolean(serviceIntent.current?.uncertain);

  async function createService(event: FormEvent) {
    event.preventDefault();
    setFormError('');
    if (!namePattern.test(serviceName) || !serviceDisplay.trim()) {
      setFormError('Enter a display name and a lowercase service identifier.');
      return;
    }
    const body = JSON.stringify({ name: serviceName, display_name: serviceDisplay.trim() });
    if (!serviceIntent.current || serviceIntent.current.body !== body) {
      serviceIntent.current = { body, key: key(), uncertain: false };
    }
    const current = generation.current;
    setBusy(true);
    try {
      const result = await sloClient.createService(JSON.parse(body), context, serviceIntent.current.key);
      serviceIntent.current = null;
      if (current !== generation.current) return;
      setServices(items => [...items, result]);
      setServiceID(result.id); setAddingService(false); setServiceDisplay(''); setServiceName('');
    } catch (failure) {
      if (failure instanceof SloFailure && failure.uncertain && serviceIntent.current) serviceIntent.current.uncertain = true;
      if (current === generation.current) setFormError(message(failure));
    } finally {
      if (current === generation.current) setBusy(false);
    }
  }

  async function create(event: FormEvent) {
    event.preventDefault();
    setFormError('');
    const targetFraction = percentTarget(target);
    const problem = !display.trim() ? 'Enter a display name.'
      : !namePattern.test(name) ? 'Use lowercase letters, numbers and hyphens for the SLO identifier.'
      : !serviceID ? 'Select a service.'
      : !selectedSli ? 'Select an SLI.'
      : targetFraction === null ? 'Target must be from 0% up to but not including 100%.'
      : !/^\d+$/.test(duration) || !Number.isSafeInteger(seconds) || seconds < 1 || seconds > 2147483647
        ? 'Enter a positive whole window within the supported limit.'
      : seconds % selectedSli.polling_interval_seconds !== 0
        ? `The window must be a multiple of the ${selectedSli.polling_interval_seconds}-second collection interval.`
      : historical && !historyStart ? 'The eligible historical range is unavailable. Refresh and try again.' : '';
    if (problem) {
      setFormError(problem);
      heading.current?.querySelector('h1')?.focus();
      return;
    }
    const input: CreateSlo = {
      name, display_name: display.trim(), service_id: serviceID, sli_id: sliID,
      target: targetFraction!, rolling_window_seconds: seconds,
      ...(historical ? { evaluation_valid_from: historyStart } : {}),
    };
    const body = JSON.stringify(input);
    if (!sloIntent.current || sloIntent.current.body !== body) sloIntent.current = { body, key: key(), uncertain: false };
    const current = generation.current;
    setBusy(true);
    try {
      const result = await sloClient.create(input, context, sloIntent.current.key);
      sloIntent.current = null;
      if (current === generation.current) navigate(`/slos/${result.id}`);
    } catch (failure) {
      if (failure instanceof SloFailure && failure.uncertain && sloIntent.current) sloIntent.current.uncertain = true;
      if (current === generation.current) {
        setFormError(message(failure));
        heading.current?.querySelector('h1')?.focus();
      }
    } finally {
      if (current === generation.current) setBusy(false);
    }
  }

  async function refresh() {
    if (!id) return;
    const current = generation.current;
    setBusy(true); setError(''); setEvalError('');
    try {
      if (isService) {
        const result = await sloClient.service(id);
        if (current !== generation.current) return;
        setService(result.value); setMetadata(result.value.display_name); setEtag(result.etag);
      } else {
        const result = await sloClient.get(id);
        if (current !== generation.current) return;
        setSlo(result.value); setMetadata(result.value.display_name); setEtag(result.etag);
        try {
          const updated = await sloClient.evaluation(id);
          if (current === generation.current) setEvaluation(updated);
        } catch (failure) {
          if (current === generation.current) {
            if (failure instanceof SloFailure && (failure.status === 401 || failure.status === 403)) {
              setSlo(null); setEvaluation(null); setError(message(failure));
            } else setEvalError(`${message(failure)} Previously shown values may be stale.`);
          }
        }
      }
    } catch (failure) {
      if (current === generation.current) {
        if (failure instanceof SloFailure && (failure.status === 401 || failure.status === 403)) {
          setSlo(null); setService(null); setEvaluation(null); setError(message(failure));
        } else setError(`${message(failure)} Previously shown values may be stale.`);
      }
    } finally {
      if (current === generation.current) setBusy(false);
    }
  }

  async function saveMetadata(event: FormEvent) {
    event.preventDefault();
    if (!id || !metadata.trim()) return;
    const current = generation.current;
    setBusy(true); setFormError('');
    try {
      const result = isService
        ? await sloClient.updateService(id, metadata.trim(), etag, context)
        : await sloClient.update(id, metadata.trim(), etag, context);
      if (current !== generation.current) return;
      setEtag(result.etag);
      if (isService) setService(result.value as Service); else setSlo(result.value as Slo);
    } catch (failure) {
      if (current === generation.current) setFormError(message(failure));
    } finally {
      if (current === generation.current) setBusy(false);
    }
  }
  const title=isList?'SLOs':isNew?'Create SLO':isService?(service?.display_name??'Service details'):(slo?.display_name??'SLO details');
  return <div className="slos-page" ref={heading}>
      <div className="slo-title">
      <Heading title={title} list={isList}/>{!loading&&isList&&canWrite&&list.length>0&&<Button onClick={()=>navigate('/slos/new')}>Create SLO</Button>}{id&&<Button variant="outline" onClick={()=>void refresh()} pending={busy}>Refresh</Button>}</div>
    {loading&&<span className="sr-only" role="status">Opening {isService?'service':'SLOs'}</span>}{error&&<p className="slo-error" role="alert">{error}</p>}
    {!loading&&isList&&<>{list.length===0&&!error?<div className="slo-empty">
      <p>{canWrite?'Create your first objective using an existing SLI.':'There are no objectives to show.'}</p>{canWrite&&<Button onClick={()=>navigate('/slos/new')}>Create SLO</Button>}</div>:<div className="slo-list">{list.map(item=>
      <a data-app-route href={`/slos/${item.id}`} className="slo-row" key={item.id}>
      <strong>{item.display_name}</strong>
      <span>{item.service_display_name} · {shift(item.target,2)}% target · {windowLabel(item.rolling_window_seconds)}</span>
      </a>)}</div>}{cursor&&<Button variant="outline" pending={busy} onClick={()=>{const current=generation.current;
      setBusy(true);void sloClient.list(cursor).then(x=>{if(current===generation.current){setList(v=>[...v,...x.items]);
      setCursor(x.next_cursor);}}).catch(e=>{if(current===generation.current)setError(message(e));}).finally(()=>{if(current===generation.current)setBusy(false);});}}>Load more SLOs</Button>}</>}
    {!loading&&isNew&&(!canWrite?<p>Editor or administrator access is required to create SLOs.</p>:<>
      <p>Define an objective from an existing request-count SLI.</p>{slis.length===0?<div className="slo-empty">
      <p>Create an SLI before creating an objective.</p>
      <a href="/slis" data-app-route>Go to SLIs</a>
      </div>:<>
      {addingService?<form onSubmit={e=>void createService(e)}>
      <Panel heading="Create service">
      <div className="slo-form-grid">
      <TextField id="service-display" label="Service display name" value={serviceDisplay} disabled={serviceFrozen||busy} onChange={e=>{setServiceDisplay(e.target.value);
      if(!serviceNameEdited)setServiceName(suggest(e.target.value));}}/>
      <TextField id="service-name" label="Service identifier" value={serviceName} disabled={serviceFrozen||busy} onChange={e=>{setServiceNameEdited(true);
      setServiceName(e.target.value);}} hint="Lowercase letters, numbers and hyphens."/>
      </div>{formError&&<p role="alert" className="slo-error">{formError}</p>}<div className="slo-actions">
      <Button variant="outline" disabled={serviceFrozen} onClick={()=>{setAddingService(false);
      setFormError('');}}>Cancel</Button>
      <Button type="submit" pending={busy}>{serviceFrozen?'Retry same service request':'Create service'}</Button>
      </div>
      </Panel>
      </form>:<form onSubmit={e=>void create(e)} noValidate>
      <Panel heading="Objective">
      <fieldset disabled={frozen||busy} className="slo-fields">
      <div className="slo-form-grid">
      <TextField id="slo-display" label="Display name" value={display} onChange={e=>{setDisplay(e.target.value);
      if(!nameEdited)setName(suggest(e.target.value));}}/>
      <TextField id="slo-name" label="SLO identifier" value={name} onChange={e=>{setNameEdited(true);
      setName(e.target.value);}} hint="Lowercase letters, numbers and hyphens."/>
      <SelectField id="slo-service" label="Service" value={serviceID} onChange={e=>setServiceID(e.target.value)} options={[{value:'',label:'Select a service',disabled:true},...services.map(x=>({value:x.id,label:x.display_name}))]}/>
      <div className="slo-add-service">
      <Button variant="outline" onClick={()=>{setAddingService(true);
      setFormError('');}}>Create service</Button>
      </div>
      <SelectField id="slo-sli" label="SLI" value={sliID} onChange={e=>setSliID(e.target.value)} options={[{value:'',label:'Select an SLI',disabled:true},...slis.map(x=>({value:x.id,label:x.display_name}))]}/>
      <TextField id="slo-target" label="Target (%)" inputMode="decimal" value={target} onChange={e=>setTarget(e.target.value)} hint="From 0 up to but not including 100%."/>
      <TextField id="slo-duration" label="Rolling window" inputMode="numeric" value={duration} onChange={e=>setDuration(e.target.value)}/>
      <SelectField id="slo-unit" label="Window unit" value={unit} onChange={e=>setUnit(e.target.value)} options={['minutes','hours','days','weeks'].map(x=>({value:x,label:x}))}/>
      </div>
      <p>A day is 24 hours. The window must align with the SLI’s {selectedSli?.polling_interval_seconds ?? 'selected'}-second collection interval.</p>{sliID&&<div className="slo-history">
      <label>
      <input type="checkbox" checked={historical} disabled={!historyStart} onChange={e=>setHistorical(e.target.checked)}/> Apply this target to earlier SLI measurements{historyStart?` from ${date(historyStart)}`:''}</label>
      <p>This eligible retained range may contain gaps. It evaluates existing measurements retrospectively, does not start backfill, and does not establish a past operational commitment.</p>
      {boundError&&<p role="alert">{boundError}</p>}
      <Button variant="outline" onClick={() => setBoundRevision(value => value + 1)}>Refresh eligible range</Button>
      </div>}</fieldset>{formError&&<p role="alert" className="slo-error">{formError}</p>}<div className="slo-actions">
      <Button variant="outline" disabled={frozen} onClick={()=>navigate('/slos')}>Cancel</Button>
      <Button type="submit" pending={busy}>{frozen?'Retry same SLO request':'Create SLO'}</Button>
      </div>{frozen&&<p>Check the SLO list before retrying. The original request and key are retained.</p>}</Panel>
      </form>}</>}</>)}
    {!loading&&isService&&service&&<>
      <Panel heading="Service">
      <p>{service.display_name}</p>
      <p className="slo-muted">Identifier: {service.name}</p>
      </Panel>{canWrite&&<form onSubmit={e=>void saveMetadata(e)}>
      <TextField id="service-rename" label="Display name" value={metadata} onChange={e=>setMetadata(e.target.value)}/>
      <Button type="submit" pending={busy}>Save display name</Button>
      </form>}{formError&&<p role="alert">{formError}</p>}</>}
    {!loading&&!isService&&slo&&<>
      <p className="slo-subtitle">{shift(slo.target,2)}% target · rolling {windowLabel(slo.rolling_window_seconds)}</p>
      <p className="slo-related">Service: <a href={`/services/${slo.service_id}`} data-app-route>{slo.service_display_name}</a> · SLI: <a href={`/slis/${slo.sli_id}`} data-app-route>{slo.sli_display_name}</a></p>
      {evalError&&<p className="slo-error" role="alert">{evalError}</p>}{evaluationBusy&&<span className="sr-only" role="status">Opening evaluation</span>}{evaluation&&<Results evaluation={evaluation} page={page} setPage={setPage}/>}<details>
      <summary>Settings and technical details</summary>
      <p>Identifier: {slo.name} · Definition revision: {slo.definition_revision} · Resource version: {slo.version}</p>
      <p>Activated: {date(slo.activated_at)} · Evaluation valid from: {date(slo.evaluation_valid_from)}</p>{slo.historical_requested_at&&<p>This target was explicitly applied to earlier measurements on {date(slo.historical_requested_at)}.</p>}{canWrite&&<form onSubmit={e=>void saveMetadata(e)}>
      <TextField id="slo-rename" label="Display name" value={metadata} onChange={e=>setMetadata(e.target.value)}/>
      <Button type="submit" pending={busy}>Save display name</Button>
      </form>}{formError&&<p role="alert">{formError}</p>}</details>
      </>}
  </div>;
}
function Results({evaluation:e,page,setPage}:{evaluation:Evaluation;page:number;
      setPage:(n:number)=>void}) { const p=e.performance,q=e.quality,t=e.timeline;
  const eligible=t.items.filter(x=>x.state!=='not_applicable');
  const points=eligible.map(x=>({timestamp:x.start,value:x.attainment,coverage:coverage(x.state)}));
  const rows:ExactValueRow[]=points.map(x=>({timestamp:x.timestamp,lane:'Success',series:'Observed',unit:'ratio',value:x.value,coverage:x.coverage}));
  const chart:TimelineChart={title:'Success over recent intervals',summary:'Observed success for each collection interval. Gaps are not zero.',start:eligible[0]?.start ?? t.start,end:t.end,stepMs:t.cadence_seconds*1000,lanes:[{id:'success',label:'Success',unit:'ratio',series:[{id:'observed',label:'Observed success',points}]}]};
  const causes = [
    q.missing_intervals ? `${q.missing_intervals} missing` : '',
    q.expired_intervals ? `${q.expired_intervals} expired` : '',
    q.known_empty_intervals ? `${q.known_empty_intervals} ${q.known_empty_intervals === 1 ? 'interval returned' : 'intervals returned'} no measurement` : '',
  ].filter(Boolean);
  return <>
      <section className="slo-result" aria-labelledby="result-heading">
      <h2 id="result-heading">Current objective</h2>
      <p>{date(e.window.effective_start)} – {date(e.window.end)} (end excluded)</p>
      <p>Evaluating measurements since {date(e.window.eligible_start)}.</p>
      {p.attainment===null&&<p>{q.state==='zero_traffic'?'There was no traffic in the eligible intervals, so success and burn are unavailable.':q.state==='not_applicable'?'No intervals are eligible yet.':'There is not enough measured traffic to calculate success or burn.'}</p>}<dl className="slo-metrics">
      <div>
      <dt>Measured success</dt>
      <dd>{p.attainment===null?'—':`${compactPercent(p.attainment)}%`}</dd>
      </div>
      <div>
      <dt>Remaining error budget</dt>
      <dd>{p.remaining_budget===null?'—':`${decimal(p.remaining_budget)} events`}</dd>
      </div>
      <div>
      <dt>Burn</dt>
      <dd>{p.burn===null?'—':`${decimal(p.burn)}×`}</dd>
      </div>
      <div>
      <dt>Coverage</dt>
      <dd>{q.coverage===null?'—':`${compactPercent(q.coverage)}%`}</dd>
      </div>
      </dl>
      {p.provisional&&<p className="slo-qualification">{causes.length > 0 && <>{causes.join('; ')}. </>}{q.known_empty_intervals > 0 && <>Collection coverage includes intervals that returned no measurement. </>}These results cover observed traffic only{p.observed_state?` and ${p.observed_state==='met'?'meet':'do not meet'} the target for that traffic`:''}; the full window cannot be assessed.</p>}
      <details>
      <summary>Coverage details</summary>
      <p>Collection coverage counts accepted observations and known-empty intervals; it does not measure traffic or success.</p>
      <p>{q.observed_intervals} observed, {q.known_empty_intervals} known empty, {q.missing_intervals} missing, {q.zero_traffic_intervals} zero traffic and {q.expired_intervals} expired of {q.expected_intervals} eligible intervals. Freshness: {q.freshness.replaceAll('_',' ')}.</p>
      <p>Last observation: {date(q.last_observation_at)} · Last receipt: {date(q.latest_receipt_at)}</p>
      </details>
      <details>
      <summary>Exact summary values</summary>
      <p>Attainment: {p.attainment ?? '—'} · Remaining budget: {p.remaining_budget ?? '—'} events · Burn: {p.burn ?? '—'} · Coverage: {q.coverage ?? '—'}</p>
      </details>{!p.provisional&&p.attainment!==null&&<p>Full-window status: {p.state.replaceAll('_',' ')}.</p>}</section>
      <section className="slo-timeline">
      <p>Eligible recent intervals: {date(eligible[0]?.start ?? t.start)} – {date(t.end)}. The summary covers the full objective window; the timeline excludes intervals before eligibility and shows at most {t.max_intervals} recent intervals.</p>{eligible.length>0&&<>
      <ChartBoundary>
      <Suspense fallback={<span className="sr-only" role="status">Opening chart</span>}>
      <Chart chart={chart}/>
      </Suspense>
      </ChartBoundary>
      <details>
      <summary>View exact values</summary>
      <ExactValuesTable rows={rows.slice((page-1)*50,page*50)} page={page} totalRows={rows.length} onPageChange={setPage}/>
      </details>
      </>}</section>
      </>;
}
