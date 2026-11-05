import { Component, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { journeyContentReady } from '../telemetry/client.ts';
import type { AuthContext } from '../auth/client.ts';
import { Button, ChoiceChips, Combobox, Dialog, FormActions, FormSection, PageHeader, Panel, TextField, type ComboboxOption } from '../components/ui/index.ts';
import { createSliClient, type Sli } from '../slis/client.ts';
import { relativeAge, sliVocabulary } from '../slis/vocabulary.ts';
import { OperationalDetailView, OperationalOverviewView } from './OperationalViews.tsx';
import { sloClient, SloFailure, type CreateSlo, type Service, type Slo } from './client.ts';
import './slos.css';

export class ChartBoundary extends Component<{children:ReactNode},{failed:boolean}> {
  state={failed:false};
  static getDerivedStateFromError(){return {failed:true};}
  render(){return this.state.failed?<p role="alert">The chart could not be opened. Exact values remain available.</p>:this.props.children;}
}
const sliClient = createSliClient();
const namePattern = /^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$/;
const suggest = (s: string) => s.normalize('NFKD').replace(/\p{M}/gu, '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0,63).replace(/-+$/g,'');
const date = (s: string | null) => s ? `${new Date(s).toLocaleString(undefined,{dateStyle:'medium',timeStyle:'medium',timeZone:'UTC'})} UTC` : 'Not recorded';
const key = () => crypto.randomUUID();
const message = (e: unknown) => e instanceof Error ? e.message : 'The request could not be completed.';
function navigate(path: string,state:unknown={}) { if (`${location.pathname}${location.search}` !== path) { history.pushState(state, '', path); dispatchEvent(new PopStateEvent('popstate')); } }
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
const validateTarget = (value: string) => percentTarget(value) !== null ? undefined : 'Target must be from 0% up to but not including 100%.';
const validateDays = (value: string) => /^\d+$/.test(value) && Number(value) >= 1 ? undefined : 'Enter a whole number of days.';
// Remaining share after the target, shown with the target's own precision.
function budgetRest(target: string): string {
  if (percentTarget(target) === null) return 'the rest';
  const decimals = (target.split('.')[1] ?? '').length;
  return `${(100 - Number(target)).toFixed(Math.max(1, decimals))}%`;
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
  const [services,setServices]=useState<Service[]>([]);
  const [slis,setSlis]=useState<Sli[]>([]);
  const [slo,setSlo]=useState<Slo|null>(null);
  const [service,setService]=useState<Service|null>(null);
  const [etag,setEtag]=useState('');
  const [loading,setLoading]=useState(true);
  const [busy,setBusy]=useState(false);
  const [error,setError]=useState('');
  const [formError,setFormError]=useState('');
  const [metadata,setMetadata]=useState('');
  const [display,setDisplay]=useState('');
  const [name,setName]=useState('');
  const [nameEdited,setNameEdited]=useState(false);
  const [serviceID,setServiceID]=useState('');
  const [sliID,setSliID]=useState('');
  const [target,setTarget]=useState('99.9');
  const [duration,setDuration]=useState('30');
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
  const loadedPath=useRef('');
  const heading=useRef<HTMLDivElement>(null);
  const editTrigger=useRef<HTMLElement>(null);
  const [editOpen,setEditOpen]=useState(false);
  const canWrite = !context.session?.restricted && ['administrator','editor'].includes(context.session?.role ?? '');
  const path=locationKey.split('?')[0]!;
  const id=path.match(/^\/(?:slos|services)\/([0-9a-f-]{36})$/)?.[1];
  const isService=path.startsWith('/services/');
  const isNew=path==='/slos/new';
  const isList=path==='/slos';
  useEffect(() => () => { generation.current++; }, []);
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
    setBusy(false);
    setError('');
    setEditOpen(false);
    setSlo(null);
    setService(null);
    const run = async () => {
      try {
        if (isList) {
          // The operational overview owns the server-filtered, snapshot-paginated read.
        } else if (isNew) {
          const [availableServices, availableSlis] = await Promise.all([
            allPages(cursor => sloClient.services(cursor, abort.signal)),
            allPages(cursor => sliClient.list(200, cursor, abort.signal)),
          ]);
          if (active) {
            setServices(availableServices); setSlis(availableSlis);
            const wanted = new URLSearchParams(location.search).get('sli');
            if (wanted && availableSlis.some(item => item.id === wanted)) setSliID(wanted);
          }
        } else if (id && isService) {
          const data = await sloClient.service(id, abort.signal);
          if (active) { setService(data.value); setMetadata(data.value.display_name); setEtag(data.etag); }
        } else if (id) {
          const data = await sloClient.get(id, abort.signal);
          if (active) {
            setSlo(data.value); setMetadata(data.value.display_name); setEtag(data.etag);
          }
        }
      } catch (failure) {
        if (active) setError(message(failure));
      } finally {
        if (active) {
          loadedPath.current=path;
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
  }, [path,id,isService,isNew,isList]);
  useEffect(() => {
    if (!loading && loadedPath.current===path && (isNew || isService)) journeyContentReady('management', !!error);
    if (!loading && loadedPath.current===path && id && !isService && error) journeyContentReady('slo.detail', true);
  }, [loading, path, isNew, isService, id, error]);

  useEffect(() => {
    setBound(''); setBoundError(''); setHistorical(false);
    if (!sliID) return;
    const abort = new AbortController();
    void sliClient.observations(sliID, 1, '', abort.signal)
      .then(result => setBound(result.earliest_queryable_interval_start))
      .catch(() => { if (!abort.signal.aborted) setBoundError('Measurement history could not be loaded. Try again.'); });
    return () => abort.abort();
  }, [sliID, boundRevision]);

  const selectedSli = slis.find(item => item.id === sliID);
  const seconds = Number(duration) * 86400;
  const historyStart = useMemo(() => {
    if (!bound || !selectedSli || !Number.isSafeInteger(seconds) || seconds <= 0) return '';
    const cadence = selectedSli.polling_interval_seconds * 1000;
    const end = Math.floor(Date.now() / cadence) * cadence;
    const start = Math.max(Date.parse(bound), end - seconds * 1000);
    return Number.isFinite(start) && start < end ? new Date(start).toISOString() : '';
  }, [bound, selectedSli, seconds]);
  const frozen = Boolean(sloIntent.current?.uncertain);
  const serviceFrozen = Boolean(serviceIntent.current?.uncertain);

  async function createService(event?: { preventDefault(): void }) {
    event?.preventDefault();
    setFormError('');
    if (!namePattern.test(serviceName) || !serviceDisplay.trim()) {
      setFormError('Enter a display name and a lowercase service identifier.');
      return;
    }
    const body = JSON.stringify({ name: serviceName, display_name: serviceDisplay.trim() });
    if (!serviceIntent.current || serviceIntent.current.body !== body) {
      serviceIntent.current = { body, key: key(), uncertain: false };
    }
    const intent = serviceIntent.current;
    const current = generation.current;
    setBusy(true);
    try {
      const result = await sloClient.createService(JSON.parse(body), context, intent.key);
      if (serviceIntent.current === intent) serviceIntent.current = null;
      if (current !== generation.current) return;
      setServices(items => [...items, result]);
      setServiceID(result.id); setAddingService(false); setServiceDisplay(''); setServiceName('');
    } catch (failure) {
      if (failure instanceof SloFailure && failure.uncertain && serviceIntent.current === intent) intent.uncertain = true;
      if (current === generation.current) setFormError(message(failure));
    } finally {
      if (current === generation.current) setBusy(false);
    }
  }

  async function create(event: FormEvent) {
    event.preventDefault();
    setFormError('');
    let intent = sloIntent.current;
    if (!intent?.uncertain) {
      const targetFraction = percentTarget(target);
      const problem = !display.trim() ? 'Enter a display name.'
        : !namePattern.test(name) ? 'Use lowercase letters, numbers and hyphens for the SLO identifier.'
        : !serviceID ? 'Select a service.'
        : !selectedSli ? 'Select an SLI.'
        : targetFraction === null ? 'Target must be from 0% up to but not including 100%.'
        : !/^\d+$/.test(duration) || !Number.isSafeInteger(seconds) || seconds < 1 || seconds > 2147483647
          ? 'Enter a whole number of days within the supported limit.'
        : seconds % selectedSli.polling_interval_seconds !== 0
          ? `The window must be a multiple of the ${selectedSli.polling_interval_seconds}-second collection interval.`
        : historical && !historyStart ? 'Measurement history is unavailable. Retry loading history before including it.' : '';
      if (problem) {
        // The target chips already announce an invalid custom value; focus it rather than repeating the alert.
        const fieldError = problem.startsWith('Target must') && document.getElementById('slo-target-error');
        if (fieldError) { setFormError(''); document.querySelector<HTMLElement>('[aria-controls="slo-target-custom"]')?.focus(); return; }
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
      if (!intent || intent.body !== body) intent = { body, key: key(), uncertain: false };
      sloIntent.current = intent;
    }
    const current = generation.current;
    setBusy(true);
    try {
      const result = await sloClient.create(JSON.parse(intent.body) as CreateSlo, context, intent.key);
      if (sloIntent.current === intent) sloIntent.current = null;
      if (current === generation.current) {
        // The next New SLO starts empty; carrying this SLO's service and SLI over invites near-duplicates.
        // Only the form that submitted resets: a response arriving after navigation must not erase a newer draft.
        setDisplay(''); setName(''); setNameEdited(false); setServiceID(''); setSliID(''); setTarget('99.9'); setDuration('30'); setHistorical(false); setFormError('');
        navigate(`/slos/${result.id}`);
      }
    } catch (failure) {
      if (failure instanceof SloFailure && failure.uncertain && sloIntent.current === intent) intent.uncertain = true;
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
      setBusy(true); setError('');
    try {
      if (isService) {
        const result = await sloClient.service(id);
        if (current !== generation.current) return;
        setService(result.value); setMetadata(result.value.display_name); setEtag(result.etag);
      } else {
        const result = await sloClient.get(id);
        if (current !== generation.current) return;
        setSlo(result.value); setMetadata(result.value.display_name); setEtag(result.etag);
      }
    } catch (failure) {
      if (current === generation.current) {
        if (failure instanceof SloFailure && (failure.status === 401 || failure.status === 403)) {
          setSlo(null); setService(null); setError(message(failure));
        } else setError(`${message(failure)} Previously shown values may be stale.`);
      }
    } finally {
      if (current === generation.current) setBusy(false);
    }
  }

  async function saveMetadata(event: FormEvent): Promise<boolean> {
    event.preventDefault();
    if (!id) return false;
    if (!metadata.trim()) { setFormError('Enter a display name.'); return false; }
    const current = generation.current;
    setBusy(true); setFormError('');
    try {
      const result = isService
        ? await sloClient.updateService(id, metadata.trim(), etag, context)
        : await sloClient.update(id, metadata.trim(), etag, context);
      if (current !== generation.current) return false;
      setEtag(result.etag);
      if (isService) setService(result.value as Service); else setSlo(result.value as Slo);
      return true;
    } catch (failure) {
      if (current === generation.current) setFormError(message(failure));
      return false;
    } finally {
      if (current === generation.current) setBusy(false);
    }
  }
  const serviceOptions: ComboboxOption[] = services.map(item => ({ id: item.id, label: item.display_name, keywords: item.name, tone: 'none', status: 'Service', detail: item.name }));
  const sliOptions: ComboboxOption[] = slis.map(item => {
    const vocabulary = sliVocabulary(item);
    const linked = item.linked_slos?.total_count ?? 0;
    const detail = [item.datasource_display_name ?? 'datasource', `${item.polling_interval_seconds} s cadence`, linked ? `${linked} SLO${linked === 1 ? '' : 's'} already` : 'no SLO yet', item.last_observation_at ? `last sample ${relativeAge(item.last_observation_at)}` : vocabulary.reason].join(' · ');
    return { id: item.id, label: item.display_name, keywords: `${item.name} ${item.datasource_display_name ?? ''}`, tone: vocabulary.tone, status: vocabulary.label, detail };
  });
  const title=isList?'SLOs':isNew?'New SLO':isService?(service?.display_name??'Service details'):(slo?.display_name??'SLO details');
  return <div className="slos-page" ref={heading}>
      {isNew&&<PageHeader title="New SLO" summary="Name the objective, pick an indicator, set a target." breadcrumb={{label:'SLOs',href:'/slos',current:'New SLO'}}/>}
      {id&&isService&&<div className="slo-title">
      <Heading title={title} list={isList}/><Button variant="outline" onClick={()=>void refresh()} pending={busy}>Refresh</Button></div>}
    {loading&&!isNew&&<span className="sr-only" role="status">Opening {isService?'service':'SLOs'}</span>}{error&&<p className="slo-error" role="alert">{error}</p>}
    {!loading&&isList&&<OperationalOverviewView locationKey={locationKey} canWrite={canWrite} onCreate={()=>navigate('/slos/new')} onLocationChange={navigate}/>}
    {isNew&&(!canWrite?<p>Editor or administrator access is required to create SLOs.</p>:<>
      {!loading&&slis.length===0&&!frozen?<div className="slo-empty">
      <p>Create an SLI before creating an objective.</p>
      <a href="/slis" data-app-route>Go to SLIs</a>
      </div>:<form className="ui-form slo-form" onSubmit={e=>void create(e)} noValidate>
      {loading&&<p role="status" className="slo-choice-loading">Loading services and SLIs. You can enter the objective while they load.</p>}
      <fieldset disabled={frozen||busy} className="slo-fields">
      <FormSection step={1} title="Identity" hint="How it appears in lists and who owns it">
      <div className="ui-form-grid">
      <TextField id="slo-display" label="Display name" value={display} placeholder="e.g. Checkout availability" onChange={e=>{setDisplay(e.target.value);
      if(!nameEdited)setName(suggest(e.target.value));}} hint="Shown in lists and on the detail page."/>
      <TextField id="slo-name" label="Identifier" value={name} autoComplete="off" onChange={e=>{setNameEdited(true);
      setName(e.target.value);}} hint="Suggested from the display name. Lowercase letters, numbers and hyphens; fixed after creation."/>
      </div>
      <Combobox id="slo-service" label="Service" options={serviceOptions} value={serviceID||null} onChange={value=>{setServiceID(value??'');setFormError('');}} disabled={loading||serviceFrozen} placeholder={loading?'Loading services…':'Type a service name, or a new one to create it…'} hint="Services group SLOs by owner. Pick an existing one, or type a new name and create it without leaving this form." noMatch={{text:term=>`No service matches “${term}”.`,action:{label:term=>`Create service “${term}”`,onSelect:term=>{setServiceDisplay(term.trim());setServiceName(suggest(term));setServiceNameEdited(false);setAddingService(true);setFormError('');}}}}/>
      {addingService&&<div className="slo-new-service" role="group" aria-label="New service" onKeyDown={e=>{if(e.key==='Enter'&&e.target instanceof HTMLInputElement){e.preventDefault();void createService();}}}>
      <div className="ui-form-grid">
      <TextField id="service-display" label="Service display name" value={serviceDisplay} disabled={serviceFrozen} onChange={e=>{setServiceDisplay(e.target.value);
      if(!serviceNameEdited)setServiceName(suggest(e.target.value));}}/>
      <TextField id="service-name" label="Service identifier" value={serviceName} disabled={serviceFrozen} autoComplete="off" onChange={e=>{setServiceNameEdited(true);
      setServiceName(e.target.value);}} hint="Lowercase letters, numbers and hyphens."/>
      </div>
      <div className="ui-form-actions__buttons">
      <Button variant="outline" disabled={serviceFrozen} onClick={()=>{setAddingService(false);
      setFormError('');}}>Cancel</Button>
      <Button pending={busy} onClick={()=>void createService()}>{serviceFrozen?'Retry same service request':'Create service'}</Button>
      </div>
      </div>}
      </FormSection>
      <FormSection step={2} title="Indicator" hint="What are we measuring?">
      <Combobox id="slo-sli" label="SLI" options={sliOptions} value={sliID||null} onChange={value=>{setSliID(value??'');setFormError('');}} disabled={loading} placeholder={loading?'Loading SLIs…':'Type an SLI name or its datasource…'} hint="The objective is evaluated on this SLI’s good ÷ total at its cadence." badge={option=>slis.find(item=>item.id===option.id)?.name}/>
      </FormSection>
      <FormSection step={3} title="Objective" hint="Target over a rolling window">
      <div className="ui-form-grid">
      <ChoiceChips id="slo-target" label="Target" value={target} onChange={value=>{setTarget(value);setFormError('');}} options={['99','99.5','99.9','99.95','99.99'].map(value=>({value,label:`${value}%`}))} custom={{unit:'%',placeholder:'e.g. 99.7',format:value=>`${value}%`,validate:validateTarget,inputMode:'decimal'}} hint={`Share of events that must be good over the window. Error budget is the rest: ${budgetRest(target)}.`}/>
      <ChoiceChips id="slo-window" label="Rolling window" value={duration} onChange={value=>{setDuration(value);setFormError('');}} options={[['7','7 days'],['28','28 days'],['30','30 days'],['90','90 days']].map(([value,label])=>({value:value!,label:label!}))} custom={{unit:'days',placeholder:'e.g. 14',format:value=>`${value} days`,validate:validateDays,inputMode:'numeric'}} hint={`The window rolls forward continuously; every evaluation covers the last ${/^\d+$/.test(duration)?`${duration} days`:'window'}.`}/>
      </div>
      {sliID&&<div className="slo-history">
      <label className="ui-form-check">
      <input type="checkbox" checked={historical} disabled={!historyStart} onChange={e=>setHistorical(e.target.checked)}/><span>Apply this target to earlier SLI measurements{historyStart?` from ${date(historyStart)}`:''}<span className="slo-history__note">Uses measurements already collected for this SLI. Missing intervals remain unknown.</span></span></label>
      {boundError&&<p role="alert" className="slo-error">{boundError}</p>}
      {boundError&&<Button variant="outline" onClick={() => setBoundRevision(value => value + 1)}>Retry loading history</Button>}
      </div>}
      </FormSection>
      <FormSection step={4} title="Alerting" hint="Not available yet · arrives with alert policies" muted>
      <p className="ui-form-note">Burn-rate alert policies (fast, medium and slow burn) will be selectable here. Nothing is configured today.</p>
      </FormSection>
      </fieldset>{formError&&<p role="alert" className="slo-error">{formError}</p>}
      <FormActions note="Evaluation starts immediately from collected measurements. You can rename the SLO later.">
      <Button variant="outline" disabled={frozen} onClick={()=>navigate('/slos')}>Cancel</Button>
      <Button type="submit" disabled={loading} pending={busy}>{frozen?'Retry same SLO request':'Create SLO'}</Button>
      </FormActions>{frozen&&<p className="slo-muted">Check the SLO list before retrying. The original request and key are retained.</p>}
      </form>}</>)}
    {!loading&&isService&&service&&<>
      <Panel heading="Service">
      <p>{service.display_name}</p>
      <p className="slo-muted">Identifier: {service.name}</p>
      </Panel>{canWrite&&<form onSubmit={e=>void saveMetadata(e)}>
      <TextField id="service-rename" label="Display name" value={metadata} onChange={e=>setMetadata(e.target.value)}/>
      <Button type="submit" pending={busy}>Save display name</Button>
      </form>}{formError&&<p role="alert">{formError}</p>}</>}
    {!isService&&!isNew&&id&&!error&&<>
      <OperationalDetailView id={id} definition={slo?.id===id?slo:undefined} locationKey={locationKey} onLocationChange={navigate} onSessionFailure={()=>{setSlo(null);setError('Your session can no longer access this SLO. Sign in again to continue.');}} onEdit={canWrite&&slo?.id===id?()=>{editTrigger.current=document.activeElement instanceof HTMLElement?document.activeElement:null;setFormError('');setEditOpen(true);}:undefined}/>
      {slo?.id===id&&<Dialog open={editOpen} onClose={()=>setEditOpen(false)} title="Edit SLO" triggerRef={editTrigger}>
        <form className="slo-edit-form" onSubmit={e=>{void saveMetadata(e).then(saved=>{if(saved)setEditOpen(false);});}}>
        <p className="slo-muted">Identifier <code>{slo.name}</code> · revision {slo.definition_revision} · activated {date(slo.activated_at)}{slo.historical_requested_at&&` · applied to earlier measurements on ${date(slo.historical_requested_at)}`}. Target and window changes arrive with definition revisions.</p>
        <TextField id="slo-rename" label="Display name" value={metadata} onChange={e=>{setMetadata(e.target.value);setFormError('');}} error={formError||undefined}/>
        <div className="slo-actions"><Button variant="outline" onClick={()=>setEditOpen(false)}>Cancel</Button><Button type="submit" pending={busy}>Save display name</Button></div>
        </form>
      </Dialog>}
      </>}
  </div>;
}
