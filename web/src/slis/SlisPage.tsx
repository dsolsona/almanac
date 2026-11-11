import {useResourceTable, ResourceTableHead, ResourceTableMobileSort, resourceRowClick, type ResourceColumn} from '../components/ui/ResourceTable.tsx';
import { providerInfo } from '../datasources/providers.ts';
import { SliMeasurementsChart, intervalTime } from '../components/charts/SliMeasurementsChart.tsx';
import { buildMeasurements, countText, measurementLabels, measurementInterval } from './measurements.ts';
import { SliObjectives } from './SliObjectives.tsx';
import { useEffect, useMemo, useRef, useState, type FormEvent, type RefObject } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { createAgentClient, type Agent } from '../agents/client.ts';
import { SliRecentStrip, type SliRecentSample } from '../components/charts/SliRecentStrip.tsx';
import { Button, Combobox, Dialog, FilterSelect, FormActions, FormSection, PageHeader, Pager, Panel, QueryEditor, RefreshButton, SearchField, SegmentedControl, StatusChip, StatusMark, TextField, Toolbar, ToolbarSpacer, type ComboboxOption } from '../components/ui/index.ts';
import { createDatasourceClient, type Datasource } from '../datasources/client.ts';
import { createSliClient, SliFailure, type Sli, type SliCreateRequest, type SliObservation, type SliObservationPage } from './client.ts';
import { clockFormat, clockTime, relativeAge, sliVocabulary } from './vocabulary.ts';
import './slis.css';
import { journeyContentReady } from '../telemetry/client.ts';

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

function sliListPath(search: string, datasourceID: string, pageSize = 20, cursor = ''): string {
  const query = new URLSearchParams();
  if (search) query.set('q', search);
  if (datasourceID) query.set('datasource_id', datasourceID);
  if (pageSize === 10) query.set('page_size', '10');
  if (cursor) query.set('cursor', cursor);
  return `/slis${query.size ? `?${query}` : ''}`;
}

function Breadcrumb({ current, parentHref }: { current: string; parentHref: string }) {
  return <nav className="app-breadcrumbs" aria-label="Breadcrumb"><a href={parentHref} data-app-route>SLIs</a><span aria-hidden="true">/</span><span aria-current="page">{current}</span></nav>;
}

function PageTitle({ title, subtitle, headingRef, isList = false, parentHref = '/slis' }: { title: string; subtitle?: string; headingRef?: RefObject<HTMLHeadingElement | null>; isList?: boolean; parentHref?: string }) {
  return <div className="sli-page-title"><nav className="app-breadcrumbs" aria-label="Breadcrumb">{isList ? <span aria-current="page">SLIs</span> : <><a href={parentHref} data-app-route>SLIs</a><span aria-hidden="true">/</span><span aria-current="page">{title}</span></>}</nav><h1 ref={headingRef} tabIndex={-1} className="app-page-heading">{title}</h1>{subtitle && <p>{subtitle}</p>}</div>;
}

function outcomeLabel(value:SliObservation['outcome']):string {
  return {observed:'Observed',complete_empty:'Complete empty',incomplete:'Incomplete',failed:'Failed'}[value];
}
function formatTime(value: string | null): string {
  return value ? new Date(value).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium', timeZone: 'UTC' }) + ' UTC' : 'Not recorded';
}

function shortTime(value: string): string {
  return new Date(value).toLocaleString(undefined, { dateStyle: 'short', timeStyle: 'short', timeZone: 'UTC' }) + ' UTC';
}
function ratioPercent(good: string, total: string): string {
  const [g, t] = [Number(good), Number(total)];
  if (!Number.isFinite(g) || !Number.isFinite(t) || t === 0) return '—';
  return `${(100 * g / t).toLocaleString(undefined, { maximumFractionDigits: g === t ? 0 : 2 })}%`;
}
// Groups the integer digits of an exact decimal string without converting it; every fractional digit is kept.
function count(value: string): string {
  const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(value);
  if (!match) return value;
  const grouped = match[2]!.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  return `${match[1]}${grouped}${match[3] ? `.${match[3]}` : ''}`;
}

function sliListHealth(item: Sli): 'healthy' | 'warning' | 'no-data' | 'stale' {
  const latest = item.latest_observation;
  if (item.freshness === 'stale') return 'stale';
  if (!latest || item.freshness === 'unknown' || latest.outcome === 'complete_empty' || latest.total_count === '0') return 'no-data';
  if (latest.outcome === 'failed' || latest.outcome === 'incomplete') return 'warning';
  return 'healthy';
}
// The list projection is found by following the bounded search pages until the SLI's own ID appears.
async function findProjection(name: string, id: string, signal?: AbortSignal): Promise<Sli | null> {
  let cursor = '';
  for (let page = 0; page < 5; page += 1) {
    const result = await slis.list(200, cursor, signal, name, '');
    const match = result.items.find((item) => item.id === id);
    if (match) return match;
    cursor = result.next_cursor;
    if (!cursor) break;
  }
  return null;
}

// Agents are read in bounded pages so datasources assigned beyond the first page keep their agent's name and state.
async function loadAgents(client: ReturnType<typeof createAgentClient>, signal: AbortSignal): Promise<Agent[]> {
  const items: Agent[] = [];
  let cursor = '';
  for (let page = 0; page < 5; page += 1) {
    const result = await client.list({ signal, limit: 200, ...(cursor ? { cursor } : {}) });
    items.push(...result.items);
    cursor = result.next_cursor;
    if (!cursor) break;
  }
  return items;
}

function makeIdempotencyKey(): string {
  if (typeof window.crypto?.randomUUID === 'function') return window.crypto.randomUUID();
  const random = Math.random().toString(36).slice(2);
  return `sli-${Date.now()}-${random}`;
}

