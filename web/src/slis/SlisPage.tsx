import { useEffect, useMemo, useRef, useState, type FormEvent, type RefObject } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { createAgentClient, type Agent } from '../agents/client.ts';
import { SliRecentStrip } from '../components/charts/SliRecentStrip.tsx';
import { Button, MeasurementChoiceCards, Panel, QueryEditor, SelectField, StatusBadge, TextField } from '../components/ui/index.ts';
import { createDatasourceClient, type Datasource } from '../datasources/client.ts';
import { createSliClient, SliFailure, type Sli, type SliCreateRequest, type SliObservation, type SliObservationPage } from './client.ts';
import './slis.css';

const slis = createSliClient();
const datasources = createDatasourceClient();
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const SLI_NAME = /^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$/;
const safeError = (error: unknown) => error instanceof SliFailure ? error.message : 'SLI management is temporarily unavailable.';
const roleCanWrite = (context: AuthContext) => context.session?.role === 'administrator' || context.session?.role === 'editor';

function suggestName(value: string) {
  return value.normalize('NFKD').replace(/\p{M}/gu, '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 63).replace(/-+$/g, '');
}

type Route = { kind: 'list' | 'create' | 'detail'; id?: string };
type CreateDraft = { name: string; displayName: string; datasourceID: string; goodQuery: string; totalQuery: string };
type CreateIntent = { body: string; key: string };
const emptyDraft: CreateDraft = { name: '', displayName: '', datasourceID: '', goodQuery: '', totalQuery: '' };

async function loadSources(signal?: AbortSignal): Promise<Datasource[]> {
  const items: Datasource[] = [];
  const seen = new Set<string>();
  let cursor = '';
  do {
    const page = await datasources.list(200, cursor, signal);
    items.push(...page.items);
    cursor = page.next_cursor;
    if (cursor && seen.has(cursor)) throw new Error('Datasource pagination did not advance.');
    if (cursor) seen.add(cursor);
  } while (cursor);
  return items;
}

function routeFromPath(pathname: string): Route {
  if (pathname === '/slis/new') return { kind: 'create' };
  const match = /^\/slis\/([0-9a-f-]{36})$/.exec(pathname);
  if (match?.[1] && UUID.test(match[1])) return { kind: 'detail', id: match[1] };
  return { kind: 'list' };
}

function navigate(path: string) {
  if (`${window.location.pathname}${window.location.search}` === path) return;
  window.history.pushState({}, '', path);
  window.dispatchEvent(new PopStateEvent('popstate'));
}

function sliListPath(search: string, datasourceID: string): string {
  const query = new URLSearchParams();
  if (search) query.set('q', search);
  if (datasourceID) query.set('datasource_id', datasourceID);
  return `/slis${query.size ? `?${query}` : ''}`;
}

function PageTitle({ title, subtitle, headingRef, isList = false, parentHref = '/slis' }: { title: string; subtitle?: string; headingRef?: RefObject<HTMLHeadingElement | null>; isList?: boolean; parentHref?: string }) {
  return <div className="sli-page-title"><nav className="app-breadcrumbs" aria-label="Breadcrumb">{isList ? <span aria-current="page">SLIs</span> : <><a href={parentHref} data-app-route>SLIs</a><span aria-hidden="true">/</span><span aria-current="page">{title}</span></>}</nav><h1 ref={headingRef} tabIndex={-1} className="app-page-heading">{title}</h1>{subtitle && <p>{subtitle}</p>}</div>;
}

function InlineState({ label, value, tone = 'neutral' }: { label: string; value: string; tone?: 'positive' | 'attention' | 'neutral' }) {
  return <span className={`sli-state sli-state--${tone}`}><span>{label}</span><strong>{value.replaceAll('_', ' ')}</strong></span>;
}

function outcomeLabel(value: SliObservation['outcome']): string {
  switch (value) {
    case 'observed': return 'Observed';
    case 'complete_empty': return 'Complete empty';
    case 'incomplete': return 'Incomplete';
    case 'failed': return 'Failed';
  }
}

function formatTime(value: string | null): string {
  return value ? new Date(value).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium', timeZone: 'UTC' }) + ' UTC' : 'Not recorded';
}

function shortTime(value: string): string {
  return new Date(value).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short', timeZone: 'UTC' }) + ' UTC';
}

function sliListHealth(item: Sli): 'healthy' | 'warning' | 'no-data' | 'stale' {
  const latest = item.latest_observation;
  if (item.freshness === 'stale') return 'stale';
  if (!latest || item.freshness === 'unknown' || latest.outcome === 'complete_empty' || latest.total_count === '0') return 'no-data';
  if (latest.outcome === 'failed' || latest.outcome === 'incomplete') return 'warning';
  return 'healthy';
}

function makeIdempotencyKey(): string {
  if (typeof window.crypto?.randomUUID === 'function') return window.crypto.randomUUID();
  const random = Math.random().toString(36).slice(2);
  return `sli-${Date.now()}-${random}`;
}

export function SlisPage({ context }: { context: AuthContext }) {
  const agentClient = useRef<ReturnType<typeof createAgentClient> | null>(null);
  if (!agentClient.current) agentClient.current = createAgentClient();
  const [locationKey, setLocationKey] = useState(() => `${window.location.pathname}${window.location.search}`);
  const route = useMemo(() => routeFromPath(new URL(locationKey, window.location.origin).pathname), [locationKey]);
  const [items, setItems] = useState<Sli[]>([]);
  const [sources, setSources] = useState<Datasource[]>([]);
  const [sli, setSli] = useState<Sli | null>(null);
  const [agent, setAgent] = useState<Agent | null>(null);
  const [agentError, setAgentError] = useState(false);
  const [etag, setETag] = useState('');
  const [history, setHistory] = useState<SliObservation[]>([]);
  const [missing, setMissing] = useState<SliObservationPage['missing_intervals']>([]);
  const [earliest, setEarliest] = useState('');
  const [nextCursor, setNextCursor] = useState('');
  const [listNextCursor, setListNextCursor] = useState('');
  const [listSearch, setListSearch] = useState(() => new URL(window.location.href).searchParams.get('q') ?? '');
  const [listDatasourceID, setListDatasourceID] = useState(() => new URL(window.location.href).searchParams.get('datasource_id') ?? '');
  const [draft, setDraft] = useState<CreateDraft>(emptyDraft);
  const [displayName, setDisplayName] = useState('');
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState('');
  const [historyBusy, setHistoryBusy] = useState(false);
  const [historyLoaded, setHistoryLoaded] = useState(false);
  const [listBusy, setListBusy] = useState(false);
  const [formError, setFormError] = useState('');
  const [historyError, setHistoryError] = useState('');
  const [sourceError, setSourceError] = useState('');
  const [sourceLoading, setSourceLoading] = useState(false);
  const [loadRevision, setLoadRevision] = useState(0);
  const nameEdited = useRef(false);
  const routeGeneration = useRef(0);
  const historyRequest = useRef(0);
  const listRequest = useRef(0);
  const listAbort = useRef<AbortController | null>(null);
  const createIntent = useRef<CreateIntent | null>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);
  const currentLocation = useRef(locationKey);
  const focusHeading = useRef(true);
  const canWrite = roleCanWrite(context);

  useEffect(() => {
    const changed = () => {
      const next = `${window.location.pathname}${window.location.search}`;
      if (next === currentLocation.current) return;
      currentLocation.current = next;
      focusHeading.current = true;
      routeGeneration.current++;
      setLoading(true);
      setLocationKey(next);
    };
    window.addEventListener('popstate', changed);
    return () => window.removeEventListener('popstate', changed);
  }, []);

  useEffect(() => {
    const request = new AbortController();
    let active = true;
    const generation = routeGeneration.current;
    const current = () => active && generation === routeGeneration.current;
    setLoading(true);
    setError('');
    setFormError('');
    setHistoryError('');
    setSourceError('');
    setSourceLoading(route.kind === 'list');
    setPending(false);
    setHistoryBusy(false);
    setHistoryLoaded(false);
    historyRequest.current++;
    listRequest.current++;
    listAbort.current?.abort();
    listAbort.current = null;
    setListBusy(false);
    setSli(null);
    setAgent(null);
    setAgentError(false);
    setHistory([]);
    setMissing([]);
    setEarliest('');
    setItems([]);
    setListNextCursor('');
    setSources([]);
    setNextCursor('');
    const load = async () => {
      try {
        if (route.kind === 'list') {
          const filters = new URLSearchParams(new URL(locationKey, window.location.origin).search);
          const search = filters.get('q') ?? '';
          const datasourceID = filters.get('datasource_id') ?? '';
          setListSearch(search);
          setListDatasourceID(datasourceID);
          const page = await slis.list(50, '', request.signal, search, datasourceID);
          if (!current()) return;
          setItems(page.items);
          setListNextCursor(page.next_cursor);
          setLoading(false);
          void loadSources(request.signal).then((value) => { if (current()) setSources(value); }).catch(() => { if (current() && !request.signal.aborted) setSourceError('Datasources could not be loaded. Try again.'); }).finally(() => { if (current()) setSourceLoading(false); });
        } else if (route.kind === 'create') {
          if (!canWrite) throw new Error('Editor or administrator access is required to create an SLI.');
          try {
            const page = await loadSources(request.signal);
            if (!current()) return;
            setSources(page);
            const selected = new URL(window.location.href).searchParams.get('datasource');
            setDraft({ ...emptyDraft, datasourceID: selected ? (page.some((source) => source.id === selected) ? selected : '') : page[0]?.id || '' });
            nameEdited.current = false;
          } catch { if (current() && !request.signal.aborted) setSourceError('Datasources could not be loaded. Try again.'); }
        } else if (route.id) {
          const resource = await slis.get(route.id, request.signal);
          if (!current()) return;
          setSli(resource.sli);
          setDisplayName(resource.sli.display_name);
          setETag(resource.etag);
          if (context.session?.role === 'administrator' && !context.session.restricted) {
            void agentClient.current!.get(resource.sli.agent_id, { signal: request.signal }).then((value) => { if (current()) setAgent(value.agent); }).catch(() => { if (current() && !request.signal.aborted) setAgentError(true); });
          }
          const historyID = ++historyRequest.current;
          setHistoryBusy(true);
          void fetchHistory(route.id, historyID, generation, '', request.signal);
          void loadSources(request.signal).then((value) => { if (current()) setSources(value); }).catch(() => { if (current() && !request.signal.aborted) setSourceError('Datasource names could not be loaded.'); });
        }
      } catch (failure) {
        if (current() && !request.signal.aborted) setError(safeError(failure));
      } finally {
        if (current()) setLoading(false);
      }
    };
    void load();
    return () => { active = false; request.abort(); };
  }, [locationKey, route.kind, route.id, canWrite, context.session?.role, context.session?.restricted, loadRevision]);

  useEffect(() => {
    if (!loading && focusHeading.current && titleRef.current) {
      titleRef.current.focus();
      focusHeading.current = false;
    }
  }, [loading, locationKey]);

  function updateDraft(key: keyof CreateDraft, value: string) {
    setDraft((current) => ({ ...current, [key]: value }));
    setFormError('');
  }

  async function fetchHistory(id: string, requestID: number, generation: number, cursor = '', signal?: AbortSignal) {
    const current = () => generation === routeGeneration.current && requestID === historyRequest.current && !signal?.aborted;
    try {
      const page = await slis.observations(id, 50, cursor, signal);
      if (!current()) return;
      setHistory((previous) => cursor ? [...previous, ...page.items] : page.items);
      setMissing((previous) => cursor ? [...previous, ...page.missing_intervals] : page.missing_intervals);
      setEarliest(page.earliest_queryable_interval_start);
      setNextCursor(page.next_cursor);
      setHistoryLoaded(true);
    } catch (failure) {
      if (current()) setHistoryError(safeError(failure));
    } finally {
      if (current()) setHistoryBusy(false);
    }
  }

  async function submitCreate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFormError('');
    const errors: string[] = [];
    if (!SLI_NAME.test(draft.name)) errors.push('Use lowercase letters, numbers, or hyphens for the identifier, starting and ending with a letter or number.');
    if (!draft.displayName.trim()) errors.push('Enter a display name.');
    if (!UUID.test(draft.datasourceID)) errors.push('Choose an existing Prometheus datasource.');
    if (!draft.goodQuery.trim()) errors.push('Enter a good count query.');
    if (!draft.totalQuery.trim()) errors.push('Enter a total count query.');
    if (errors.length) { setFormError(errors.join(' ')); return; }
    const input: SliCreateRequest = {
      name: draft.name,
      display_name: draft.displayName.trim(),
      datasource_id: draft.datasourceID,
      measurement_kind: 'event_counts',
      component_mode: 'interval_count',
      good_query: draft.goodQuery.trim(),
      total_query: draft.totalQuery.trim(),
    };
    const body = JSON.stringify(input);
    if (createIntent.current?.body !== body) createIntent.current = { body, key: makeIdempotencyKey() };
    setPending(true);
    const generation = routeGeneration.current;
    try {
      const created = await slis.create(input, context, createIntent.current.key);
      if (generation !== routeGeneration.current) return;
      createIntent.current = null;
      navigate(`/slis/${created.id}`);
    } catch (failure) {
      if (generation !== routeGeneration.current) return;
      setFormError(safeError(failure));
      titleRef.current?.focus();
    } finally { if (generation === routeGeneration.current) setPending(false); }
  }

  async function refreshDetail() {
    if (!route.id) return;
    const generation = routeGeneration.current;
    const historyID = ++historyRequest.current;
    setPending(true);
    setHistoryBusy(true);
    setError('');
    setHistoryError('');
    try {
      const resource = await slis.get(route.id);
      if (generation !== routeGeneration.current) return;
      setSli(resource.sli);
      setDisplayName((current) => current === sli?.display_name ? resource.sli.display_name : current);
      setETag(resource.etag);
      void loadSources().then((value) => { if (generation === routeGeneration.current) { setSources(value); setSourceError(''); } }).catch(() => { if (generation === routeGeneration.current) setSourceError('Datasource names could not be loaded. Refresh to try again.'); });
      if (context.session?.role === 'administrator' && !context.session.restricted) {
        void agentClient.current!.get(resource.sli.agent_id).then((value) => { if (generation === routeGeneration.current) { setAgent(value.agent); setAgentError(false); } }).catch(() => { if (generation === routeGeneration.current) setAgentError(true); });
      }
      await fetchHistory(route.id, historyID, generation);
    } catch (failure) {
      if (generation === routeGeneration.current) setError(safeError(failure));
      if (generation === routeGeneration.current && historyID === historyRequest.current) {
        setHistoryError('Observation history could not be refreshed.');
        setHistoryBusy(false);
      }
    }
    finally { if (generation === routeGeneration.current) setPending(false); }
  }

  async function loadMoreHistory() {
    if (!route.id || !nextCursor) return;
    const generation = routeGeneration.current;
    const historyID = ++historyRequest.current;
    setHistoryBusy(true);
    setHistoryError('');
    await fetchHistory(route.id, historyID, generation, nextCursor);
  }

  async function loadMoreSLIs() {
    if (!listNextCursor) return;
    const generation = routeGeneration.current;
    const requestID = ++listRequest.current;
    const controller = new AbortController();
    listAbort.current?.abort();
    listAbort.current = controller;
    setListBusy(true);
    try {
      const page = await slis.list(50, listNextCursor, controller.signal, listSearch, listDatasourceID);
      if (generation !== routeGeneration.current || requestID !== listRequest.current) return;
      setItems((current) => [...current, ...page.items]);
      setListNextCursor(page.next_cursor);
    } catch (failure) { if (generation === routeGeneration.current && requestID === listRequest.current && !controller.signal.aborted) setError(safeError(failure)); }
    finally { if (generation === routeGeneration.current && requestID === listRequest.current) setListBusy(false); }
  }

  async function updateListFilters(search: string, datasourceID: string) {
    setListSearch(search);
    setListDatasourceID(datasourceID);
    const query = new URLSearchParams();
    if (search) query.set('q', search);
    if (datasourceID) query.set('datasource_id', datasourceID);
    const path = `/slis${query.size ? `?${query}` : ''}`;
    window.history.replaceState({}, '', path);
    currentLocation.current = path;
    const generation = routeGeneration.current;
    const requestID = ++listRequest.current;
    const controller = new AbortController();
    listAbort.current?.abort();
    listAbort.current = controller;
    setListBusy(true);
    setError('');
    try {
      const page = await slis.list(50, '', controller.signal, search, datasourceID);
      if (generation !== routeGeneration.current || requestID !== listRequest.current) return;
      setItems(page.items);
      setListNextCursor(page.next_cursor);
    } catch (failure) {
      if (generation === routeGeneration.current && requestID === listRequest.current && !controller.signal.aborted) setError(safeError(failure));
    } finally {
      if (generation === routeGeneration.current && requestID === listRequest.current) setListBusy(false);
    }
  }

  async function saveMetadata(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!sli || !canWrite) return;
    if (!displayName.trim()) { setFormError('Enter a display name.'); return; }
    const generation = routeGeneration.current;
    setPending(true);
    setError('');
    try {
      const updated = await slis.updateMetadata(sli.id, etag, displayName, context);
      if (generation !== routeGeneration.current) return;
      setSli(updated);
      setETag(`"sli:${updated.id}:${updated.version}"`);
      setFormError('');
    } catch (failure) { if (generation === routeGeneration.current) setError(safeError(failure)); }
    finally { if (generation === routeGeneration.current) setPending(false); }
  }

  if (loading) return <><PageTitle isList={route.kind === 'list'} title={route.kind === 'detail' ? 'SLI details' : route.kind === 'create' ? 'Create SLI' : 'SLIs'} subtitle={route.kind === 'list' ? 'Service level indicators collected from assigned agents.' : route.kind === 'create' ? 'Define good and total interval counts from a Prometheus datasource.' : undefined} /><span className="sr-only" role="status">Opening SLIs</span></>;
  if (error && !sli && route.kind === 'detail') return <><PageTitle title="SLI unavailable" headingRef={titleRef} /><Panel heading="SLI could not be loaded"><p role="alert">{error}</p><Button variant="outline" onClick={() => navigate('/slis')}>Back to SLIs</Button></Panel></>;

  if (route.kind === 'create') {
    const options = [{ value: '', label: 'Select a datasource', disabled: true }, ...sources.map((source) => ({ value: source.id, label: `${source.display_name} · ${source.polling_interval_seconds}s cadence` }))];
    return <>
      <PageTitle title="Create SLI" subtitle="Define good and total interval counts from a Prometheus datasource." headingRef={titleRef} parentHref={sliListPath(listSearch, listDatasourceID)} />
      {error && <p className="sli-error" role="alert">{error}</p>}
      {!canWrite ? <Panel heading="Access required"><p>Editor or administrator access is required to create SLIs.</p></Panel>
        : sourceError ? <div className="sli-empty"><p role="alert">{sourceError}</p><Button variant="outline" onClick={() => setLoadRevision((revision) => revision + 1)}>Try again</Button></div>
        : sources.length === 0 ? <div className="sli-empty"><p>{context.session?.role === 'administrator' ? 'Add a Prometheus datasource to create an SLI.' : 'Ask an administrator to add a Prometheus datasource before creating an SLI.'}</p>{context.session?.role === 'administrator' && <Button onClick={() => navigate('/datasources/new?return=slis')}>Add datasource</Button>}<Button variant="outline" onClick={() => navigate('/slis')}>Back to SLIs</Button></div>
          : <form className="sli-form" onSubmit={(event) => void submitCreate(event)} noValidate>
            <Panel heading="Definition">
              <div className="sli-form-grid">
                <TextField id="sli-display-name" label="Display name" value={draft.displayName} onChange={(event) => { const value = event.target.value; setDraft((current) => ({ ...current, displayName: value, ...(!nameEdited.current ? { name: suggestName(value) } : {}) })); setFormError(''); }} />
                <TextField id="sli-name" label="SLI name" value={draft.name} onChange={(event) => { nameEdited.current = true; updateDraft('name', event.target.value); }} hint="Suggested from the display name. Use lowercase letters, numbers, or hyphens; start and end with a letter or number." autoComplete="off" />
                <SelectField id="sli-datasource" label="Datasource" options={options} value={draft.datasourceID} onChange={(event) => updateDraft('datasourceID', event.target.value)} hint="Collection runs only on the agent assigned to this datasource." />
                <div className="sli-measurement-picker"><MeasurementChoiceCards value="event_counts" onChange={() => undefined} /></div>
                <p className="measurement-units"><strong>Query values represent:</strong> event counts over each completed interval. Every event contributes one count, so busy intervals carry more weight than quiet intervals.</p>
                <QueryEditor id="sli-good-query" label="Good count query" value={draft.goodQuery} onChange={(value) => updateDraft('goodQuery', value)} />
                <QueryEditor id="sli-total-query" label="Total count query" value={draft.totalQuery} onChange={(value) => updateDraft('totalQuery', value)} />
              </div>
              <p className="sli-form-note">Each query must return one aggregate count for an interval. Create saves a result from recent data already in your datasource, then continues at the datasource’s frequency. The first interval can precede creation.</p>
              {formError && <p className="sli-error" role="alert">{formError}</p>}
              <div className="sli-actions"><Button variant="outline" onClick={() => navigate(sliListPath(listSearch, listDatasourceID))}>Cancel</Button><Button type="submit" pending={pending}>Create SLI</Button></div>
            </Panel>
          </form>}
    </>;
  }

  if (route.kind === 'detail' && sli) {
    const datasource = sources.find((item) => item.id === sli.datasource_id);
    const latest = sli.latest_observation;
    const activatedAt = sli.activated_at;
    return <>
      <div className="sli-detail-heading"><PageTitle title={sli.display_name} subtitle={sli.name} headingRef={titleRef} parentHref={sliListPath(listSearch, listDatasourceID)} /><div className="sli-actions"><Button variant="outline" onClick={() => navigate(sliListPath(listSearch, listDatasourceID))}>Back to SLIs</Button><Button variant="outline" onClick={() => void refreshDetail()} pending={pending}>Refresh</Button></div></div>
      {error && <p className="sli-error" role="alert">{error}</p>}
      {sourceError && <p className="sli-error" role="alert">{sourceError}</p>}
      {agentError && <p className="sli-error" role="alert">Agent name could not be refreshed. Refresh to try again.</p>}
      <Panel heading="Latest result" className="sli-detail-section">
          {latest ? <>
            <p className="sli-interval">{formatTime(latest.interval_start)} – {formatTime(latest.interval_end)}</p>
            <p className="sli-outcome">{outcomeLabel(latest.outcome)}</p>
            {latest.outcome === 'observed' ? <div className="sli-counts"><div><span>Good</span><strong>{latest.good_count}</strong></div><div><span>Total</span><strong>{latest.total_count}</strong></div></div>
              : latest.outcome === 'incomplete' ? <p>Missing component: {latest.missing_components.join(', ')}</p>
                : latest.outcome === 'failed' ? <p>Safe error code: {latest.error_code}</p>
                  : <p>Queries completed without a count value.</p>}
          </> : <><p>No observation has been recorded yet; this does not mean the counts are zero. Almanac saves the most recent complete interval, allowing {sli.settling_delay_seconds} seconds for the datasource to finish reporting. This result can cover time before the SLI was created.</p><p>{sli.agent_reachability === 'unreachable' || sli.agent_reachability === 'disabled' ? 'The assigned agent is unavailable.' : sli.activation_state === 'blocked' || sli.collection_progress === 'blocked' ? 'Collection is blocked. Check the agent and datasource.' : 'The first query runs after the assigned agent receives its configuration and is ready.'}</p></>}
          <p className="sli-muted">Last attempt: {formatTime(sli.last_attempt_at)} · Last observation: {formatTime(sli.last_observation_at)}</p>
        </Panel>
      <Panel heading="Collection" className="sli-detail-section">
        <div className="sli-state-list">
          <InlineState label="Activation" value={sli.activation_state} tone={sli.activation_state === 'active' ? 'positive' : sli.activation_state === 'blocked' ? 'attention' : 'neutral'} />
          <InlineState label="Agent reachability" value={sli.agent_reachability} tone={sli.agent_reachability === 'connected' ? 'positive' : sli.agent_reachability === 'unreachable' || sli.agent_reachability === 'disabled' ? 'attention' : 'neutral'} />
          <InlineState label="Collection progress" value={sli.collection_progress} tone={sli.collection_progress === 'running' ? 'positive' : sli.collection_progress === 'blocked' ? 'attention' : 'neutral'} />
          <InlineState label="Freshness" value={sli.freshness} tone={sli.freshness === 'fresh' ? 'positive' : sli.freshness === 'stale' ? 'attention' : 'neutral'} />
        </div>
        <dl className="sli-facts"><div><dt>Datasource</dt><dd>{sourceError && !datasource ? 'Datasource name unavailable. Refresh to try again.' : <a href={`/datasources/${sli.datasource_id}`} data-app-route>{datasource?.display_name ?? 'View datasource'}</a>}</dd></div><div><dt>Assigned agent</dt><dd>{context.session?.role !== 'administrator' || context.session.restricted ? 'Agent details are available to administrators.' : agentError && agent?.id !== sli.agent_id ? 'Agent name unavailable. Refresh to try again.' : <a href={`/agents?agent=${sli.agent_id}`} data-app-route>{agent?.id === sli.agent_id ? agent.display_name : 'View assigned agent'}</a>}</dd></div><div><dt>Interval</dt><dd>{sli.polling_interval_seconds} seconds</dd></div><div><dt>Source settling delay</dt><dd>{sli.settling_delay_seconds} seconds</dd></div><div><dt>Collection activated</dt><dd>{activatedAt === null ? 'Not activated yet' : formatTime(activatedAt)}</dd></div><div><dt>Next scheduled query</dt><dd>{formatTime(sli.next_expected_execution_at)}</dd></div></dl>
        <p className="sli-muted">Each result covers a completed {sli.polling_interval_seconds}-second interval. The query runs after a {sli.settling_delay_seconds}-second delay to allow source data to settle.</p>
      </Panel>
      <Panel heading="Observation history" className="sli-history-panel">
        {historyError && <p className="sli-error" role="alert">{historyError}</p>}
        {historyBusy && <span className="sr-only" role="status">Refreshing observation history</span>}
        {earliest && <p className="sli-muted">History before {formatTime(earliest)} is unavailable.</p>}
        {missing.length > 0 && <div><strong>Missing intervals</strong><ul>{missing.map((range) => <li key={range.interval_start}>{formatTime(range.interval_start)} – {formatTime(range.interval_end)}</li>)}</ul></div>}
        {history.length === 0 ? historyLoaded && !historyError && <p>No observations have been recorded in this range.</p> : <div className="sli-table-scroll" role="region" aria-label="Observation history" tabIndex={0}><table className="sli-table"><thead><tr><th scope="col">Interval start (UTC)</th><th scope="col">Interval end (UTC)</th><th scope="col">Outcome</th><th scope="col">Good count</th><th scope="col">Total count</th><th scope="col">Details</th></tr></thead><tbody>{history.map((row) => <tr key={row.observation_id}><td className="sli-mono">{new Date(row.interval_start).toISOString()}</td><td className="sli-mono">{new Date(row.interval_end).toISOString()}</td><td>{outcomeLabel(row.outcome)}</td><td className="sli-mono">{row.good_count ?? '—'}</td><td className="sli-mono">{row.total_count ?? '—'}</td><td>{row.outcome === 'incomplete' ? `Missing ${row.missing_components.join(', ')}` : row.error_code ?? '—'}</td></tr>)}</tbody></table></div>}
        {nextCursor && <div className="sli-actions"><Button variant="outline" onClick={() => void loadMoreHistory()} pending={historyBusy}>Load more intervals</Button></div>}
      </Panel>
      <details className="sli-settings"><summary>Settings and technical details</summary>
        <dl className="sli-queries"><div><dt>Good count query</dt><dd><code>{sli.good_query}</code></dd></div><div><dt>Total count query</dt><dd><code>{sli.total_query}</code></dd></div></dl>
        <dl className="sli-facts"><div><dt>SLI ID</dt><dd className="sli-mono">{sli.id}</dd></div><div><dt>Assigned agent ID</dt><dd className="sli-mono">{sli.agent_id}</dd></div><div><dt>Measurement revision</dt><dd>{sli.measurement_revision}</dd></div><div><dt>Resource version</dt><dd>{sli.version}</dd></div></dl>
        {canWrite && <form className="sli-metadata-form" onSubmit={(event) => void saveMetadata(event)}><TextField id="sli-edit-display-name" label="Display name" value={displayName} onChange={(event) => { setDisplayName(event.target.value); setFormError(''); }} /><Button type="submit" pending={pending}>Save display name</Button></form>}
        {formError && <p className="sli-error" role="alert">{formError}</p>}
        <p className="sli-form-note">Measurement queries and history are immutable from this screen.</p>
      </details>
    </>;
  }

  const datasourceOptions = [{ value: '', label: 'All datasources' }, ...sources.map((source) => ({ value: source.id, label: source.display_name }))];
  const hasListFilters = listSearch.trim() !== '' || listDatasourceID !== '';
  return <>
    <div className="sli-list-heading"><PageTitle isList title="SLIs" subtitle="Service level indicators collected from assigned agents." headingRef={titleRef} />{canWrite && items.length > 0 && sources.length > 0 && !sourceError && <Button onClick={() => { setDraft(emptyDraft); createIntent.current = null; navigate('/slis/new'); }}>Add SLI</Button>}</div>
    {error && <p className="sli-error" role="alert">{error}</p>}
    {sourceError && <><p className="sli-error" role="alert">{sourceError}</p><Button variant="outline" onClick={() => setLoadRevision((revision) => revision + 1)}>Try again</Button></>}
    {sourceLoading && <span className="sr-only" role="status">Checking datasources</span>}
    <div className="sli-list-toolbar"><TextField id="sli-list-search" label="Search SLIs" placeholder="Name or datasource" value={listSearch} onChange={(event) => void updateListFilters(event.target.value, listDatasourceID)} /><SelectField id="sli-list-datasource" label="Datasource" options={datasourceOptions} value={listDatasourceID} onChange={(event) => void updateListFilters(listSearch, event.target.value)} />{listBusy && <span className="sr-only" role="status">Updating SLI results</span>}</div>
    {items.length === 0 ? error || sourceLoading ? null : hasListFilters ? <div className="sli-empty"><p>No SLIs match these filters.</p><Button variant="outline" onClick={() => void updateListFilters('', '')}>Clear filters</Button></div> : <div className="sli-empty">
      {!canWrite ? <p>No SLIs are available to your account.</p> : sourceError ? null : sources.length === 0 ? <><p>{context.session?.role === 'administrator' ? 'Add a Prometheus datasource to create your first SLI.' : 'Ask an administrator to add a Prometheus datasource before creating an SLI.'}</p>{context.session?.role === 'administrator' && <Button onClick={() => navigate('/datasources/new?return=slis')}>Add datasource</Button>}</> : <><p>Create your first SLI from a Prometheus datasource.</p><Button onClick={() => { setDraft(emptyDraft); createIntent.current = null; navigate('/slis/new'); }}>Create SLI</Button></>}
    </div>
      : <div className="sli-table-scroll" role="region" aria-label="Service level indicator table" tabIndex={0}><table className="sli-list-table"><thead><tr><th scope="col">Health</th><th scope="col">SLI</th><th scope="col">Type</th><th scope="col">Datasource</th><th scope="col">Recent measurements</th><th scope="col">Last sample</th><th scope="col">SLOs</th></tr></thead><tbody>{items.map((item) => {
        const observation = item.latest_observation;
        const linked = item.linked_slos?.items ?? [];
        const recent = item.recent_measurements ?? [];
        return <tr key={item.id}>
          <td data-label="Health"><StatusBadge status={sliListHealth(item)} /><small className="sli-row-health-context">{item.agent_reachability === 'connected' && !observation ? 'Agent connected · no sample yet' : `Agent ${item.agent_reachability.replaceAll('_', ' ')}`}</small></td>
          <td data-label="SLI" className="sli-list-name"><a href={`/slis/${item.id}`} data-app-route><strong>{item.display_name}</strong></a><small>{item.name}</small></td>
          <td data-label="Type">{item.measurement_kind === 'event_counts' && item.component_mode === 'interval_count' ? 'Ratio · two queries' : 'Unsupported mode'}</td>
          <td data-label="Datasource">{item.datasource_display_name ? <a href={`/datasources/${item.datasource_id}`} data-app-route>{item.datasource_display_name}</a> : <span>Datasource name unavailable</span>}</td>
          <td data-label="Recent measurements"><SliRecentStrip samples={recent} cadenceSeconds={item.polling_interval_seconds} /><small>{recent.length ? `${recent.length} eligible intervals` : 'No eligible samples yet'}</small></td>
          <td data-label="Last sample">{observation ? <><time dateTime={observation.interval_end}>{shortTime(observation.interval_end)}</time><small>{observation.outcome === 'observed' ? `Latest ${observation.good_count} good / ${observation.total_count} total` : outcomeLabel(observation.outcome)}</small></> : <span>No sample yet</span>}</td>
          <td data-label="SLOs">{linked.length ? <><div className="sli-list-links">{linked.map((objective) => <a key={objective.id} href={`/slos/${objective.id}`} data-app-route>{objective.display_name || objective.name}</a>)}</div>{item.linked_slos && item.linked_slos.total_count > linked.length && <small>Showing {linked.length} of {item.linked_slos.total_count}</small>}</> : <span>No linked SLOs</span>}</td>
        </tr>;
      })}</tbody></table></div>}
    {listNextCursor && <div className="sli-actions"><Button variant="outline" onClick={() => void loadMoreSLIs()} pending={listBusy}>Load more SLIs</Button></div>}
  </>;
}
