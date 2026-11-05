import { useEffect, useMemo, useRef, useState, type FormEvent, type RefObject } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { createAgentClient, type Agent } from '../agents/client.ts';
import { ModuleView } from '../components/ModuleView.tsx';
import { SliRecentStrip, type SliRecentSample } from '../components/charts/SliRecentStrip.tsx';
import { ExactValuesTable, type ExactValueRow } from '../components/charts/ExactValuesTable.tsx';
import type { TimelineChart, Coverage } from '../components/charts/timelineModel.ts';
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
const loadTimelineChart = () => import('../components/charts/TimelineChartView.tsx').then((module) => ({ default: module.TimelineChartView }));

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
function observationCoverage(row: SliObservation): Coverage {
  if (row.outcome === 'observed') return row.total_count === '0' ? 'zero-traffic' : 'complete';
  if (row.outcome === 'complete_empty') return 'known-empty';
  if (row.outcome === 'incomplete') return 'partial';
  return 'missing';
}
// The chart is built from the loaded observation rows only; it never fabricates a range beyond them.
const CHART_INTERVALS = 1440;
// The chart plots the most recent intervals whose filled span stays within the lane's point budget; the exact table keeps every loaded row.
function buildHistoryChart(rows: SliObservation[], cadenceSeconds: number): TimelineChart | null {
  if (rows.length === 0) return null;
  const sorted = [...rows].sort((a, b) => Date.parse(a.interval_end) - Date.parse(b.interval_end));
  const last = Date.parse(sorted[sorted.length - 1]!.interval_end);
  const ordered = sorted.filter((row) => (last - Date.parse(row.interval_end)) / 1000 < cadenceSeconds * CHART_INTERVALS);
  const trimmed = ordered.length < sorted.length;
  // Points sit at interval ends; the range end is exclusive, one cadence past the last end.
  const start = ordered[0]!.interval_start, end = new Date(Date.parse(ordered[ordered.length - 1]!.interval_end) + cadenceSeconds * 1000).toISOString();
  if (Date.parse(end) <= Date.parse(start)) return null;
  const points = ordered.map((row) => {
    const coverage = observationCoverage(row);
    const value = coverage === 'complete' ? ((100 * Number(row.good_count)) / Number(row.total_count)).toFixed(4) : null;
    return { timestamp: row.interval_end, value, coverage };
  }).filter((point, index, all) => index === 0 || point.timestamp !== all[index - 1]!.timestamp);
  return { title: 'Measurements', summary: `Good ÷ total for each loaded ${cadenceSeconds}-second interval${trimmed ? ` (most recent ${CHART_INTERVALS} intervals plotted; the exact table keeps every loaded row)` : ''}. Failed and incomplete intervals are gaps; zero traffic is marked.`, start, end, stepMs: cadenceSeconds * 1000, fillExpectedGaps: true, timeZone: 'UTC', showLaneNames: false, lanes: [{ id: 'ratio', label: 'good ÷ total', unit: '%', height: 220, series: [{ id: 'ratio-series', label: 'Observed', points }] }] };
}

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
  const [chartTableOpen, setChartTableOpen] = useState(false);
  const [chartTablePage, setChartTablePage] = useState(1);
  const editTrigger = useRef<HTMLElement>(null);
  useEffect(() => { if (route.kind === 'list' && !loading && !listBusy) journeyContentReady('sli.list', !!error); }, [route.kind, loading, listBusy, items, error]);
  useEffect(() => {
    if (loading) return;
    if (route.kind === 'detail' && !historyBusy && (historyLoaded || error || historyError)) journeyContentReady('sli.detail', !!error || !!historyError);
    if (route.kind === 'create') journeyContentReady('management', !!error || !!sourceError);
  }, [route.kind, loading, historyBusy, historyLoaded, error, historyError, sourceError]);
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
    setChartTableOpen(false);
    setChartTablePage(1);
    const load = async () => {
      try {
        if (route.kind === 'list') {
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
          const page = await slis.list(pageSize, cursor, request.signal, search, datasourceID);
          if (!current()) return;
          setItems(page.items);
          setListNextCursor(page.next_cursor);
          setUpdatedAt(clockFormat.format(new Date()));
          setLoading(false);
          void loadSources(request.signal).then((value) => { if (current()) setSources(value); }).catch(() => { if (current() && !request.signal.aborted) setSourceError('Datasources could not be loaded. Try again.'); }).finally(() => { if (current()) setSourceLoading(false); });
        } else if (route.kind === 'create') {
          if (!canWrite) throw new Error('Editor or administrator access is required to create an SLI.');
          try {
            const page = await loadSources(request.signal);
            if (!current()) return;
            setSources(page);
            const selected = new URL(window.location.href).searchParams.get('datasource');
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
          void fetchHistory(route.id, historyID, generation, '', request.signal);
          void loadSources(request.signal).then((value) => { if (current()) setSources(value); }).catch(() => { if (current() && !request.signal.aborted) setSourceError('Datasource names could not be loaded.'); });
          // The list projection carries the bounded recent strip and linked objectives; the detail read omits them.
          void findProjection(resource.sli.name, resource.sli.id, request.signal).then((found) => { if (current()) { setProjection(found); setProjectionMissing(!found); } }).catch(() => { if (current() && !request.signal.aborted) setProjectionMissing(true); });
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
    setFieldErrors((current) => (current[key] ? { ...current, [key]: undefined } : current));
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

  async function submitCreate(event: FormEvent<HTMLFormElement> | undefined, thenSlo = false) {
    event?.preventDefault();
    setFormError('');
    const problems: Partial<Record<keyof CreateDraft, string>> = {};
    if (!draft.displayName.trim()) problems.displayName = 'Enter a display name.';
    if (!SLI_NAME.test(draft.name)) problems.name = 'Use lowercase letters, numbers, or hyphens for the identifier, starting and ending with a letter or number.';
    if (!UUID.test(draft.datasourceID)) problems.datasourceID = 'Choose an existing Prometheus datasource.';
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
    setChartTablePage(1);
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
    try {
      const page = await slis.list(pageSize, cursor, controller.signal, search, datasourceID);
      if (generation !== routeGeneration.current || requestID !== listRequest.current) return;
      setItems(page.items);
      setListNextCursor(page.next_cursor);
      setUpdatedAt(clockFormat.format(new Date()));
    } catch (failure) {
      if (generation === routeGeneration.current && requestID === listRequest.current && !controller.signal.aborted) setError(safeError(failure));
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
    const agentByID = new Map(agents.map((item) => [item.id, item] as const));
    const options: ComboboxOption[] = sources.map((source) => {
      const agent = agentByID.get(source.agent_id);
      // A disabled agent is not operational whatever its last report said.
      const disabled = agent ? !agent.enabled : false;
      const reach = agent && !disabled ? agent.runtime?.reachability : undefined;
      const cadence = `Prometheus · ${source.polling_interval_seconds} s cadence`;
      return {
        id: source.id,
        label: source.display_name,
        keywords: `${source.name} prometheus ${agent?.display_name ?? ''}`,
        tone: disabled ? 'stale' : reach === 'connected' ? 'healthy' : reach ? 'stale' : 'none',
        status: !agent ? 'Prometheus' : disabled ? 'Agent disabled' : reach === 'connected' ? 'Healthy' : reach === 'unreachable' ? 'Agent unreachable' : reach === 'incompatible' ? 'Agent incompatible' : 'Agent not yet reporting',
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
        : sources.length === 0 ? <div className="sli-empty"><p>{administrator ? 'Add a Prometheus datasource to create an SLI.' : 'Ask an administrator to add a Prometheus datasource before creating an SLI.'}</p>{administrator && <Button onClick={() => navigate('/datasources/new?return=slis')}>Add datasource</Button>}<Button variant="outline" onClick={() => navigate('/slis')}>Back to SLIs</Button></div>
          : <form className="ui-form sli-form" onSubmit={(event) => void submitCreate(event)} noValidate>
            <FormSection step={1} title="Identity" hint="What is this indicator?">
              <div className="ui-form-grid">
                <TextField id="sli-display-name" label="Display name" value={draft.displayName} onChange={(event) => { const value = event.target.value; setDraft((current) => ({ ...current, displayName: value, ...(!nameEdited.current ? { name: suggestName(value) } : {}) })); setFormError(''); setFieldErrors((current) => ({ ...current, displayName: undefined, ...(!nameEdited.current ? { name: undefined } : {}) })); }} hint="Name what you measure, not the query." error={fieldErrors.displayName} />
                <TextField id="sli-name" label="Identifier" value={draft.name} onChange={(event) => { nameEdited.current = true; updateDraft('name', event.target.value); }} hint="Suggested from the display name; lowercase letters, numbers and hyphens. Used in the API and CLI, fixed after creation." autoComplete="off" error={fieldErrors.name} />
              </div>
            </FormSection>
            <FormSection step={2} title="Source" hint="Where does the data live?">
              <Combobox id="sli-datasource" label="Datasource" options={options} value={draft.datasourceID || null} onChange={(id) => updateDraft('datasourceID', id ?? '')} placeholder="Type a datasource name, provider or agent…" hint="Queries run on the agent assigned to the datasource you pick, at the datasource’s cadence." error={fieldErrors.datasourceID} noMatch={{ text: (term) => `No datasource matches “${term}”.`, ...(administrator ? { action: { label: () => 'Add a datasource', onSelect: () => navigate('/datasources/new?return=slis') } } : {}) }} />
            </FormSection>
            <FormSection step={3} title="Measurement" hint="What is good, what is total?" tools={<SegmentedControl size="sm" label="Measurement kind" value="event_counts" onChange={() => undefined} options={[{ value: 'event_counts', label: 'Ratio · two queries' }, { value: 'fraction', label: 'Single fraction', disabled: true, title: 'Not available yet · needs collector support' }, { value: 'threshold', label: 'Threshold', disabled: true, title: 'Not available yet · needs collector support' }]} />}>
              <p className="ui-form-note">The SLI is <b>good ÷ total</b> for every interval. Write a PromQL query for each; both must return one number per interval, and good must be a subset of total.</p>
              <div className="ui-form-grid sli-queries">
                <div className={fieldErrors.goodQuery ? 'sli-query--invalid' : undefined}><QueryEditor id="sli-good-query" label="Good events" value={draft.goodQuery} onChange={(value) => updateDraft('goodQuery', value)} aria-invalid={fieldErrors.goodQuery ? true : undefined} aria-describedby={fieldErrors.goodQuery ? 'sli-good-query-error' : undefined} />{fieldErrors.goodQuery && <p className="ui-error" id="sli-good-query-error">{fieldErrors.goodQuery}</p>}</div>
                <div className={fieldErrors.totalQuery ? 'sli-query--invalid' : undefined}><QueryEditor id="sli-total-query" label="Total events" value={draft.totalQuery} onChange={(value) => updateDraft('totalQuery', value)} aria-invalid={fieldErrors.totalQuery ? true : undefined} aria-describedby={fieldErrors.totalQuery ? 'sli-total-query-error' : undefined} />{fieldErrors.totalQuery && <p className="ui-error" id="sli-total-query-error">{fieldErrors.totalQuery}</p>}</div>
              </div>
              <div className="sli-query-foot">
                <span>Returns more than one series? Wrap it in <code>sum()</code>.</span>
                <span>Queries return events per interval: use <code>increase()</code> or <code>sum_over_time()</code>, not <code>rate()</code>.</span>
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
    const datasource = sources.find((item) => item.id === sli.datasource_id);
    const latest = sli.latest_observation;
    const activatedAt = sli.activated_at;
    const words = sliVocabulary(sli);
    const lastGood = history.find((row) => row.outcome === 'observed' && row.total_count !== '0');
    const observedRows = history.filter((row) => row.outcome === 'observed').length;
    const failedRows = history.filter((row) => row.outcome === 'failed' || row.outcome === 'incomplete').length;
    const chart = buildHistoryChart(history, sli.polling_interval_seconds);
    const chartRows: ExactValueRow[] = [...history].sort((a, b) => Date.parse(b.interval_end) - Date.parse(a.interval_end)).map((row) => ({ timestamp: new Date(row.interval_end).toISOString(), lane: 'good ÷ total', series: outcomeLabel(row.outcome), unit: '%', value: observationCoverage(row) === 'complete' ? ((100 * Number(row.good_count)) / Number(row.total_count)).toFixed(4) : null, coverage: observationCoverage(row), details: row.outcome === 'observed' ? `${row.good_count} good / ${row.total_count} total` : row.outcome === 'incomplete' ? `Missing ${row.missing_components.join(', ')}` : row.error_code ?? '—' }));
    const visibleHistory = historyFilter === 'problems' ? history.filter((row) => row.outcome !== 'observed') : history;
    const linked = projection?.linked_slos?.items ?? [];
    const totalLinked = projection?.linked_slos?.total_count ?? 0;
    const openEdit = () => { editTrigger.current = document.activeElement instanceof HTMLElement ? document.activeElement : null; setFormError(''); setEditOpen(true); };
    return <>
      <Breadcrumb current={sli.display_name} parentHref={listHref} />
      <div className="sli-detail-head">
        <div className="sli-detail-head__copy">
          <h1 ref={titleRef} tabIndex={-1} className="app-page-heading sli-detail-head__title">{sli.display_name}</h1>
          <div className="sli-detail-head__status"><StatusMark tone={words.tone} label={`${words.label} · ${words.reason}`} /></div>
          <p className="sli-detail-head__meta"><code>{sli.name}</code> · Ratio · two queries · {datasource?.display_name ?? 'datasource'} · every {sli.polling_interval_seconds} s</p>
        </div>
        <div className="sli-detail-head__actions"><RefreshButton label="Refresh SLI" busy={pending} lastRefreshed={updatedAt} onClick={() => void refreshDetail()} />{canWrite && <Button onClick={openEdit}>Edit SLI</Button>}{canWrite && <Button variant="outline" onClick={() => navigate('/slos/new')}>New SLO</Button>}</div>
      </div>
      {error && <p className="sli-error" role="alert">{error}</p>}
      {sourceError && <p className="sli-error" role="alert">{sourceError}</p>}
      {agentError && <p className="sli-error" role="alert">Agent name could not be refreshed. Refresh to try again.</p>}
      <div className="sli-tiles">
        <section className="sli-tile" aria-labelledby="sli-tile-latest"><h2 id="sli-tile-latest" className="sli-tile__key">Latest result</h2>
          {latest ? <>
            <p className={`sli-tile__value ${latest.outcome === 'failed' || latest.outcome === 'incomplete' ? 'is-warning' : ''}`}><span className="sli-outcome">{outcomeLabel(latest.outcome)}</span></p>
            <p className="sli-interval sli-tile__sub">{formatTime(latest.interval_start)} – {formatTime(latest.interval_end)}</p>
            {latest.outcome === 'observed' ? <div className="sli-counts"><div><span>Good</span><strong>{latest.good_count}</strong></div><div><span>Total</span><strong>{latest.total_count}</strong></div></div>
              : latest.outcome === 'incomplete' ? <p className="sli-tile__sub">Missing component: {latest.missing_components.join(', ')}</p>
                : latest.outcome === 'failed' ? <p className="sli-tile__sub">Safe error code: {latest.error_code}</p>
                  : <p className="sli-tile__sub">Queries completed without a count value.</p>}
          </> : <><p className="sli-tile__value muted">No sample yet</p><p className="sli-tile__sub">No observation has been recorded yet; this does not mean the counts are zero. Almanac saves the most recent complete interval, allowing {sli.settling_delay_seconds} seconds for the datasource to finish reporting. This result can cover time before the SLI was created.</p><p className="sli-tile__sub">{sli.agent_reachability === 'unreachable' || sli.agent_reachability === 'disabled' ? 'The assigned agent is unavailable.' : sli.activation_state === 'blocked' || sli.collection_progress === 'blocked' ? 'Collection is blocked. Check the agent and datasource.' : 'The first query runs after the assigned agent receives its configuration and is ready.'}</p></>}
          <p className="sli-muted sli-tile__sub">Last attempt: {formatTime(sli.last_attempt_at)} · Last observation: {formatTime(sli.last_observation_at)}</p>
        </section>
        <section className="sli-tile" aria-labelledby="sli-tile-ratio"><h2 id="sli-tile-ratio" className="sli-tile__key">Last good ratio</h2>
          {lastGood ? <><p className="sli-tile__value">{ratioPercent(lastGood.good_count!, lastGood.total_count!)}</p><p className="sli-tile__sub mono">{count(lastGood.good_count!)} / {count(lastGood.total_count!)} · {clockTime(lastGood.interval_end)}</p></> : <><p className="sli-tile__value muted">—</p><p className="sli-tile__sub">No observed interval with traffic in the loaded history.</p></>}
        </section>
        <section className="sli-tile" aria-labelledby="sli-tile-collection"><h2 id="sli-tile-collection" className="sli-tile__key">Collection</h2>
          <div className="sli-state-list">
            <InlineState label="Activation" value={sli.activation_state} tone={sli.activation_state === 'active' ? 'positive' : sli.activation_state === 'blocked' ? 'attention' : 'neutral'} />
            <InlineState label="Agent reachability" value={sli.agent_reachability} tone={sli.agent_reachability === 'connected' ? 'positive' : sli.agent_reachability === 'unreachable' || sli.agent_reachability === 'disabled' ? 'attention' : 'neutral'} />
            <InlineState label="Collection progress" value={sli.collection_progress} tone={sli.collection_progress === 'running' ? 'positive' : sli.collection_progress === 'blocked' ? 'attention' : 'neutral'} />
            <InlineState label="Freshness" value={sli.freshness} tone={sli.freshness === 'fresh' ? 'positive' : sli.freshness === 'stale' ? 'attention' : 'neutral'} />
          </div>
          <p className="sli-tile__sub">Next scheduled query: {formatTime(sli.next_expected_execution_at)}</p>
        </section>
        <section className="sli-tile" aria-labelledby="sli-tile-loaded"><h2 id="sli-tile-loaded" className="sli-tile__key">Loaded history</h2>
          <p className="sli-tile__value">{observedRows} / {history.length}</p>
          <p className="sli-tile__sub">intervals observed · {failedRows} failed or incomplete · {missing.length} missing range{missing.length === 1 ? '' : 's'}{nextCursor ? ' · more available' : ''}</p>
        </section>
      </div>

      <section className="sli-card" aria-labelledby="sli-measurements-title">
        <header><h2 id="sli-measurements-title">Measurements</h2><span className="sli-card__hint">good ÷ total per loaded interval · UTC</span><div className="sli-card__tools"><Button variant="outline" aria-expanded={chartTableOpen} aria-controls="sli-measurements-table" onClick={() => setChartTableOpen((open) => !open)}>▦ Table</Button></div></header>
        <div className="sli-card__body">
          {historyBusy && <span className="sr-only" role="status">Refreshing observation history</span>}
          {chart ? <div className="sli-chart"><ModuleView load={loadTimelineChart} componentProps={{ chart, showHeader: false }} fallback={<p role="status">Loading measurement chart…</p>} /></div>
            : historyLoaded && !historyError ? <p className="sli-muted">No observations have been loaded yet, so there is nothing to plot.</p> : null}
          <div id="sli-measurements-table" hidden={!chartTableOpen}>{chartTableOpen && <ExactValuesTable rows={chartRows.slice((chartTablePage - 1) * 50, chartTablePage * 50)} page={chartTablePage} totalRows={chartRows.length} onPageChange={setChartTablePage} />}</div>
        </div>
      </section>

      <section className="sli-card" aria-labelledby="sli-history-title">
        <header><h2 id="sli-history-title">Recent intervals</h2><span className="sli-card__hint">exact counts · newest first{earliest ? ` · history before ${formatTime(earliest)} is unavailable` : ''}</span><div className="sli-card__tools"><SegmentedControl size="sm" label="Show intervals" value={historyFilter} onChange={setHistoryFilter} options={[{ value: 'all', label: 'All loaded', count: history.length }, { value: 'problems', label: 'Failed & missing', count: history.length - observedRows }]} /></div></header>
        {historyError && <p className="sli-error sli-card__alert" role="alert">{historyError}</p>}
        {earliest && <p className="sli-muted sli-card__note">History before {formatTime(earliest)} is unavailable.</p>}
        {missing.length > 0 && <div className="sli-card__note"><strong>Missing intervals</strong><ul>{missing.map((range) => <li key={range.interval_start}>{formatTime(range.interval_start)} – {formatTime(range.interval_end)}</li>)}</ul></div>}
        {history.length === 0 ? historyLoaded && !historyError && <p className="sli-card__note">No observations have been recorded in this range.</p>
          : visibleHistory.length === 0 ? <p className="sli-card__note">Every loaded interval was observed.</p>
            : <div className="sli-table-scroll" role="region" aria-label="Observation history" tabIndex={0}><table className="sli-table"><thead><tr><th scope="col">Interval start (UTC)</th><th scope="col">Interval end (UTC)</th><th scope="col">Outcome</th><th scope="col" className="num">Good count</th><th scope="col" className="num">Total count</th><th scope="col" className="num">Ratio</th><th scope="col">Details</th></tr></thead><tbody>{visibleHistory.map((row) => <tr key={row.observation_id}><td className="sli-mono">{new Date(row.interval_start).toISOString()}</td><td className="sli-mono">{new Date(row.interval_end).toISOString()}</td><td><StatusMark tone={row.outcome === 'observed' ? (row.total_count === '0' ? 'none' : 'healthy') : row.outcome === 'complete_empty' ? 'none' : 'warning'} label={outcomeLabel(row.outcome)} /></td><td className="sli-mono num">{row.good_count ?? '—'}</td><td className="sli-mono num">{row.total_count ?? '—'}</td><td className="sli-mono num">{row.outcome === 'observed' ? ratioPercent(row.good_count!, row.total_count!) : '—'}</td><td>{row.outcome === 'incomplete' ? `Missing ${row.missing_components.join(', ')}` : row.error_code ?? '—'}</td></tr>)}</tbody></table></div>}
        {nextCursor && <div className="sli-card__footer"><Button variant="outline" onClick={() => void loadMoreHistory()} pending={historyBusy}>Load more intervals</Button></div>}
      </section>

      <section className="sli-card" aria-labelledby="sli-definition-title">
        <header><h2 id="sli-definition-title">Definition</h2><span className="sli-card__hint">revision {sli.measurement_revision} · queries are immutable from this screen</span></header>
        <div className="sli-card__body sli-defgrid">
          <div><p className="sli-key">Datasource</p><p className="sli-value">{sourceError && !datasource ? 'Datasource name unavailable. Refresh to try again.' : <a href={`/datasources/${sli.datasource_id}`} data-app-route>{datasource?.display_name ?? 'View datasource'}</a>}</p></div>
          <div><p className="sli-key">Assigned agent</p><p className="sli-value">{context.session?.role !== 'administrator' || context.session.restricted ? 'Agent details are available to administrators.' : agentError && agent?.id !== sli.agent_id ? 'Agent name unavailable. Refresh to try again.' : <a href={`/agents?agent=${sli.agent_id}`} data-app-route>{agent?.id === sli.agent_id ? agent.display_name : 'View assigned agent'}</a>}</p></div>
          <div><p className="sli-key">Cadence</p><p className="sli-value">{sli.polling_interval_seconds} s intervals · settles {sli.settling_delay_seconds} s</p></div>
          <div><p className="sli-key">Values</p><p className="sli-value">Ratio · two queries · event counts per interval</p></div>
          <div><p className="sli-key">Collection activated</p><p className="sli-value">{activatedAt === null ? 'Not activated yet' : formatTime(activatedAt)}</p></div>
          <div><p className="sli-key">Next scheduled query</p><p className="sli-value">{formatTime(sli.next_expected_execution_at)}</p></div>
          <div className="sli-defgrid__query"><p className="sli-key">Good count query</p><pre>{sli.good_query}</pre></div>
          <div className="sli-defgrid__query"><p className="sli-key">Total count query</p><pre>{sli.total_query}</pre></div>
        </div>
        <p className="sli-card__note sli-muted">Each result covers a completed {sli.polling_interval_seconds}-second interval. The query runs after a {sli.settling_delay_seconds}-second delay to allow source data to settle.</p>
      </section>

      <section className="sli-card" aria-labelledby="sli-objectives-title">
        <header><h2 id="sli-objectives-title">Objectives</h2><span className="sli-card__hint">{projection ? `${totalLinked} SLO${totalLinked === 1 ? '' : 's'} use${totalLinked === 1 ? 's' : ''} this indicator` : projectionMissing ? 'linked objectives could not be read' : 'linked objectives are loading'}</span>{canWrite && <div className="sli-card__tools"><Button variant="outline" onClick={() => navigate('/slos/new')}>+ New SLO</Button></div>}</header>
        <div className="sli-card__body">{linked.length ? <ul className="sli-linked">{linked.map((objective) => <li key={objective.id}><a href={`/slos/${objective.id}`} data-app-route>{objective.display_name || objective.name}</a><code>{objective.name}</code></li>)}{totalLinked > linked.length && <li className="sli-muted">Showing {linked.length} of {totalLinked}</li>}</ul> : projection ? <p className="sli-muted">No SLO targets this indicator yet.</p> : <span className="sr-only" role="status">Loading linked objectives</span>}</div>
      </section>

      <details className="sli-settings"><summary>Settings and technical details</summary>
        <dl className="sli-facts"><div><dt>SLI ID</dt><dd className="sli-mono">{sli.id}</dd></div><div><dt>Assigned agent ID</dt><dd className="sli-mono">{sli.agent_id}</dd></div><div><dt>Measurement revision</dt><dd>{sli.measurement_revision}</dd></div><div><dt>Resource version</dt><dd>{sli.version}</dd></div></dl>
        <p className="ui-form-note">Measurement queries and history are immutable from this screen. Rename the SLI with Edit SLI.</p>
      </details>
      <Dialog open={editOpen} onClose={() => setEditOpen(false)} title="Edit SLI" triggerRef={editTrigger}>
        <form className="sli-metadata-form" onSubmit={(event) => { void saveMetadata(event).then((saved) => { if (saved) setEditOpen(false); }); }}>
          <p className="sli-muted">Identifier <code>{sli.name}</code> · measurement revision {sli.measurement_revision}. Queries, datasource and cadence are immutable here.</p>
          <TextField id="sli-edit-display-name" label="Display name" value={displayName} onChange={(event) => { setDisplayName(event.target.value); setFormError(''); }} error={formError || undefined} />
          <div className="sli-actions"><Button variant="outline" onClick={() => setEditOpen(false)}>Cancel</Button><Button type="submit" pending={pending}>Save display name</Button></div>
        </form>
      </Dialog>
    </>;
  }

  const datasourceOptions = [{ value: '', label: 'Any' }, ...sources.map((source) => ({ value: source.id, label: source.display_name }))];
  const hasListFilters = listSearch.trim() !== '' || listDatasourceID !== '';
  const paged = Boolean(listCursor || listNextCursor);
  const openCreate = () => { setDraft(emptyDraft); createIntent.current = null; navigate('/slis/new'); };
  return <>
    <PageHeader title="SLIs" headingRef={titleRef} actions={canWrite && !sourceError ? <Button onClick={openCreate}>New SLI</Button> : undefined} />
    {error && <p className="sli-error" role="alert">{error}</p>}
    {sourceError && <><p className="sli-error" role="alert">{sourceError}</p><Button variant="outline" onClick={() => setLoadRevision((revision) => revision + 1)}>Try again</Button></>}
    {sourceLoading && <span className="sr-only" role="status">Checking datasources</span>}
    <Toolbar className="sli-list-toolbar"><SearchField id="sli-list-search" label="Search SLIs" placeholder="Search by name or datasource" value={listSearch} onChange={(event) => void updateListFilters(event.target.value, listDatasourceID)} shortcut /><FilterSelect id="sli-list-datasource" label="Datasource" options={datasourceOptions} value={listDatasourceID} onChange={(value) => void updateListFilters(listSearch, value)} /><ToolbarSpacer /><RefreshButton label="Refresh SLIs" busy={listBusy} lastRefreshed={updatedAt} onClick={() => void updateListPage(listSearch, listDatasourceID, listPageSize, listCursor, listPreviousCursors)} />{listBusy && <span className="sr-only" role="status">Updating SLI results</span>}</Toolbar>
    {items.length === 0 ? error || sourceLoading ? null : hasListFilters ? <div className="sli-empty"><p>No SLIs match these filters.</p><Button variant="outline" onClick={() => void updateListFilters('', '')}>Clear filters</Button></div> : <div className="sli-empty">
      {!canWrite ? <p>No SLIs are available to your account.</p> : sourceError ? null : sources.length === 0 ? <><p>{context.session?.role === 'administrator' ? 'Add a Prometheus datasource to create your first SLI.' : 'Ask an administrator to add a Prometheus datasource before creating an SLI.'}</p></> : <p>Create your first SLI from a Prometheus datasource with New SLI.</p>}
    </div>
      : <div className="sli-table-scroll sli-list-scroll" role="region" aria-label="Service level indicator table" tabIndex={0}><table className="sli-list-table ui-list-table"><thead><tr><th scope="col">SLI</th><th scope="col" className="sli-col-health">Health</th><th scope="col" className="sli-col-recent">Last 12 intervals</th><th scope="col" className="sli-col-sample">Last sample</th><th scope="col" className="sli-col-slos">SLOs</th></tr></thead><tbody>{items.map((item) => {
        const observation = item.latest_observation;
        const linked = item.linked_slos?.items ?? [];
        const totalLinked = item.linked_slos?.total_count ?? linked.length;
        const recent = (item.recent_measurements ?? []) as SliRecentSample[];
        const words = sliVocabulary(item);
        // One value per cell (ADR-0032): counts, the failure detail and the exact UTC time are in the tooltip.
        const sampleDetail = observation ? `${observation.outcome === 'observed' ? `${count(observation.good_count!)} / ${count(observation.total_count!)} good` : observation.outcome === 'failed' ? `query ${observation.error_code ?? 'failed'}` : observation.outcome === 'incomplete' ? `missing ${observation.missing_components.join(', ')}` : 'no count value'} · interval ending ${shortTime(observation.interval_end)}` : item.next_expected_execution_at ? `first query ${clockTime(item.next_expected_execution_at)}` : undefined;
        const linkedNames = linked.map((objective) => objective.display_name || objective.name).join(', ') + (totalLinked > linked.length ? `, and ${totalLinked - linked.length} more` : '');
        return <tr key={item.id}>
          <td data-label="SLI" className="sli-list-name"><a href={`/slis/${item.id}`} data-app-route><strong>{item.display_name}</strong></a><small className="ui-list-table__sub"><span className="sli-list-source">{item.datasource_display_name ? <a href={`/datasources/${item.datasource_id}`} data-app-route>{item.datasource_display_name}</a> : 'datasource name unavailable'}</span></small></td>
          <td data-label="Health" className="sli-col-health"><StatusChip tone={words.tone} label={words.label} reason={words.reason} /><span className="sr-only">{sliListHealth(item)}</span></td>
          <td data-label="Last 12 intervals" className="sli-col-recent"><SliRecentStrip samples={recent} cadenceSeconds={item.polling_interval_seconds} exactValues={false} /></td>
          <td data-label="Last sample" className="sli-col-sample mono" title={sampleDetail}>{observation ? <><b className={observation.outcome === 'observed' ? '' : 'muted'}>{observation.outcome === 'observed' ? ratioPercent(observation.good_count!, observation.total_count!) : outcomeLabel(observation.outcome).toLowerCase()}</b> <span className="sli-muted">· <time dateTime={observation.interval_end}>{relativeAge(observation.interval_end)}</time></span><span className="sr-only">, {sampleDetail}</span></> : <><b className="muted">no sample yet</b>{sampleDetail && <span className="sr-only">, {sampleDetail}</span>}</>}</td>
          <td data-label="SLOs" className="sli-col-slos">{totalLinked > 0 ? <a className="sli-list-count" href={totalLinked === 1 && linked[0] ? `/slos/${linked[0].id}` : `/slis/${item.id}`} data-app-route title={linkedNames} aria-label={`${totalLinked} SLO${totalLinked === 1 ? '' : 's'}: ${linkedNames}`}>{totalLinked} SLO{totalLinked === 1 ? '' : 's'}</a> : canWrite ? <a className="sli-list-create" href="/slos/new" data-app-route>Create SLO</a> : <span className="sli-muted">No SLOs</span>}</td>
        </tr>;
      })}</tbody></table></div>}
    {(items.length > 0 || listCursor || listNextCursor) && <Pager id="sli-page-size" label="SLI result pages" position={paged ? `Showing ${items.length} on this page` : `Showing ${items.length} of ${items.length}`} pageSize={listPageSize} onPageSizeChange={(size) => void updateListPage(listSearch, listDatasourceID, size, '', [])} hasPrevious={Boolean(listCursor)} hasNext={Boolean(listNextCursor)} busy={listBusy} onPrevious={() => void updateListPage(listSearch, listDatasourceID, listPageSize, listPreviousCursors.at(-1) ?? '', listPreviousCursors.slice(0, -1), true)} onNext={() => void updateListPage(listSearch, listDatasourceID, listPageSize, listNextCursor, [...listPreviousCursors, listCursor], true)} />}
  </>;
}