type HistoryFilter = 'all' | 'problems';
/** Manage event-count definitions and display observed intervals while preserving source-specific query semantics. */
export function SlisPage({ context }: { context: AuthContext }) {
  const agentClient = useRef<ReturnType<typeof createAgentClient> | null>(null);
  if (!agentClient.current) agentClient.current = createAgentClient();
  const [locationKey, setLocationKey] = useState(() => `${window.location.pathname}${window.location.search}`);
  const route = useMemo(() => routeFromPath(new URL(locationKey, window.location.origin).pathname), [locationKey]);
  const [items, setItems] = useState<Sli[]>([]);
  const [sources, setSources] = useState<Datasource[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [fieldErrors, setFieldErrors] = useState<Partial<Record<keyof CreateDraft, string>>>({});
  const [pendingAction, setPendingAction] = useState<'create' | 'create-slo' | null>(null);
  const [sli, setSli] = useState<Sli | null>(null);
  const [projection, setProjection] = useState<Sli | null>(null);
  const [projectionMissing, setProjectionMissing] = useState(false);
  const [agent, setAgent] = useState<Agent | null>(null);
  const [agentError, setAgentError] = useState(false);
  const [etag, setETag] = useState('');
  const [history, setHistory] = useState<SliObservation[]>([]);
  const [missing, setMissing] = useState<SliObservationPage['missing_intervals']>([]);
  const [earliest, setEarliest] = useState('');
  const [nextCursor, setNextCursor] = useState('');
  const [listNextCursor, setListNextCursor] = useState('');
  const [listCursor, setListCursor] = useState(() => new URL(window.location.href).searchParams.get('cursor') ?? '');
  const [listPageSize, setListPageSize] = useState(() => new URL(window.location.href).searchParams.get('page_size') === '10' ? 10 : 20);
  const [listPreviousCursors, setListPreviousCursors] = useState<string[]>([]);
  const [listSearch, setListSearch] = useState(() => new URL(window.location.href).searchParams.get('q') ?? '');
  const [listDatasourceID, setListDatasourceID] = useState(() => new URL(window.location.href).searchParams.get('datasource_id') ?? '');
  const table = useResourceTable({items, loadPage: async (cursor, signal) => {const page = await slis.list(200, cursor, signal, listSearch, listDatasourceID); if (!signal.aborted) setError(''); return page;}, pageSize: listPageSize, scope: `${context.session?.user_id ?? ''}:${listSearch}:${listDatasourceID}`, enabled: route.kind === 'list' && !!context.session && !context.session.restricted});
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
  const [updatedAt, setUpdatedAt] = useState('');
  const [editOpen, setEditOpen] = useState(false);
  const [historyFilter, setHistoryFilter] = useState<HistoryFilter>('all');
  const [historyWindow, setHistoryWindow] = useState<{start:string|null;end:string|null}>({start:null,end:null});
  const [historyStart,setHistoryStart]=useState<string|null>(null);
  const [supportOpen,setSupportOpen]=useState(false);
  const supportTrigger=useRef<HTMLElement|null>(null);
  const editTrigger = useRef<HTMLElement>(null);
  useEffect(() => { if (route.kind === 'list' && !loading && !listBusy) journeyContentReady('sli.list', !!error); }, [route.kind, loading, listBusy, items, error]);
  useEffect(() => {
    if (loading) return;
    if (route.kind === 'detail' && !historyBusy && (historyLoaded || error || historyError)) journeyContentReady('sli.detail', !!error || !!historyError);
    if (route.kind === 'create') journeyContentReady('management', !!error || !!sourceError);
  }, [route.kind, loading, historyBusy, historyLoaded, error, historyError, sourceError]);
  const nameEdited = useRef(false);
  const previousSource = useRef<Datasource | undefined>(undefined);
  const routeGeneration = useRef(0);
  const historyRequest = useRef(0);
  const listRequest = useRef(0);
  const listAbort = useRef<AbortController | null>(null);
  const createIntent = useRef<CreateIntent | null>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);
  const currentLocation = useRef(locationKey);
  const focusHeading = useRef(true);
  const canWrite = roleCanWrite(context);
  const measurements=useMemo(()=>buildMeasurements(history,historyWindow.start,historyWindow.end,sli?.polling_interval_seconds??60),[history,historyWindow,sli?.polling_interval_seconds]);
  const exactRows=useMemo(()=>buildMeasurements(history,historyStart,historyWindow.end,sli?.polling_interval_seconds??60).intervals.reverse(),[history,historyStart,historyWindow.end,sli?.polling_interval_seconds]);

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
    setProjection(null);
    setProjectionMissing(false);
    setAgent(null);
    setAgentError(false);
    setHistory([]);
    setMissing([]);
    setEarliest('');
    setItems([]);
    setListNextCursor('');
    setSources([]);
    setAgents([]);
    setFieldErrors({});
    setPendingAction(null);
    setNextCursor('');
    setEditOpen(false);
    setHistoryFilter('all');
    setHistoryWindow({start:null,end:null});
    setHistoryStart(null);
    setSupportOpen(false);
    const load = async () => {
      try {
        if (route.kind === 'list') {
          void loadSources(request.signal).then((value) => { if (current()) setSources(value); }).catch(() => { if (current() && !request.signal.aborted) setSourceError('Datasources could not be loaded. Try again.'); }).finally(() => { if (current()) setSourceLoading(false); });
          const filters = new URL(window.location.href).searchParams;
          const search = filters.get('q') ?? '';
          const datasourceID = filters.get('datasource_id') ?? '';
          setListSearch(search);
          setListDatasourceID(datasourceID);
          const pageSize = filters.get('page_size') === '10' ? 10 : 20;
          const cursor = filters.get('cursor') ?? '';
          const previous: unknown = window.history.state?.sliPreviousCursors;
          setListPageSize(pageSize);
          setListCursor(cursor);
          setListPreviousCursors(Array.isArray(previous) ? previous.slice(0, 1000).filter((value): value is string => typeof value === 'string' && value.length <= 512) : []);
          const acceptsRead = table.readGuard();
          const page = await slis.list(pageSize, cursor, request.signal, search, datasourceID);
          if (!current() || !acceptsRead()) return;
          setItems(page.items);
          setListNextCursor(page.next_cursor);
          setUpdatedAt(clockFormat.format(new Date()));
          setLoading(false);
        } else if (route.kind === 'create') {
          if (!canWrite) throw new Error('Editor or administrator access is required to create an SLI.');
          try {
            const page = await loadSources(request.signal);
            if (!current()) return;
            setSources(page);
            const selected = new URL(window.location.href).searchParams.get('datasource');
            previousSource.current = undefined;
            setDraft({ ...emptyDraft, datasourceID: selected ? (page.some((source) => source.id === selected) ? selected : '') : page.length === 1 ? page[0]!.id : '' });
            nameEdited.current = false;
            if (context.session?.role === 'administrator' && !context.session.restricted) {
              void loadAgents(agentClient.current!, request.signal).then((value) => { if (current()) setAgents(value); }).catch(() => undefined);
            }
          } catch { if (current() && !request.signal.aborted) setSourceError('Datasources could not be loaded. Try again.'); }
        } else if (route.id) {
          const resource = await slis.get(route.id, request.signal);
          if (!current()) return;
          setSli(resource.sli);
          setDisplayName(resource.sli.display_name);
          setETag(resource.etag);
          setUpdatedAt(clockFormat.format(new Date()));
          if (context.session?.role === 'administrator' && !context.session.restricted) {
            void agentClient.current!.get(resource.sli.agent_id, { signal: request.signal }).then((value) => { if (current()) setAgent(value.agent); }).catch(() => { if (current() && !request.signal.aborted) setAgentError(true); });
          }
          const historyID = ++historyRequest.current;
          setHistoryBusy(true);
          void fetchHistory(route.id, historyID, generation, '', request.signal, resource.sli.polling_interval_seconds);
          void loadSources(request.signal).then((value) => { if (current()) setSources(value); }).catch(() => { if (current() && !request.signal.aborted) setSourceError('Datasource names could not be loaded.'); });
          // The list projection carries the bounded recent strip and linked objectives; the detail read omits them.
          void findProjection(resource.sli.name, resource.sli.id, request.signal).then((found) => { if (current()) { setProjection(found); setProjectionMissing(!found); } }).catch(() => { if (current() && !request.signal.aborted) setProjectionMissing(true); });
        }
      } catch (failure) {
        if (current() && !request.signal.aborted) {if (route.kind === 'list') table.readFailed(failure); setError(safeError(failure));}
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
    setFieldErrors((current) => (current[key] ? { ...current, [key]: undefined } : current));
  }

  async function fetchHistory(id: string, requestID: number, generation: number, cursor = '', signal?: AbortSignal, cadence = sli?.polling_interval_seconds ?? 60) {
    const current = () => generation === routeGeneration.current && requestID === historyRequest.current && !signal?.aborted;
    try {
      const requested=cursor?50:Math.max(1,Math.ceil(3600/cadence));
      const page = await slis.observations(id, Math.min(200,requested), cursor, signal);
      if (!current()) return;
      let items=page.items,missingIntervals=page.missing_intervals,next=page.next_cursor,start=page.inspected_start;
      // The history API counts canonical intervals, including gaps; 15-second cadence needs two pages.
      if(!cursor&&requested>200&&next&&start&&page.inspected_end&&Date.parse(page.inspected_end)-Date.parse(start)===200*cadence*1000) {
        const older=await slis.observations(id,requested-200,next,signal);
        if(!current())return;
        if(Date.parse(older.inspected_end??'')!==Date.parse(start))throw new Error('Observation history did not continue its inspected range.');
        items=[...items,...older.items];missingIntervals=[...missingIntervals,...older.missing_intervals];next=older.next_cursor;start=older.inspected_start??start;
      }
      setHistory((previous) => cursor ? [...previous, ...items] : items);
      setMissing((previous) => cursor ? [...previous, ...missingIntervals] : missingIntervals);
      if(!cursor)setHistoryWindow({start,end:page.inspected_end});
      if(start)setHistoryStart(previous=>cursor&&previous&&Date.parse(previous)<Date.parse(start)?previous:start);
      setEarliest(page.earliest_queryable_interval_start);
      setNextCursor(next);
      setHistoryLoaded(true);
    } catch (failure) {
      if (current()) setHistoryError(safeError(failure));
    } finally {
      if (current()) setHistoryBusy(false);
    }
  }

  async function submitCreate(event: FormEvent<HTMLFormElement> | undefined, thenSlo = false) {
    event?.preventDefault();
    setFormError('');
    const problems: Partial<Record<keyof CreateDraft, string>> = {};
    if (!draft.displayName.trim()) problems.displayName = 'Enter a display name.';
    if (!SLI_NAME.test(draft.name)) problems.name = 'Use lowercase letters, numbers, or hyphens for the identifier, starting and ending with a letter or number.';
    if (!UUID.test(draft.datasourceID)) problems.datasourceID = 'Choose an existing datasource.';
    if (!draft.goodQuery.trim()) problems.goodQuery = 'Enter a good events query.';
    if (!draft.totalQuery.trim()) problems.totalQuery = 'Enter a total events query.';
    const errors = Object.values(problems);
    setFieldErrors(problems);
    if (errors.length) { setFormError(errors.join(' ')); titleRef.current?.focus(); return; }
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
    setPendingAction(thenSlo ? 'create-slo' : 'create');
    const generation = routeGeneration.current;
    try {
      const created = await slis.create(input, context, createIntent.current.key);
      if (generation !== routeGeneration.current) return;
      createIntent.current = null;
      navigate(thenSlo ? `/slos/new?sli=${created.id}` : `/slis/${created.id}`);
    } catch (failure) {
      if (generation !== routeGeneration.current) return;
      setFormError(safeError(failure));
      titleRef.current?.focus();
    } finally { if (generation === routeGeneration.current) { setPending(false); setPendingAction(null); } }
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
      setUpdatedAt(clockFormat.format(new Date()));
      void loadSources().then((value) => { if (generation === routeGeneration.current) { setSources(value); setSourceError(''); } }).catch(() => { if (generation === routeGeneration.current) setSourceError('Datasource names could not be loaded. Refresh to try again.'); });
      if (context.session?.role === 'administrator' && !context.session.restricted) {
        void agentClient.current!.get(resource.sli.agent_id).then((value) => { if (generation === routeGeneration.current) { setAgent(value.agent); setAgentError(false); } }).catch(() => { if (generation === routeGeneration.current) setAgentError(true); });
      }
      void findProjection(resource.sli.name, resource.sli.id).then((found) => { if (generation === routeGeneration.current) { setProjection(found); setProjectionMissing(!found); } }).catch(() => { if (generation === routeGeneration.current) setProjectionMissing(true); });
      await fetchHistory(route.id, historyID, generation, '', undefined, resource.sli.polling_interval_seconds);
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

  async function updateListPage(search: string, datasourceID: string, pageSize: number, cursor: string, previous: string[], push = false) {
    setListSearch(search);
    setListDatasourceID(datasourceID);
    setListPageSize(pageSize);
    setListCursor(cursor);
    setListPreviousCursors(previous);
    const path = sliListPath(search, datasourceID, pageSize, cursor);
    const state = { ...window.history.state, sliPreviousCursors: previous };
    if (push) window.history.pushState(state, '', path);
    else window.history.replaceState(state, '', path);
    currentLocation.current = path;
    const generation = routeGeneration.current;
    const requestID = ++listRequest.current;
    const controller = new AbortController();
    listAbort.current?.abort();
    listAbort.current = controller;
    setListBusy(true);
    setError('');
    const acceptsRead = table.readGuard();
    try {
      const page = await slis.list(pageSize, cursor, controller.signal, search, datasourceID);
      if (generation !== routeGeneration.current || requestID !== listRequest.current || !acceptsRead()) return;
      setItems(page.items);
      setListNextCursor(page.next_cursor);
      setUpdatedAt(clockFormat.format(new Date()));
    } catch (failure) {
      if (generation === routeGeneration.current && requestID === listRequest.current && !controller.signal.aborted && acceptsRead()) {table.readFailed(failure); setError(safeError(failure));}
    } finally {
      if (generation === routeGeneration.current && requestID === listRequest.current) setListBusy(false);
    }
  }

  async function updateListFilters(search: string, datasourceID: string) {
    await updateListPage(search, datasourceID, listPageSize, '', []);
  }

  async function saveMetadata(event: FormEvent<HTMLFormElement>): Promise<boolean> {
    event.preventDefault();
    if (!sli || !canWrite) return false;
    if (!displayName.trim()) { setFormError('Enter a display name.'); return false; }
    const generation = routeGeneration.current;
    setPending(true);
    setError('');
    try {
      const updated = await slis.updateMetadata(sli.id, etag, displayName, context);
      if (generation !== routeGeneration.current) return false;
      setSli(updated);
      setETag(`"sli:${updated.id}:${updated.version}"`);
      setFormError('');
      return true;
    } catch (failure) { if (generation === routeGeneration.current) setFormError(safeError(failure)); return false; }
    finally { if (generation === routeGeneration.current) setPending(false); }
  }

  const listHref = sliListPath(listSearch, listDatasourceID, listPageSize, listCursor);
  if (loading) return <>{route.kind === 'create' ? <PageHeader title="New SLI" summary="Three steps: name it, choose where the data lives, write the two queries." breadcrumb={{ label: 'SLIs', href: listHref, current: 'New SLI' }} headingRef={titleRef} /> : <PageTitle isList={route.kind === 'list'} title={route.kind === 'detail' ? 'SLI details' : 'SLIs'} />}<span className="sr-only" role="status">Opening SLIs</span></>;
  if (error && !sli && route.kind === 'detail') return <><PageTitle title="SLI unavailable" headingRef={titleRef} /><Panel heading="SLI could not be loaded"><p role="alert">{error}</p><Button variant="outline" onClick={() => navigate('/slis')}>Back to SLIs</Button></Panel></>;

  if (route.kind === 'create') {
    const selectedSource = sources.find((source) => source.id === draft.datasourceID);
    const querySource = selectedSource ?? previousSource.current;
    const provider = providerInfo(querySource?.provider ?? 'prometheus');
    const interval = querySource?.polling_interval_seconds ?? 60;
    const agentByID = new Map(agents.map((item) => [item.id, item] as const));
    const options: ComboboxOption[] = sources.map((source) => {
      const agent = agentByID.get(source.agent_id);
      // A disabled agent is not operational whatever its last report said.
      const disabled = agent ? !agent.enabled : false;
      const reach = agent && !disabled ? agent.runtime?.reachability : undefined;
      const cadence = `${providerInfo(source.provider).label} · ${source.polling_interval_seconds} s cadence`;
      return {
        id: source.id,
        label: source.display_name,
        keywords: `${source.name} ${source.provider} ${agent?.display_name ?? ''}`,
        tone: disabled ? 'stale' : reach === 'connected' ? 'healthy' : reach ? 'stale' : 'none',
        status: !agent ? providerInfo(source.provider).label : disabled ? 'Agent disabled' : reach === 'connected' ? 'Healthy' : reach === 'unreachable' ? 'Agent unreachable' : reach === 'incompatible' ? 'Agent incompatible' : 'Agent not yet reporting',
        detail: agent ? `${cadence} · agent ${agent.display_name} · ${disabled ? 'disabled' : reach ?? 'not yet reporting'}` : cadence,
        summaryMessage: disabled ? 'The assigned agent is disabled. Collection cannot start until the datasource is moved to an enabled agent.' : reach && reach !== 'connected' ? 'You can still create the SLI. Collection starts when the agent reconnects.' : undefined,
      };
    });
    const administrator = context.session?.role === 'administrator';
    return <>
      <PageHeader title="New SLI" summary="Three steps: name it, choose where the data lives, write the two queries." breadcrumb={{ label: 'SLIs', href: listHref, current: 'New SLI' }} headingRef={titleRef} />
      {error && <p className="sli-error" role="alert">{error}</p>}
      {!canWrite ? <Panel heading="Access required"><p>Editor or administrator access is required to create SLIs.</p></Panel>
        : sourceError ? <div className="sli-empty"><p role="alert">{sourceError}</p><Button variant="outline" onClick={() => setLoadRevision((revision) => revision + 1)}>Try again</Button></div>
        : sources.length === 0 ? <div className="sli-empty"><p>{administrator ? 'Add a datasource to create an SLI.' : 'Ask an administrator to add a datasource before creating an SLI.'}</p>{administrator && <Button onClick={() => navigate('/datasources/new?return=slis')}>Add datasource</Button>}<Button variant="outline" onClick={() => navigate('/slis')}>Back to SLIs</Button></div>
          : <form className="ui-form sli-form" onSubmit={(event) => void submitCreate(event)} noValidate>
            <FormSection step={1} title="Identity" hint="What is this indicator?">
              <div className="ui-form-grid">
                <TextField id="sli-display-name" label="Display name" value={draft.displayName} onChange={(event) => { const value = event.target.value; setDraft((current) => ({ ...current, displayName: value, ...(!nameEdited.current ? { name: suggestName(value) } : {}) })); setFormError(''); setFieldErrors((current) => ({ ...current, displayName: undefined, ...(!nameEdited.current ? { name: undefined } : {}) })); }} hint="Name what you measure, not the query." error={fieldErrors.displayName} />
                <TextField id="sli-name" label="Identifier" value={draft.name} onChange={(event) => { nameEdited.current = true; updateDraft('name', event.target.value); }} hint="Suggested from the display name; lowercase letters, numbers and hyphens. Used in the API and CLI, fixed after creation." autoComplete="off" error={fieldErrors.name} />
              </div>
            </FormSection>
            <FormSection step={2} title="Source" hint="Where does the data live?">
              <Combobox id="sli-datasource" label="Datasource" options={options} value={draft.datasourceID || null} onChange={(id) => { const next = sources.find((source) => source.id === id); const previous = selectedSource ?? previousSource.current; const previousProvider = previous?.provider ?? 'prometheus'; if (!id) previousSource.current = previous; if (next) previousSource.current = next; if (previousProvider && next && previousProvider !== next.provider) { setDraft((current) => ({ ...current, datasourceID: id ?? '', goodQuery: '', totalQuery: '' })); setFieldErrors({}); setFormError(''); } else updateDraft('datasourceID', id ?? ''); }} placeholder="Type a datasource name, provider or agent…" hint="Queries run on the agent assigned to the datasource you pick, at the datasource’s cadence." error={fieldErrors.datasourceID} noMatch={{ text: (term) => `No datasource matches “${term}”.`, ...(administrator ? { action: { label: () => 'Add a datasource', onSelect: () => navigate('/datasources/new?return=slis') } } : {}) }} />
            </FormSection>
            <FormSection step={3} title="Measurement" hint="What is good, what is total?" tools={<SegmentedControl size="sm" label="Measurement kind" value="event_counts" onChange={() => undefined} options={[{ value: 'event_counts', label: 'Ratio · two queries' }, { value: 'fraction', label: 'Single fraction', disabled: true, title: 'Not available yet · needs collector support' }, { value: 'threshold', label: 'Threshold', disabled: true, title: 'Not available yet · needs collector support' }]} />}>
              <p className="ui-form-note">The SLI is <b>good ÷ total</b> for every interval. Write a {provider.language} query for each; both must return one integral count per {interval}-second interval, and good must be a subset of total.</p>
              <div className="ui-form-grid sli-queries">
                <div className={fieldErrors.goodQuery ? 'sli-query--invalid' : undefined}><QueryEditor language={provider.language} placeholder={provider.example.replaceAll('$interval', `${interval}s`)} id="sli-good-query" label="Good events" value={draft.goodQuery} onChange={(value) => updateDraft('goodQuery', value)} aria-invalid={fieldErrors.goodQuery ? true : undefined} aria-describedby={fieldErrors.goodQuery ? 'sli-good-query-error' : undefined} />{fieldErrors.goodQuery && <p className="ui-error" id="sli-good-query-error">{fieldErrors.goodQuery}</p>}</div>
                <div className={fieldErrors.totalQuery ? 'sli-query--invalid' : undefined}><QueryEditor language={provider.language} placeholder={provider.example.replaceAll('$interval', `${interval}s`)} id="sli-total-query" label="Total events" value={draft.totalQuery} onChange={(value) => updateDraft('totalQuery', value)} aria-invalid={fieldErrors.totalQuery ? true : undefined} aria-describedby={fieldErrors.totalQuery ? 'sli-total-query-error' : undefined} />{fieldErrors.totalQuery && <p className="ui-error" id="sli-total-query-error">{fieldErrors.totalQuery}</p>}</div>
              </div>
              {querySource && querySource.provider !== 'prometheus' && <p className="ui-form-note">{querySource.provider === 'loki' ? 'Count one log event per request. Use [$interval] in count_over_time; Almanac replaces it with the datasource cadence. Missing results stay unknown; return an explicit zero for no matching good events.' : 'Count one server or root span per request. Sampling and dropped spans change the measured population; Almanac does not extrapolate. Filter individual durations for latency SLOs; do not use a percentile as a count.'} {querySource.provider === 'victoriatraces' ? 'End each component with an ungrouped stats count(); Almanac supplies the interval bounds.' : querySource.provider === 'tempo' ? 'End each component with ungrouped count_over_time(); Almanac supplies the interval bounds.' : ''}</p>}
              <div className="sli-query-foot">
                <span>{querySource?.provider === 'victoriatraces' ? 'Use ungrouped stats count() for one count. Query options are not supported.' : querySource?.provider === 'tempo' ? 'Use ungrouped count_over_time() for one count.' : 'Returns more than one series? Aggregate it with sum().'}</span>
                <span>{querySource?.provider === 'loki' ? 'Use count_over_time with [$interval] to count log events.' : querySource?.provider === 'victoriatraces' || querySource?.provider === 'tempo' ? 'Almanac supplies the canonical interval bounds.' : 'Return an explicit integer event count for the interval; rates and interpolated estimates are not counts.'}</span>
              </div>
              <p className="ui-form-note">Create saves a result from recent data already in your datasource, then continues at the datasource’s cadence. The first interval can precede creation.</p>
            </FormSection>
            {formError && <p className="sli-error" role="alert">{formError}</p>}
            <FormActions note="Collection starts as soon as you save.">
              <Button variant="outline" onClick={() => navigate(listHref)}>Cancel</Button>
              <Button type="submit" variant="outline" pending={pendingAction === 'create'} disabled={pending && pendingAction !== 'create'}>Create SLI</Button>
              <Button pending={pendingAction === 'create-slo'} disabled={pending && pendingAction !== 'create-slo'} onClick={() => void submitCreate(undefined, true)}>Create SLI and add an SLO</Button>
            </FormActions>
          </form>}
    </>;
  }

  if (route.kind === 'detail' && sli) {
    const datasource=sources.find(item=>item.id===sli.datasource_id),latest=sli.latest_observation,words=sliVocabulary(sli);
    const latestInterval=latest?measurementInterval(latest):null;
    const wholeHour=historyWindow.start&&historyWindow.end&&Date.parse(historyWindow.end)-Date.parse(historyWindow.start)===3600_000;
    const visibleRows=historyFilter==='problems'?exactRows.filter(row=>row.state!=='observed'&&row.state!=='zero'):exactRows;
    const problems=exactRows.filter(row=>row.state!=='observed'&&row.state!=='zero').length;
    const withoutRatio=exactRows.filter(row=>row.ratio===null).length;
    const openDetails=(id:string)=>{const details=document.getElementById(id) as HTMLDetailsElement|null;if(details){details.open=true;details.scrollIntoView?.({block:'start'});details.querySelector('summary')?.focus();}};
    const openEdit=()=>{editTrigger.current=document.activeElement instanceof HTMLElement?document.activeElement:null;setFormError('');setEditOpen(true);};
    const date=historyWindow.start?new Date(historyWindow.start).toLocaleDateString('en-GB',{day:'2-digit',month:'short',year:'numeric',timeZone:'UTC'}):null;
    return <article className="sli-detail" key={sli.id}>
      <Breadcrumb current={sli.display_name} parentHref={listHref}/>
      <div className="sli-detail-head"><div className="sli-detail-head__copy"><h1 ref={titleRef} tabIndex={-1} className="app-page-heading sli-detail-head__title">{sli.display_name}</h1><div className="sli-detail-head__status"><StatusMark tone={words.tone} label={words.label}/><span>{latest?`Latest interval ended ${clockTime(latest.interval_end)} · ${relativeAge(latest.interval_end)}`:words.reason}</span></div></div><div className="sli-detail-head__actions"><RefreshButton label="Refresh SLI" busy={pending} lastRefreshed={updatedAt} onClick={()=>void refreshDetail()}/><Button variant="ghost" onClick={()=>openDetails('sli-definition')}>View queries</Button>{canWrite&&<Button variant="outline" onClick={openEdit}>Edit SLI</Button>}{canWrite&&<Button variant="outline" onClick={()=>navigate(`/slos/new?sli=${sli.id}`)}>New SLO</Button>}</div></div>
      <p className="sli-definition-line"><strong>Good events ÷ total events.</strong> Each query defines which events count. Measured every {sli.polling_interval_seconds===60?'minute':`${sli.polling_interval_seconds} seconds`}.</p>
      {error&&<p className="sli-error" role="alert">{error}</p>}{sourceError&&<p className="sli-error" role="alert">{sourceError}</p>}{agentError&&<p className="sli-error" role="alert">Agent name could not be refreshed. Refresh to try again.</p>}
      <section className="sli-detail-card sli-measurements-panel" aria-labelledby="sli-measurements-title" aria-busy={historyBusy}>
        <header><h2 id="sli-measurements-title">Measurements</h2><span className="hint">{date&&measurements.intervals.length?`${date} · ${intervalTime({start:historyWindow.start!,end:historyWindow.end!})}`:'No inspected intervals yet'}</span><div className="tools"><Button variant="ghost" onClick={()=>openDetails('sli-intervals')}>Exact values ↓</Button></div></header>
        <div className="sli-measurements-body">
          <dl className="sli-measurement-summary"><div><dt>{sli.polling_interval_seconds===60?'Latest minute':'Latest interval'}</dt><dd>{latestInterval?.ratio!==null&&latestInterval?.ratio!==undefined?`${latestInterval.ratio}%`:latestInterval?measurementLabels[latestInterval.state]:'Waiting for data'}</dd><p>{latestInterval?.good!==null&&latestInterval?.good!==undefined&&latestInterval.total!==null?`${countText(latestInterval.good)} / ${countText(latestInterval.total)} good · ${intervalTime(latestInterval)}`:latestInterval?latestInterval.detail:'No observation has been recorded yet; this does not mean the counts are zero. The first saved interval can precede creation.'}</p></div><div><dt>{`Observed requests · ${wholeHour?'this hour':'shown period'}`}</dt><dd>{measurements.ratio!==null?`${measurements.ratio}%`:'—'}</dd><p>{measurements.total!=='0'?`${countText(measurements.bad)} unsuccessful / ${countText(measurements.total)} total`:'No observed traffic in this period.'}</p></div><div><dt>{`Data coverage · ${wholeHour?'this hour':'shown period'}`}</dt><dd className={measurements.counts.failed+measurements.counts.missing+measurements.counts.partial?'is-warning':''}>{measurements.counts.observed+measurements.counts.zero+measurements.counts.empty} <small>/ {measurements.intervals.length} {sli.polling_interval_seconds===60?'min':'intervals'}</small></dd><p>{measurements.counts.failed} query errors · {measurements.counts.missing} missing{measurements.counts.partial?` · ${measurements.counts.partial} partial`:''}{measurements.counts.empty?` · ${measurements.counts.empty} known empty`:''}{measurements.counts.zero?` · ${measurements.counts.zero} zero traffic`:''}</p></div></dl>
          {!latest&&<p className="sli-first-result">Almanac saves the most recent complete interval, allowing {sli.settling_delay_seconds} seconds for source data to settle. {sli.agent_reachability==='unreachable'||sli.agent_reachability==='disabled'?'The assigned agent is unavailable.':sli.activation_state==='blocked'||sli.collection_progress==='blocked'?'Collection is blocked. Check the agent and datasource.':'The first query runs after the assigned agent receives its configuration and is ready.'}</p>}
          {historyBusy&&<span className="sr-only" role="status">Refreshing observation history</span>}
          {historyError&&<p className="sli-error" role="alert">{historyError}</p>}
          {measurements.intervals.length>0?<SliMeasurementsChart intervals={measurements.intervals}/>:historyLoaded&&!historyError?<p className="sli-muted">No observations have been recorded in this range.</p>:null}
        </div>
        <div className="sli-measurements-legend">{(Object.keys(measurements.counts) as (keyof typeof measurements.counts)[]).filter(state=>measurements.counts[state]>0||['observed','failed','missing'].includes(state)).map(state=><span key={state}><i className={`state-key ${state}`}/>{`${measurements.counts[state]} ${state==='failed'?'query errors':measurementLabels[state].toLowerCase()}`}</span>)}<span>Observed means counts received, not an SLO verdict.</span></div>
        <div className="sli-detail-inline-note"><span>{measurements.minimum?<>{sli.polling_interval_seconds===60?'Lowest minute':'Lowest interval'} <strong className="mono">{measurements.minimum.ratio}%</strong>{` · ${intervalTime(measurements.minimum)}. Ratios use observed requests only.`}</>:'Ratios use observed requests only.'}</span><Button variant="ghost" onClick={()=>{setHistoryFilter('problems');openDetails('sli-intervals');}}>Inspect gaps ↓</Button></div>
      </section>
      <SliObjectives projection={projection} unavailable={projectionMissing}/>
      <details className="sli-detail-disclosure" id="sli-intervals"><summary>Recent intervals<span>{exactRows.length} inspected · {withoutRatio} without a ratio · exact values</span></summary>
        <div className="sli-interval-filter"><SegmentedControl size="sm" label="Show intervals" value={historyFilter} onChange={setHistoryFilter} options={[{value:'all',label:'All loaded',count:exactRows.length},{value:'problems',label:'Failed & missing',count:problems}]}/><span>Newest first · event counts per interval</span></div>
        {earliest&&<p className="sli-detail-note sli-muted">History before {formatTime(earliest)} is unavailable.</p>}
        {missing.length>0&&<p className="sr-only">Missing intervals: {missing.map(range=>`${formatTime(range.interval_start)} – ${formatTime(range.interval_end)}`).join('; ')}</p>}
        {visibleRows.length?<div className="sli-detail-table-wrap" role="region" aria-label="Observation history" tabIndex={0}><table className="sli-intervals-table"><caption>Exact counts · UTC · Observed means counts are available, not that an SLO target was met.</caption><thead><tr><th scope="col">Interval (UTC)</th><th scope="col">Outcome</th><th scope="col">Good</th><th scope="col">Total</th><th scope="col">Good ÷ total</th><th scope="col">Details</th></tr></thead><tbody>{visibleRows.map(row=><tr key={row.start}><td className="mono" title={`${row.start} – ${row.end}`}>{new Date(row.start).toLocaleDateString('en-GB',{day:'2-digit',month:'short',timeZone:'UTC'})} {intervalTime(row).replace(' UTC','')}</td><td><StatusMark tone={row.state==='observed'?'healthy':row.state==='failed'||row.state==='partial'?'warning':'none'} label={measurementLabels[row.state]}/></td><td className="mono">{row.good===null?'—':countText(row.good)}</td><td className="mono">{row.total===null?'—':countText(row.total)}</td><td className="mono">{row.ratio===null?'—':`${row.ratio}%`}</td><td>{row.detail||'—'}</td></tr>)}</tbody></table></div>:historyLoaded&&!historyError?<p className="sli-detail-note">{historyFilter==='problems'?'Every loaded interval has observed counts.':'No exact interval rows are available.'}</p>:null}
        {nextCursor&&<div className="sli-detail-note"><Button variant="outline" onClick={()=>void loadMoreHistory()} pending={historyBusy}>Load more intervals</Button></div>}
      </details>
      <details className="sli-detail-disclosure" id="sli-definition"><summary>Measurement definition<span>Source, queries and collection timing</span></summary><div className="sli-definition-source"><span>Datasource: <a href={`/datasources/${sli.datasource_id}`} data-app-route>{datasource?.display_name??'View datasource'}</a></span><span>{sli.polling_interval_seconds} s intervals · {sli.settling_delay_seconds} s settling delay</span></div><div className="sli-definition-queries"><div><h3>Good events</h3><pre>{sli.good_query}</pre></div><div><h3>Total events</h3><pre>{sli.total_query}</pre></div><p>Queries return event counts for each completed interval. A {sli.settling_delay_seconds}-second delay allows source data to settle.</p></div></details>
      <details className="sli-detail-disclosure" id="sli-collection"><summary>Collection diagnostics<span>Agent {sli.agent_reachability} · {sli.collection_progress.replaceAll('_',' ')} · {sli.freshness}</span></summary><dl className="sli-collection-facts"><div><dt>Assigned agent</dt><dd>{context.session?.role!=='administrator'||context.session.restricted?'Agent details are available to administrators.':<a href={`/agents?agent=${sli.agent_id}`} data-app-route>{agent?.id===sli.agent_id?agent.display_name:'View assigned agent'}</a>} · {sli.agent_reachability}</dd></div><div><dt>Configuration</dt><dd>{sli.activation_state}</dd></div><div><dt>Collection progress</dt><dd>{sli.collection_progress.replaceAll('_',' ')}</dd></div><div><dt>Observation freshness</dt><dd>{sli.freshness}</dd></div><div><dt>Collection activated</dt><dd>{sli.activated_at?formatTime(sli.activated_at):'Not activated yet'}</dd></div><div><dt>Last attempt</dt><dd>{formatTime(sli.last_attempt_at)}</dd></div><div><dt>Last observation</dt><dd>{formatTime(sli.last_observation_at)}</dd></div><div><dt>Next scheduled query</dt><dd>{formatTime(sli.next_expected_execution_at)}</dd></div></dl></details>
      <footer className="sli-detail-footer"><Button variant="ghost" onClick={()=>{supportTrigger.current=document.activeElement as HTMLElement;setSupportOpen(true);}}>Support identifiers</Button></footer>
      <Dialog open={supportOpen} onClose={()=>setSupportOpen(false)} title="Support identifiers" triggerRef={supportTrigger}><p>For API, CLI and support use. These do not describe measurement quality.</p><dl className="sli-facts"><div><dt>Identifier</dt><dd>{sli.name}</dd></div><div><dt>SLI ID</dt><dd>{sli.id}</dd></div><div><dt>Assigned agent ID</dt><dd>{sli.agent_id}</dd></div><div><dt>Measurement revision</dt><dd>{sli.measurement_revision}</dd></div><div><dt>Resource version</dt><dd>{sli.version}</dd></div></dl><Button variant="outline" onClick={()=>setSupportOpen(false)}>Close</Button></Dialog>
      <Dialog open={editOpen} onClose={()=>setEditOpen(false)} title="Edit SLI" triggerRef={editTrigger}><form className="sli-metadata-form" onSubmit={event=>{void saveMetadata(event).then(saved=>{if(saved)setEditOpen(false);});}}><p className="sli-muted">Identifier <code>{sli.name}</code> · measurement revision {sli.measurement_revision}. Queries, datasource and cadence are immutable here.</p><TextField id="sli-edit-display-name" label="Display name" value={displayName} onChange={event=>{setDisplayName(event.target.value);setFormError('');}} error={formError||undefined}/><div className="sli-actions"><Button variant="outline" onClick={()=>setEditOpen(false)}>Cancel</Button><Button type="submit" pending={pending}>Save display name</Button></div></form></Dialog>
    </article>;
  }

  const datasourceOptions = [{ value: '', label: 'Any' }, ...sources.map((source) => ({ value: source.id, label: source.display_name }))];
  const hasListFilters = listSearch.trim() !== '' || listDatasourceID !== '';
  const columns: ResourceColumn<Sli>[] = [
    {id: 'name', label: 'SLI', value: item => item.display_name},
    {id: 'health', label: 'Health', value: item => sliVocabulary(item).label},
    {id: 'recent', label: 'Last 12 intervals', title: 'Sort by the number of observed intervals', value: item => item.recent_measurements?.filter(sample => sample.state === 'observed').length ?? null},
    {id: 'sample', label: 'Last sample', title: 'Sort by the latest good-to-total ratio; unavailable values last', value: item => {const sample = item.latest_observation; return sample?.outcome === 'observed' && Number(sample.total_count) > 0 ? Number(sample.good_count) / Number(sample.total_count) : null;}},
    {id: 'slos', label: 'SLOs', value: item => item.linked_slos?.total_count ?? item.linked_slos?.items.length ?? 0},
  ];
  const sortedPage = table.rows(columns);
  const visibleItems = sortedPage.items;
  const paged = Boolean(listCursor || listNextCursor);
  const openCreate = () => { setDraft(emptyDraft); createIntent.current = null; navigate('/slis/new'); };
  return <>
    <PageHeader title="SLIs" headingRef={titleRef} actions={canWrite && !sourceError ? <Button onClick={openCreate}>New SLI</Button> : undefined} />
    {error && <p className="sli-error" role="alert">{error}</p>}
    {sourceError && <><p className="sli-error" role="alert">{sourceError}</p><Button variant="outline" onClick={() => setLoadRevision((revision) => revision + 1)}>Try again</Button></>}
    {sourceLoading && <span className="sr-only" role="status">Checking datasources</span>}
    <Toolbar className="sli-list-toolbar"><SearchField id="sli-list-search" label="Search SLIs" placeholder="Search by name or datasource" value={listSearch} onChange={(event) => void updateListFilters(event.target.value, listDatasourceID)} shortcut /><FilterSelect id="sli-list-datasource" label="Datasource" options={datasourceOptions} value={listDatasourceID} onChange={(value) => void updateListFilters(listSearch, value)} /><ToolbarSpacer /><RefreshButton label="Refresh SLIs" busy={listBusy} lastRefreshed={updatedAt} onClick={() => void updateListPage(listSearch, listDatasourceID, listPageSize, listCursor, listPreviousCursors)} />{listBusy && <span className="sr-only" role="status">Updating SLI results</span>}</Toolbar>
    <ResourceTableMobileSort id="slis-sort" columns={columns} sorting={table}/>
    {table.error && <p role="alert">{table.error}<Button variant="outline" disabled={listBusy || table.busy} onClick={() => table.active ? table.refresh() : void updateListPage(listSearch, listDatasourceID, listPageSize, listCursor, listPreviousCursors)}>Try again</Button></p>}
    <div className="ui-resource-panel" aria-busy={listBusy || table.busy || undefined}>
    {table.busy && <span className="sr-only" role="status">Sorting SLIs</span>}
        {visibleItems.length === 0 ? error || table.error || table.busy || sourceLoading ? null : hasListFilters ? <div className="sli-empty"><p>No SLIs match these filters.</p><Button variant="outline" onClick={() => void updateListFilters('', '')}>Clear filters</Button></div> : <div className="sli-empty">
      {!canWrite ? <p>No SLIs are available to your account.</p> : sourceError ? null : sources.length === 0 ? <><p>{context.session?.role === 'administrator' ? 'Add a datasource to create your first SLI.' : 'Ask an administrator to add a datasource before creating an SLI.'}</p></> : <p>Create your first SLI from a datasource with New SLI.</p>}
    </div>
      : <div className="sli-table-scroll sli-list-scroll" role="region" aria-label="Service level indicator table" tabIndex={0}><table className="sli-list-table ui-list-table ui-resource-table"><ResourceTableHead columns={columns} sorting={table}/><tbody>{visibleItems.map((item) => {
        const observation = item.latest_observation;
        const linked = item.linked_slos?.items ?? [];
        const totalLinked = item.linked_slos?.total_count ?? linked.length;
        const recent = (item.recent_measurements ?? []) as SliRecentSample[];
        const words = sliVocabulary(item);
        // One value per cell (ADR-0032): counts, the failure detail and the exact UTC time are in the tooltip.
        const sampleDetail = observation ? `${observation.outcome === 'observed' ? `${count(observation.good_count!)} / ${count(observation.total_count!)} good` : observation.outcome === 'failed' ? `query ${observation.error_code ?? 'failed'}` : observation.outcome === 'incomplete' ? `missing ${observation.missing_components.join(', ')}` : 'no count value'} · interval ending ${shortTime(observation.interval_end)}` : item.next_expected_execution_at ? `first query ${clockTime(item.next_expected_execution_at)}` : undefined;
        const linkedNames = linked.map((objective) => objective.display_name || objective.name).join(', ') + (totalLinked > linked.length ? `, and ${totalLinked - linked.length} more` : '');
        return <tr key={item.id} onClick={event => resourceRowClick(event, () => navigate(`/slis/${item.id}`))}>
          <td data-label="SLI" className="sli-list-name"><a href={`/slis/${item.id}`} data-app-route><strong>{item.display_name}</strong></a><small className="ui-list-table__sub"><span className="sli-list-source">{item.datasource_display_name ? <a href={`/datasources/${item.datasource_id}`} data-app-route>{item.datasource_display_name}</a> : 'datasource name unavailable'}</span></small></td>
          <td data-label="Health" className="sli-col-health"><StatusChip tone={words.tone} label={words.label} reason={words.reason} /><span className="sr-only">{sliListHealth(item)}</span></td>
          <td data-label="Last 12 intervals" className="sli-col-recent"><SliRecentStrip samples={recent} cadenceSeconds={item.polling_interval_seconds} exactValues={false} /></td>
          <td data-label="Last sample" className="sli-col-sample mono" title={sampleDetail}>{observation ? <><b className={observation.outcome === 'observed' ? '' : 'muted'}>{observation.outcome === 'observed' ? ratioPercent(observation.good_count!, observation.total_count!) : outcomeLabel(observation.outcome).toLowerCase()}</b> <span className="sli-muted">· <time dateTime={observation.interval_end}>{relativeAge(observation.interval_end)}</time></span><span className="sr-only">, {sampleDetail}</span></> : <><b className="muted">no sample yet</b>{sampleDetail && <span className="sr-only">, {sampleDetail}</span>}</>}</td>
          <td data-label="SLOs" className="sli-col-slos">{totalLinked > 0 ? <a className="sli-list-count" href={totalLinked === 1 && linked[0] ? `/slos/${linked[0].id}` : `/slis/${item.id}`} data-app-route title={linkedNames} aria-label={`${totalLinked} SLO${totalLinked === 1 ? '' : 's'}: ${linkedNames}`}>{totalLinked} SLO{totalLinked === 1 ? '' : 's'}</a> : canWrite ? <a className="sli-list-create" href="/slos/new" data-app-route>Create SLO</a> : <span className="sli-muted">No SLOs</span>}</td>
        </tr>;
      })}</tbody></table></div>}
    {(sortedPage.total > 0 || items.length > 0 || listCursor || listNextCursor) && <Pager id="sli-page-size" label="SLI result pages" position={table.active && table.busy ? 'Loading sorted results…' : table.active ? `Showing ${sortedPage.total ? sortedPage.start + 1 : 0}–${sortedPage.start + visibleItems.length} of ${sortedPage.total}` : paged ? `Showing ${items.length} on this page` : `Showing ${items.length} of ${items.length}`} pageSize={listPageSize} onPageSizeChange={(size) => {table.resetPage(); void updateListPage(listSearch, listDatasourceID, size, '', []);}} hasPrevious={table.active ? sortedPage.hasPrevious : Boolean(listCursor)} hasNext={table.active ? sortedPage.hasNext : Boolean(listNextCursor)} busy={listBusy || table.busy} onPrevious={() => table.active ? table.previous() : void updateListPage(listSearch, listDatasourceID, listPageSize, listPreviousCursors.at(-1) ?? '', listPreviousCursors.slice(0, -1), true)} onNext={() => table.active ? table.next() : void updateListPage(listSearch, listDatasourceID, listPageSize, listNextCursor, [...listPreviousCursors, listCursor], true)} />}
    </div>
  </>;
}
