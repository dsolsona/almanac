import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type RefObject } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, ChoiceChips, Combobox, FormActions, FormSection, PageHeader, Pager, Panel, QueryEditor, RefreshButton, SearchField, SegmentedControl, StatusChip, StatusMark, TextareaField, TextField, Toolbar, ToolbarSpacer, type ComboboxOption, type MarkTone } from '../components/ui/index.ts';
import { createSliClient } from '../slis/client.ts';
import { relativeAge } from '../slis/vocabulary.ts';
import { createDatasourceClient, DatasourceFailure, type Datasource, type DatasourceWrite, type Diagnostic, type Provider } from './client.ts';
import './datasources.css';
import { journeyContentReady } from '../telemetry/client.ts';

type Agent = { id: string; name: string; display_name: string; enabled: boolean; runtime?: { reachability?: string; protocol?: string; software_version?: string; last_heartbeat_at?: string | null; capabilities?: { provider: string; configuration_formats: string[]; operations: string[] }[] } | null };
type SliUsage = Map<string, { count: number; names: string[] }>;
type HealthFilter = 'all' | 'attention' | 'unused';
type TlsMode = 'verify' | 'ca' | 'skip';
type ProviderReadinessState = Provider['availability'] | 'loading' | 'still_applying' | 'error';
type Draft = Omit<DatasourceWrite, 'authentication'> & { authMode: 'none' | 'basic' | 'bearer'; username: string; password: string; token: string; passwordAction: 'replace' | 'retain' | 'clear'; tokenAction: 'replace' | 'retain' | 'clear'; passwordConfigured: boolean; tokenConfigured: boolean; nameEdited: boolean };
type SaveIntent = { input: DatasourceWrite; key: string; etag?: string; isNew: boolean; route: string; cancelReassignment?: boolean };

const client = createDatasourceClient();
const sliClient = createSliClient();
const clockFormat = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: 'UTC', hour12: false });
const CADENCE_PRESETS = [['15', '15 s'], ['30', '30 s'], ['60', '60 s'], ['300', '5 min']] as const;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const DATASOURCE_NAME = /^[a-z][a-z0-9-]{0,62}$/;
const object = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const emptyDraft = (): Draft => ({ name: '', display_name: '', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: '', tls: { insecure_skip_verify: false, ca_pem: null }, polling_interval_seconds: 60, agent_id: '', authMode: 'none', username: '', password: '', token: '', passwordAction: 'retain', tokenAction: 'retain', passwordConfigured: false, tokenConfigured: false, nameEdited: false });
function suggestDatasourceName(displayName: string): string {
  let suggestion = displayName.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 63).replace(/-+$/g, '');
  if (!suggestion) return '';
  if (suggestion[0] < 'a' || suggestion[0] > 'z') suggestion = `source-${suggestion}`.slice(0, 63).replace(/-+$/g, '');
  return suggestion;
}
const safeError = (error: unknown) => error instanceof DatasourceFailure ? error.message : 'Datasource management is temporarily unavailable.';
const terminal = (value: Diagnostic) => ['succeeded', 'failed', 'canceled', 'timed_out', 'unknown'].includes(value.state);

async function loadAgents(signal: AbortSignal): Promise<Agent[]> {
  let response: Response;
  try { response = await fetch('/api/v1/agents?limit=200', { credentials: 'same-origin', cache: 'no-store', signal }); }
  catch { throw new Error('Agent assignments could not be loaded.'); }
  if (!response.ok) throw new Error(response.status === 403 ? 'Editor or administrator access is required to manage agent assignments.' : 'Agent assignments could not be loaded.');
  const value: unknown = await response.json();
  if (!object(value) || !Array.isArray(value.items) || value.items.length > 200 || !value.items.every((item) => object(item) && typeof item.id === 'string' && UUID.test(item.id) && typeof item.display_name === 'string' && typeof item.enabled === 'boolean')) {
    throw new Error('The server returned invalid agent assignment data.');
  }
  return value.items as Agent[];
}

type DatasourceAgentState = 'disabled' | 'unreported' | 'incompatible' | 'unsupported' | 'offline' | 'connected';

function datasourceAgentState(agent: Agent, forCollection: boolean | null = false): DatasourceAgentState {
  if (!agent.enabled) return 'disabled';
  const runtime = agent.runtime;
  if (!runtime) return 'unreported';
  if (runtime.reachability === 'incompatible') return 'incompatible';
  const capabilities = Array.isArray(runtime.capabilities) ? runtime.capabilities : [];
  if (capabilities.length === 0) return 'unreported';
  const supported = capabilities.some((capability) => capability.provider === 'prometheus'
    && capability.configuration_formats.includes('prometheus-http-v1')
    && (forCollection !== false && capability.operations.includes('collect') || forCollection !== true && capability.operations.includes('connection_test') && capability.operations.includes('query_preview')));
  if (!supported) return 'unsupported';
  if (runtime.reachability === 'connected') return 'connected';
  if (runtime.reachability === 'unreachable') return 'offline';
  return 'unreported';
}

function supportsDatasourceDiagnostics(agent: Agent): boolean {
  const state = datasourceAgentState(agent);
  return state === 'connected' || state === 'offline';
}

// Picker status for an agent, from the same eligibility rules the assignment uses.
function agentOption(agent: Agent, forCollection: boolean | null = false): ComboboxOption {
  const state = datasourceAgentState(agent, forCollection);
  const runtime = agent.runtime ?? undefined;
  const status: Record<DatasourceAgentState, [MarkTone, string]> = {
    connected: ['healthy', 'Connected'], offline: ['stale', 'Unreachable'], disabled: ['none', 'Disabled'],
    unreported: ['none', runtime ? 'Capabilities not reported' : 'Never connected'], incompatible: ['warning', 'Incompatible protocol'], unsupported: ['warning', forCollection ? 'Collection unsupported' : 'Diagnostics unsupported'],
  };
  const [tone, label] = status[state];
  const detail = [runtime?.protocol && runtime.software_version ? `${runtime.protocol} · ${runtime.software_version}` : '', runtime?.last_heartbeat_at ? `last heartbeat ${relativeAge(runtime.last_heartbeat_at)}` : '',
    state === 'connected' ? supportsDatasourceDiagnostics(agent) ? 'can test & preview' : 'collection only' : state === 'offline' ? 'waiting for reconnection' : state === 'unreported' && !runtime ? 'start it with its token first' : ''].filter(Boolean).join(' · ');
  return { id: agent.id, label: agent.display_name, keywords: agent.name, tone, status: label, detail: detail || undefined, disabled: state !== 'connected' && state !== 'offline' };
}

// ADR-0028 vocabulary for a datasource row, from its assignment and the agent's runtime only.
function datasourceVocabulary(item: Datasource, agents: Agent[], forCollection: boolean | null = false): { tone: MarkTone; label: string; reason: string; attention: boolean } {
  const byID = (id: string) => agents.find((agent) => agent.id === id)?.display_name ?? 'another agent';
  if (item.reassignment) {
    const from = byID(item.reassignment.source_agent_id), to = byID(item.reassignment.target_agent_id);
    return item.reassignment.state === 'replacement_unavailable'
      ? { tone: 'stale', label: 'Not collecting', reason: `${to} unavailable`, attention: true }
      : { tone: 'warning', label: 'Updating', reason: `${from} → ${to} · applying configuration`, attention: true };
  }
  const agent = agents.find((candidate) => candidate.id === item.agent_id);
  if (!agent) return { tone: 'none', label: 'Unknown agent', reason: 'assigned agent not found', attention: true };
  switch (datasourceAgentState(agent, forCollection)) {
    case 'connected': return { tone: 'healthy', label: 'Active', reason: 'agent connected', attention: false };
    case 'offline': return { tone: 'stale', label: 'Not collecting', reason: agent.runtime?.last_heartbeat_at ? `agent unreachable for ${relativeAge(agent.runtime.last_heartbeat_at).replace(' ago', '')}` : 'agent unreachable', attention: true };
    case 'disabled': return { tone: 'stale', label: 'Not collecting', reason: 'agent disabled', attention: true };
    case 'unreported': return { tone: 'none', label: 'Waiting', reason: agent.runtime ? 'agent capabilities not reported' : 'agent never connected', attention: false };
    case 'incompatible': return { tone: 'warning', label: 'Needs attention', reason: 'agent protocol incompatible', attention: true };
    case 'unsupported': return { tone: 'warning', label: 'Needs attention', reason: forCollection ? 'agent lacks Prometheus collection' : 'agent lacks Prometheus diagnostics', attention: true };
  }
}

// Best-effort SLI usage per datasource from the bounded SLI list; a few pages at most.
async function loadSliUsage(signal: AbortSignal, datasourceID = ''): Promise<{ usage: SliUsage; truncated: boolean }> {
  const usage: SliUsage = new Map();
  let cursor = '';
  for (let page = 0; page < 5; page += 1) {
    const result = await sliClient.list(200, cursor, signal, '', datasourceID);
    for (const item of result.items) {
      const entry = usage.get(item.datasource_id) ?? { count: 0, names: [] };
      entry.count += 1;
      if (entry.names.length < 3) entry.names.push(item.name);
      usage.set(item.datasource_id, entry);
    }
    cursor = result.next_cursor;
    if (!cursor) return { usage, truncated: false };
  }
  return { usage, truncated: true };
}

// An empty PEM keeps the Custom CA choice open in the form; it is sent as null.
// Field checks the browser no longer performs for us (the forms use noValidate), so invalid settings never reach a save.
function validateDraft(draft: Draft): { field: string; message: string } | null {
  let endpoint: URL | null = null;
  try { endpoint = new URL(draft.endpoint.trim()); } catch { endpoint = null; }
  if (!endpoint || !['http:', 'https:'].includes(endpoint.protocol)) return { field: 'datasource-endpoint', message: 'Enter the Prometheus endpoint as an HTTP or HTTPS URL.' };
  if (endpoint.username || endpoint.password) return { field: 'datasource-endpoint', message: 'Remove credentials from the endpoint URL; use the authentication settings instead.' };
  const cadence = Number(draft.polling_interval_seconds);
  if (!Number.isInteger(cadence) || cadence < 15 || cadence > 86400) return { field: 'datasource-polling', message: 'Query every must be a whole number of seconds from 15 to 86400.' };
  if (draft.authMode === 'basic' && !draft.username.trim()) return { field: 'datasource-username', message: 'Enter the username for basic authentication.' };
  if (draft.authMode === 'basic' && draft.passwordAction === 'replace' && !draft.password) return { field: 'datasource-password', message: 'Enter the password, or keep the saved one.' };
  if (draft.authMode === 'bearer' && draft.tokenAction === 'replace' && !draft.token) return { field: 'datasource-token', message: 'Enter the bearer token, or keep the saved one.' };
  return null;
}

function tlsMode(tls: DatasourceWrite['tls']): TlsMode {
  return tls.insecure_skip_verify ? 'skip' : tls.ca_pem !== null ? 'ca' : 'verify';
}
function normalizeTls(tls: DatasourceWrite['tls']): DatasourceWrite['tls'] {
  return { insecure_skip_verify: tls.insecure_skip_verify, ca_pem: tls.ca_pem ? tls.ca_pem : null };
}

function datasourceAvailabilityMessage(agent: Agent | undefined, agents: Agent[]): string {
  if (agent) {
    switch (datasourceAgentState(agent)) {
      case 'disabled': return 'The assigned agent is disabled.';
      case 'unreported': return 'The assigned agent has not reported its datasource capabilities yet.';
      case 'incompatible': return 'The assigned agent is using an incompatible protocol.';
      case 'unsupported': return 'The assigned agent does not report the required Prometheus diagnostic operations.';
      case 'offline': return 'The assigned agent is offline. Diagnostics require a connected agent.';
      case 'connected': return 'Runs on the assigned agent.';
    }
  }
  if (agents.length === 0) return 'No agents have been registered.';
  if (agents.some((item) => item.enabled && datasourceAgentState(item) === 'unreported')) return 'Agent capabilities have not been reported yet.';
  if (agents.every((item) => !item.enabled)) return 'All registered agents are disabled.';
  if (agents.some((item) => datasourceAgentState(item) === 'incompatible')) return 'An enabled agent is using an incompatible protocol.';
  if (agents.some((item) => datasourceAgentState(item) === 'offline')) return 'Compatible agents are offline. Diagnostics require a connected agent.';
  return 'No enabled agent reports the required Prometheus diagnostic operations.';
}

function providerReadinessState(items: Provider[]): ProviderReadinessState {
  const provider = items.find((item) => item.provider === 'prometheus' && item.configuration_format === 'prometheus-http-v1');
  if (!provider) return 'unsupported';
  if (provider.availability === 'available') {
    return provider.desired_configuration_revision !== null && provider.applied_configuration_revision === provider.desired_configuration_revision
      ? 'available' : 'unreported';
  }
  return provider.availability;
}

function providerReadinessMessage(state: ProviderReadinessState, provider: Provider | undefined): string {
  switch (state) {
    case 'loading': return 'Checking whether the assigned agent has applied these datasource settings.';
    case 'applying': {
      const progress = provider?.desired_configuration_revision && provider.applied_configuration_revision !== null
        ? ` (revision ${provider.applied_configuration_revision} of ${provider.desired_configuration_revision})` : '';
      return `The assigned agent is applying datasource settings${progress}. Preview will be available when it finishes.`;
    }
    case 'still_applying': return 'The assigned agent is still applying datasource settings. Preview remains unavailable; check readiness again shortly.';
    case 'offline': return 'The assigned agent is offline. Preview is unavailable until it reconnects.';
    case 'disabled': return 'The assigned agent is disabled. Preview is unavailable.';
    case 'unreported': return 'The assigned agent has not reported an applied datasource configuration yet.';
    case 'configuration_failed': return 'The assigned agent could not apply its latest configuration. Preview is unavailable until it recovers.';
    case 'incompatible': return 'The assigned agent is using an incompatible protocol.';
    case 'unsupported': case 'planned': return 'The assigned agent does not support Prometheus query previews.';
    case 'error': return 'Agent readiness could not be checked. Retry the readiness check before running a preview.';
    case 'available': return 'The assigned agent has applied the datasource settings. Preview runs on that agent and does not create observations.';
  }
}

async function pollProviderReadiness(agentID: string, signal: AbortSignal, deadline: number, onUpdate: (items: Provider[], state: ProviderReadinessState) => void): Promise<ProviderReadinessState> {
  while (!signal.aborted) {
    const items = await client.providers(agentID, signal);
    const state = providerReadinessState(items);
    onUpdate(items, state);
    if (state !== 'applying') return state;
    const remaining = deadline - Date.now();
    if (remaining <= 0) return 'still_applying';
    await new Promise<void>((resolve) => {
      let timer: number | undefined;
      const finish = () => {
        if (timer !== undefined) window.clearTimeout(timer);
        signal.removeEventListener('abort', finish);
        resolve();
      };
      timer = window.setTimeout(finish, Math.min(500, remaining));
      signal.addEventListener('abort', finish, { once: true });
    });
  }
  return 'error';
}

function asWrite(draft: Draft, isNew: boolean): DatasourceWrite {
  const secret = (action: Draft['passwordAction'], value: string) => action === 'replace' ? { action, value } : { action };
  const passwordAction = draft.authMode === 'basic' ? (isNew && draft.passwordAction === 'retain' ? 'clear' : draft.passwordAction) : 'clear';
  const tokenAction = draft.authMode === 'bearer' ? (isNew && draft.tokenAction === 'retain' ? 'clear' : draft.tokenAction) : 'clear';
  return { name: draft.name.trim(), display_name: draft.display_name.trim(), provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: draft.endpoint.trim(), tls: normalizeTls(draft.tls),
      authentication: { mode: draft.authMode, username: draft.authMode === 'basic' ? draft.username : null, password_secret: secret(passwordAction, draft.password), token_secret: secret(tokenAction, draft.token) },
    polling_interval_seconds: Number(draft.polling_interval_seconds), agent_id: draft.agent_id };
}

function fromDatasource(source: Datasource): Draft {
  return { ...emptyDraft(), name: source.name, display_name: source.display_name, endpoint: source.endpoint, tls: source.tls, polling_interval_seconds: source.polling_interval_seconds, agent_id: source.agent_id,
    authMode: source.authentication.mode, username: source.authentication.username ?? '', passwordConfigured: source.authentication.password_configured, tokenConfigured: source.authentication.token_configured };
}

function connectionDraftChanged(draft: Draft, source: Datasource): boolean {
  const authChanged = draft.authMode !== source.authentication.mode
    || (draft.authMode === 'basic' && (draft.username !== (source.authentication.username ?? '') || draft.passwordAction !== 'retain' || draft.password !== ''))
    || (draft.authMode === 'bearer' && (draft.tokenAction !== 'retain' || draft.token !== ''));
  return draft.endpoint.trim() !== source.endpoint || JSON.stringify(normalizeTls(draft.tls)) !== JSON.stringify(normalizeTls(source.tls))
    || draft.agent_id !== source.agent_id || authChanged;
}

function retainsConfiguredSecret(draft: Draft): boolean {
  return draft.authMode === 'basic' && draft.passwordConfigured && draft.passwordAction === 'retain'
    || draft.authMode === 'bearer' && draft.tokenConfigured && draft.tokenAction === 'retain';
}

export function DatasourcesPage({ context }: { context: AuthContext }) {
  const role = context.session?.role.toLowerCase() ?? 'viewer';
  const admin = role === 'administrator';
  const canPreview = admin || role === 'editor';
  const [locationKey, setLocationKey] = useState(() => `${location.pathname}${location.search}`);
  const selectedID = useMemo(() => /^\/datasources\/([0-9a-f-]{36})$/.exec(new URL(locationKey, location.origin).pathname)?.[1] ?? null, [locationKey]);
  const isNew = new URL(locationKey, location.origin).pathname === '/datasources/new';
  const returnToSLI = isNew && new URL(locationKey, location.origin).searchParams.get('return') === 'slis';
  const [items, setItems] = useState<Datasource[]>([]);
  const [cursor, setCursor] = useState('');
  const [nextCursor, setNextCursor] = useState('');
  const [previousCursors, setPreviousCursors] = useState<string[]>([]);
  const [pageSize, setPageSize] = useState(20);
  const [updatedAt, setUpdatedAt] = useState('');
  const [sliUsage, setSliUsage] = useState<SliUsage | null>(null);
  const [sliUsageTruncated, setSliUsageTruncated] = useState(false);
  const [listSearch, setListSearch] = useState('');
  const [healthFilter, setHealthFilter] = useState<HealthFilter>('all');
  const [createAndAddSli, setCreateAndAddSli] = useState(false);
  const continueToSli = useRef(false);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [providers, setProviders] = useState<Provider[]>([]);
  const [previewReadiness, setPreviewReadiness] = useState<ProviderReadinessState>('loading');
  const [providerRefresh, setProviderRefresh] = useState(0);
  const [source, setSource] = useState<Datasource | null>(null);
  const [etag, setEtag] = useState('');
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [query, setQuery] = useState('up');
  const [diagnostic, setDiagnostic] = useState<Diagnostic | null>(null);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [cancelSubmitting, setCancelSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [reassignmentStale, setReassignmentStale] = useState(false);
  const [nameValidationAttempted, setNameValidationAttempted] = useState(false);
  const diagnosticController = useRef<AbortController | null>(null);
  const providerRequest = useRef<AbortController | null>(null);
  const providerGeneration = useRef(0);
  const locationKeyRef = useRef(locationKey);
  const listResultKey = useRef('');
  const saveIntent = useRef<SaveIntent | null>(null);
  const saveController = useRef<AbortController | null>(null);
  const [uncertainSave, setUncertainSave] = useState(false);
  const currentUncertainSave = uncertainSave && saveIntent.current?.route === locationKey;
  const heading = useRef<HTMLHeadingElement>(null);
  const errorSummary = useRef<HTMLParagraphElement>(null);
  useEffect(() => () => { saveIntent.current = null; }, []);
  useEffect(() => { if (!loading) heading.current?.focus(); }, [locationKey, loading]);
  useEffect(() => { if (error) errorSummary.current?.focus(); }, [error]);
  useEffect(() => {
    if (saveIntent.current && saveIntent.current.route !== locationKey) {
      saveIntent.current = null;
      saveController.current?.abort();
      saveController.current = null;
      setUncertainSave(false);
      setPending(false);
      setError('');
    }
  }, [locationKey]);

  function changeRoute(nextRoute: string) {
    if (locationKeyRef.current !== nextRoute) { setNameValidationAttempted(false); setLoading(true); }
    if (locationKeyRef.current !== nextRoute && !saveIntent.current && saveController.current) { saveController.current.abort(); saveController.current = null; setPending(false); }
    locationKeyRef.current = nextRoute;
    if (saveIntent.current && saveIntent.current.route !== nextRoute) {
      saveIntent.current = null;
      saveController.current?.abort();
      saveController.current = null;
      setUncertainSave(false);
      setPending(false);
      setError('');
    }
    setLocationKey(nextRoute);
  }

  const navigate = useCallback((path: string) => {
    history.pushState({}, '', path);
    window.dispatchEvent(new PopStateEvent('popstate'));
  }, []);
  useEffect(() => {
    const changed = () => changeRoute(`${location.pathname}${location.search}`);
    window.addEventListener('popstate', changed);
    return () => window.removeEventListener('popstate', changed);
  }, []);

  // The previous-cursor trail changes only once the page it describes has loaded; usage counts are re-read for the list scope.
  const loadList = useCallback((next = '', size = 20, trailAfter?: string[]) => {
    const request = new AbortController(); const routeAtRequest = locationKeyRef.current; setLoading(true); setError('');
    setSliUsage(null); setSliUsageTruncated(false);
    void (async () => {
      try {
        const [page, agentItems] = await Promise.all([
          client.list(size, next, request.signal),
          canPreview ? loadAgents(request.signal) : Promise.resolve([]),
        ]);
        if (request.signal.aborted || locationKeyRef.current !== routeAtRequest) return;
        listResultKey.current = routeAtRequest;
        setItems(page.items); setCursor(next); setNextCursor(page.next_cursor); setAgents(agentItems); setUpdatedAt(clockFormat.format(new Date()));
        if (trailAfter) setPreviousCursors(trailAfter);
        void loadSliUsage(request.signal).then((result) => { if (!request.signal.aborted && locationKeyRef.current === routeAtRequest) { setSliUsage(result.usage); setSliUsageTruncated(result.truncated); } }).catch(() => undefined);
      } catch (reason) { if (!request.signal.aborted && locationKeyRef.current === routeAtRequest) setError(`${safeError(reason)}${listResultKey.current === routeAtRequest ? ' Showing previous results.' : ''}`); }
      finally { if (!request.signal.aborted && locationKeyRef.current === routeAtRequest) setLoading(false); }
    })();
    return request;
  }, [canPreview]);

  useEffect(() => {
    if (selectedID) {
      const request = new AbortController(); setLoading(true); setError(''); setNotice(''); setReassignmentStale(false);
      setProviders([]); setPreviewReadiness('loading');
      setSliUsage(null); setSliUsageTruncated(false);
      void loadSliUsage(request.signal, selectedID).then((result) => { if (!request.signal.aborted) { setSliUsage(result.usage); setSliUsageTruncated(result.truncated); } }).catch(() => undefined);
      void client.get(selectedID, request.signal).then(({ datasource, etag: tag }) => {
        if (request.signal.aborted) return [] as Agent[];
        setSource(datasource); setEtag(tag); setDraft(fromDatasource(datasource)); setUpdatedAt(clockFormat.format(new Date()));
        return canPreview ? loadAgents(request.signal) : Promise.resolve([] as Agent[]);
      }).then((agentItems) => { if (!request.signal.aborted) setAgents(agentItems); })
        .catch((reason: unknown) => { if (!request.signal.aborted) setError(safeError(reason)); }).finally(() => { if (!request.signal.aborted) setLoading(false); });
      return () => request.abort();
    }
    setSource(null); setEtag(''); setDraft(emptyDraft()); setProviders([]); setPreviewReadiness('loading'); setDiagnostic(null);
    setCreateAndAddSli(false); continueToSli.current = false;
    if (!isNew) { const request = loadList(cursor, pageSize); return () => request.abort(); }
    const request = new AbortController(); setLoading(true);
    void loadAgents(request.signal).then(async (result) => {
      if (request.signal.aborted) return;
      const eligible = result.filter(supportsDatasourceDiagnostics);
      setAgents(result); setDraft((current) => ({ ...current, agent_id: eligible[0]?.id ?? '' }));
      if (eligible[0]) setProviders(await client.providers(eligible[0].id, request.signal));
    }).catch((reason: unknown) => { if (!request.signal.aborted) setError(safeError(reason)); }).finally(() => { if (!request.signal.aborted) setLoading(false); });
    return () => request.abort();
  // locationKey is the route state; cursor is intentionally retained by Back.
  // eslint-disable-next-line react-hooks/exhaustive-deps -- pageSize changes reload through the pager handler.
  }, [locationKey, selectedID, isNew, loadList, admin, canPreview]);
  useEffect(() => {
    if (!loading) journeyContentReady('management', !!error);
  }, [loading, error, locationKey]);

  useEffect(() => {
    if (!source?.reassignment || source.id !== selectedID || loading || pending || currentUncertainSave || reassignmentStale || draft.agent_id !== source.agent_id) return;
    const request = new AbortController();
    let timer: number | undefined;
    const poll = async () => {
      try {
        const result = await client.get(source.id, request.signal);
        if (request.signal.aborted) return;
        if (result.datasource.version !== source.version) {
          setReassignmentStale(true); setNotice('Datasource settings changed. Refresh to review the current assignment.'); return;
        }
        if (!result.datasource.reassignment && canPreview) {
          const agentItems = await loadAgents(request.signal);
          if (request.signal.aborted) return;
          setAgents(agentItems);
        }
        setSource(result.datasource); setEtag(result.etag);
        setUpdatedAt(clockFormat.format(new Date()));
        if (result.datasource.reassignment) timer = window.setTimeout(() => void poll(), 1000);
      } catch (reason) {
        if (!request.signal.aborted) setError(safeError(reason));
      }
    };
    timer = window.setTimeout(() => void poll(), 1000);
    return () => { request.abort(); window.clearTimeout(timer); };
  }, [source?.id, source?.version, source?.reassignment, selectedID, loading, pending, currentUncertainSave, reassignmentStale, draft.agent_id, source?.agent_id, canPreview]);

  useEffect(() => {
    if (!source || source.reassignment || !canPreview) return;
    const request = new AbortController();
    const deadline = Date.now() + 30_000;
    const timeout = window.setTimeout(() => request.abort(), 30_000);
    void pollProviderReadiness(source.agent_id, request.signal, deadline, (items, state) => {
      setProviders(items);
      setPreviewReadiness(state);
    }).then((state) => {
      if (!request.signal.aborted) setPreviewReadiness(state);
      else if (Date.now() >= deadline) setPreviewReadiness('still_applying');
    }).catch((reason: unknown) => {
      if (request.signal.aborted && Date.now() >= deadline) setPreviewReadiness('still_applying');
      else if (!request.signal.aborted) {
        setPreviewReadiness('error');
        setError(safeError(reason));
      }
    });
    return () => {
      request.abort();
      window.clearTimeout(timeout);
    };
  }, [source?.agent_id, source?.reassignment, canPreview, providerRefresh]);

  const selectedAgent = agents.find((agent) => agent.id === draft.agent_id);
  const connectionAvailable = selectedAgent !== undefined && selectedAgent.enabled && datasourceAgentState(selectedAgent) === 'connected' && (providers.some((provider) => provider.provider === 'prometheus' && provider.availability === 'available' && provider.operations.includes('connection_test'))
    || (selectedAgent.runtime?.capabilities ?? []).some((cap) => cap.provider === 'prometheus' && cap.configuration_formats.includes('prometheus-http-v1') && cap.operations.includes('connection_test')));

  function selectAgent(agentID: string) {
    setDraft((current) => ({ ...current, agent_id: agentID }));
    providerRequest.current?.abort();
    const generation = ++providerGeneration.current;
    if (!agentID) { setProviders([]); return; }
    const request = new AbortController(); providerRequest.current = request;
    void client.providers(agentID, request.signal).then((items) => {
      if (!request.signal.aborted && generation === providerGeneration.current) setProviders(items);
    }).catch((reason: unknown) => {
      if (!request.signal.aborted && generation === providerGeneration.current) setError(safeError(reason));
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (currentUncertainSave) { await retryUncertainSave(); return; }
    if (!DATASOURCE_NAME.test(draft.name)) {
      setNameValidationAttempted(true);
      document.getElementById('datasource-name')?.focus();
      return;
    }
    const problem = validateDraft(draft);
    if (problem) { setError(problem.message); document.getElementById(problem.field)?.focus(); return; }
    const cancelReassignment = Boolean(source?.reassignment && source.agent_id === source.reassignment.target_agent_id && draft.agent_id === source.reassignment.source_agent_id);
    const intent = { input: { ...asWrite(draft, isNew), ...(cancelReassignment ? { cancel_reassignment: true } : {}) }, key: crypto.randomUUID(), ...(isNew ? {} : { etag }), isNew, route: locationKey, cancelReassignment } satisfies SaveIntent;
    saveIntent.current = intent;
    await executeSave(intent);
  }

  async function executeSave(intent: SaveIntent) {
    setPending(true); setError(''); setNotice('');
    const request = new AbortController(); saveController.current = request;
    const activeRequest = () => saveController.current === request && locationKeyRef.current === intent.route && !request.signal.aborted;
    const activeIntent = () => activeRequest() && saveIntent.current === intent;
    try {
      if (admin && !intent.input.agent_id) throw new Error('Choose an assigned agent before saving.');
      const saved = await client.save(intent.input, context, intent.key, intent.etag, request.signal);
      if (!activeIntent()) return;
      saveIntent.current = null; setUncertainSave(false); setNotice(intent.isNew ? 'Datasource created.' : saved.reassignment ? '' : intent.cancelReassignment ? 'Reassignment canceled.' : 'Datasource settings saved.'); setDiagnostic(null);
      setDraft((current) => ({ ...current, password: '', token: '' }));
      if (intent.isNew) { setPending(false); navigate(returnToSLI || continueToSli.current ? `/slis/new?datasource=${encodeURIComponent(saved.id)}` : `/datasources/${saved.id}`); }
      else {
        setSource(saved); setEtag(`"datasource:${saved.id}:${saved.version}"`); setDraft(fromDatasource(saved));
        setProviders([]); setPreviewReadiness('loading'); setProviderRefresh((value) => value + 1);
      }
    } catch (reason) {
      if (!activeIntent()) return;
      if (reason instanceof DatasourceFailure && reason.uncertain) {
        setUncertainSave(true);
        setError(intent.isNew ? 'The create response was lost. Retry the same request to safely recover its result.' : 'The save outcome is unknown. Refresh this datasource to reconcile its current version before continuing.');
      } else {
        saveIntent.current = null; setUncertainSave(false);
        if (intent.input.cancel_reassignment && reason instanceof DatasourceFailure && reason.code === 'conflict') {
          try {
            const [current, agentItems] = await Promise.all([client.get(selectedID!, request.signal), loadAgents(request.signal)]);
            if (!activeRequest()) return;
            setSource(current.datasource); setEtag(current.etag); setDraft(fromDatasource(current.datasource)); setAgents(agentItems); setReassignmentStale(false);
            setProviders([]); setPreviewReadiness('loading'); setProviderRefresh((value) => value + 1);
            setNotice(current.datasource.reassignment ? 'The assignment changed. Showing its current status.' : 'The move completed before cancellation. Showing the current assignment.');
          } catch (refreshError) {
            if (activeRequest()) { setReassignmentStale(true); setError(safeError(refreshError)); }
          }
        } else {
          if (source?.reassignment && reason instanceof DatasourceFailure && ['conflict', 'reassignment_pending'].includes(reason.code)) setReassignmentStale(true);
          setError(reason instanceof Error && !(reason instanceof DatasourceFailure) ? reason.message : safeError(reason));
        }
      }
    }
    finally {
      if (activeRequest()) {
        setDraft((current) => ({ ...current, password: '', token: '' }));
        setPending(false);
      }
      if (saveController.current === request) saveController.current = null;
    }
  }

  async function cancelReassignment() {
    if (!source?.reassignment || !etag || pending || currentUncertainSave || reassignmentStale) return;
    const intent: SaveIntent = { input: { ...asWrite(fromDatasource(source), false), agent_id: source.reassignment.source_agent_id, ...(source.agent_id === source.reassignment.target_agent_id ? { cancel_reassignment: true } : {}) }, key: crypto.randomUUID(), etag, isNew: false, route: locationKey, cancelReassignment: true };
    saveIntent.current = intent; await executeSave(intent);
  }

  async function refreshReassignment() {
    if (!selectedID || pending || currentUncertainSave) return;
    setPending(true); setError('');
    const request = new AbortController(); saveController.current = request;
    const route = locationKey;
    try {
      const [current, agentItems] = await Promise.all([client.get(selectedID, request.signal), canPreview ? loadAgents(request.signal) : Promise.resolve([])]);
      if (request.signal.aborted || locationKeyRef.current !== route) return;
      setSource(current.datasource); setEtag(current.etag); setDraft(fromDatasource(current.datasource)); setAgents(agentItems); setReassignmentStale(false); setUpdatedAt(clockFormat.format(new Date())); setNotice('Datasource status refreshed.');
      setProviders([]); setPreviewReadiness('loading'); setProviderRefresh((value) => value + 1);
    } catch (reason) { if (!request.signal.aborted && locationKeyRef.current === route) setError(safeError(reason)); }
    finally { if (!request.signal.aborted && locationKeyRef.current === route) setPending(false); if (saveController.current === request) saveController.current = null; }
  }

  async function retryUncertainSave() {
    const intent = saveIntent.current;
    if (!intent) { setUncertainSave(false); setError('The original request is unavailable. Refresh the datasource before continuing.'); return; }
    if (!intent.isNew) {
      setPending(true); setError('');
      const request = new AbortController(); saveController.current = request;
      const activeRequest = () => saveController.current === request && locationKeyRef.current === intent.route && !request.signal.aborted;
      const activeIntent = () => activeRequest() && saveIntent.current === intent;
      try {
        const current = await client.get(selectedID!, request.signal);
        if (!activeIntent()) return;
        setSource(current.datasource); setEtag(current.etag); setDraft(fromDatasource(current.datasource));
        saveIntent.current = null; setUncertainSave(false); setNotice(intent.cancelReassignment ? current.datasource.reassignment ? 'The reassignment is still pending. Review its current status before trying again.' : 'The datasource was refreshed. Review its current assignment.' : 'The datasource was refreshed after an uncertain save. Review its current version and settings.');
      } catch (reason) { if (activeIntent()) setError(safeError(reason)); }
      finally {
        if (activeRequest()) setPending(false);
        if (saveController.current === request) saveController.current = null;
      }
      return;
    }
    await executeSave(intent);
  }

  async function runDiagnostic(kind: 'connection' | 'preview') {
    if (source?.reassignment) return;
    if (kind === 'preview' && previewReadiness !== 'available') return;
    setPending(true); setError(''); setNotice(''); setDiagnostic(null);
    const request = new AbortController(); diagnosticController.current = request;
    let deadlineTimer: number | undefined;
    try {
      const settings = asWrite(draft, isNew);
      const connectionInput = { provider: 'prometheus' as const, configuration_format: 'prometheus-http-v1' as const, endpoint: settings.endpoint, tls: settings.tls, authentication: settings.authentication, agent_id: settings.agent_id };
      setDraft((current) => ({ ...current, password: '', token: '' }));
      let created: Diagnostic;
      if (kind === 'connection') {
        if (source && !connectionDraftChanged(draft, source)) {
          created = await client.savedConnectionTest(source.id, etag, context, crypto.randomUUID(), request.signal);
        } else {
          if (source && retainsConfiguredSecret(draft)) {
            setError('Save the connection settings before testing with the saved credential, or replace the credential to test unsaved settings.');
            return;
          }
          created = await client.connectionTest(connectionInput, context, request.signal);
        }
      } else {
        const readinessDeadline = Date.now() + 30_000;
        let attempts = 0;
        while (true) {
          try {
            created = await client.preview(selectedID!, query, context, request.signal);
            break;
          } catch (reason) {
            if (!(reason instanceof DatasourceFailure) || reason.code !== 'configuration_pending') throw reason;
            setPreviewReadiness('applying');
            setNotice('No preview was started. The agent is applying datasource settings; waiting briefly before retrying this query.');
            let latestProvider: Provider | undefined;
            const state = await pollProviderReadiness(source!.agent_id, request.signal, readinessDeadline, (items, nextState) => {
              setProviders(items);
              setPreviewReadiness(nextState);
              latestProvider = items.find((provider) => provider.provider === 'prometheus' && provider.configuration_format === 'prometheus-http-v1');
            });
            if (state !== 'available' || attempts >= 2) {
              setError(state === 'available'
                ? 'The agent is ready, but the preview could not be admitted. Retry the preview.'
                : state === 'still_applying'
                ? 'No preview was started. The agent is still applying datasource settings. Check readiness and retry when it is ready.'
                : providerReadinessMessage(state, latestProvider));
              return;
            }
            attempts += 1;
          }
        }
      }
      if (request.signal.aborted) return;
      if (kind === 'preview') setNotice('');
      setDiagnostic(created);
      const cutoff = Math.min(Date.parse(created.deadline_at), Date.now() + 30_000);
      deadlineTimer = window.setTimeout(() => { setError('The diagnostic deadline passed before a final result arrived.'); request.abort(); }, Math.max(0, cutoff - Date.now()));
      let current = created;
      while (!terminal(current) && Date.now() < cutoff && !request.signal.aborted) {
        await new Promise<void>((resolve) => window.setTimeout(resolve, 700));
        current = await client.diagnostic(created.id, request.signal);
        if (!request.signal.aborted) setDiagnostic(current);
      }
      if (!terminal(current) && !request.signal.aborted) setError('The diagnostic deadline passed before a final result arrived.');
    } catch (reason) { if (!request.signal.aborted) setError(safeError(reason)); }
    finally {
      if (deadlineTimer !== undefined) window.clearTimeout(deadlineTimer);
      if (diagnosticController.current === request) diagnosticController.current = null;
      setPending(false);
    }
  }

  async function cancelDiagnostic() {
    if (!diagnostic || !context.session) return;
    setCancelSubmitting(true); setError('');
    try {
      const current = await client.cancelDiagnostic(diagnostic.id, context);
      setDiagnostic(current);
      if (terminal(current)) diagnosticController.current?.abort();
    }
    catch (reason) { setError(safeError(reason)); }
    finally { setCancelSubmitting(false); }
  }

  const listCrumb = { label: 'Datasources', href: '/datasources' };
  const createHeader = <PageHeader title="New datasource" summary="Three steps: name it, say where Prometheus is, pick the agent that can reach it." breadcrumb={{ ...listCrumb, current: 'New datasource' }} headingRef={heading} />;
  if (loading && (selectedID || isNew || listResultKey.current !== locationKey)) return <>{isNew ? createHeader : <PageTitle headingRef={heading} isList={!selectedID} title={selectedID ? 'Datasource' : 'Datasources'} />}<span className="sr-only" role="status">Opening datasource</span></>;
  if (error && !source && selectedID) return <><PageTitle headingRef={heading} isList={!selectedID} title="Datasources" /><Panel heading="Datasource unavailable"><p ref={errorSummary} role="alert" tabIndex={-1}>{error}</p><Button variant="outline" onClick={() => navigate('/datasources')}>Back to datasources</Button></Panel></>;
  // A datasource missing from a complete read has no SLIs; missing from a truncated read it is unknown.
  const usageOf = (id: string) => sliUsage?.get(id) ?? (sliUsage && !sliUsageTruncated ? { count: 0, names: [] } : null);
  const usageCount = (usage: { count: number }) => `${usage.count}${sliUsageTruncated ? '+' : ''}`;
  const agentName = (id: string) => agents.find((agent) => agent.id === id)?.display_name ?? null;

  if (!selectedID && !isNew) {
    const currentItems = listResultKey.current === locationKey ? items : [];
    const term = listSearch.trim().toLowerCase();
    const rows = currentItems.map((item) => {
      const usage = usageOf(item.id);
      return { item, vocabulary: canPreview ? datasourceVocabulary(item, agents, usage === null ? null : usage.count > 0) : null, usage };
    });
    const attention = rows.filter((row) => row.vocabulary?.attention).length;
    const unused = rows.filter((row) => row.usage?.count === 0).length;
    const visible = rows.filter((row) => (!term || `${row.item.display_name} ${row.item.name} ${row.item.endpoint} ${agentName(row.item.agent_id) ?? ''}`.toLowerCase().includes(term))
      && (healthFilter === 'all' || (healthFilter === 'attention' ? row.vocabulary?.attention : row.usage?.count === 0)));
    // Refresh stays available when the list is empty or its first read failed, so new records can be found without a reload.
    const refresh = <RefreshButton label="Refresh datasources" busy={loading} lastRefreshed={updatedAt} onClick={() => loadList(cursor, pageSize)} />;
    return <>
      <PageHeader title="Datasources" headingRef={heading} actions={admin ? <Button onClick={() => navigate('/datasources/new')}>New datasource</Button> : undefined} />
      {error && <p ref={errorSummary} role="alert" tabIndex={-1} className="datasource-alert">{error}</p>}{notice && <p role="status" className="datasource-notice">{notice}</p>}
      {loading && <span className="sr-only" role="status">Refreshing datasources</span>}
      {currentItems.length === 0 && error && listResultKey.current !== locationKey ? <Toolbar><ToolbarSpacer />{refresh}</Toolbar> : currentItems.length === 0 ? <><Toolbar><ToolbarSpacer />{refresh}</Toolbar><Panel heading="No datasources yet"><p>{admin ? 'Add a Prometheus datasource to connect an agent.' : 'No datasources are available to your account.'}</p></Panel></> : <>
        <Toolbar>
          <SearchField id="datasource-search" label="Filter datasources" placeholder="Filter by name, endpoint or agent" value={listSearch} onChange={(event) => setListSearch(event.target.value)} />
          <SegmentedControl label="Filter by health" value={healthFilter} onChange={setHealthFilter} options={[{ value: 'all', label: 'All', count: rows.length }, ...(admin ? [{ value: 'attention' as const, label: 'Needs attention', count: attention }] : []), ...(sliUsage ? [{ value: 'unused' as const, label: 'Unused', count: unused }] : [])]} />
          <ToolbarSpacer />{refresh}
        </Toolbar>
        {visible.length === 0 ? <p className="datasource-empty-filter">No datasources match these filters on this page.</p> : <table className="ds-table ui-list-table" aria-label="Datasources">
          <thead><tr><th scope="col">Datasource</th>{canPreview && <th scope="col">Health</th>}{canPreview && <th scope="col">Agent</th>}<th scope="col">Cadence</th><th scope="col" className="num">SLIs</th></tr></thead>
          <tbody>{visible.map(({ item, vocabulary, usage }) => <tr key={item.id} className="ds-row">
            <td data-label="Datasource" className="ds-row__name"><a href={`/datasources/${item.id}`} data-app-route onClick={(event) => { event.preventDefault(); navigate(`/datasources/${item.id}`); }}>{item.display_name}</a><span className="ds-row__sub ui-list-table__sub">Prometheus · {item.endpoint}{item.tls.insecure_skip_verify && <span className="ui-pill ds-pill--warning">TLS unverified</span>}</span></td>
            {canPreview && vocabulary && <td data-label="Health"><StatusChip tone={vocabulary.tone} label={vocabulary.label} reason={vocabulary.reason} /></td>}
            {canPreview && <td data-label="Agent" className="mono">{item.reassignment ? `${agentName(item.reassignment.source_agent_id) ?? '…'} → ${agentName(item.reassignment.target_agent_id) ?? '…'}` : agentName(item.agent_id) ?? 'Unknown'}</td>}
            <td data-label="Cadence" className="mono">{item.polling_interval_seconds} s</td>
            <td data-label="SLIs" className="num mono">{usage ? usageCount(usage) : '—'}</td>
          </tr>)}</tbody>
        </table>}
        <Pager id="datasource-page-size" label="Datasource result pages" position={`Showing ${visible.length} of ${currentItems.length}${nextCursor || cursor ? ' on this page' : ''}`} pageSize={pageSize} onPageSizeChange={(size) => { setPageSize(size); loadList('', size, []); }} hasPrevious={previousCursors.length > 0} hasNext={Boolean(nextCursor)} busy={loading}
          onPrevious={() => { const previous = previousCursors[previousCursors.length - 1] ?? ''; loadList(previous, pageSize, previousCursors.slice(0, -1)); }}
          onNext={() => loadList(nextCursor, pageSize, [...previousCursors, cursor])} />
      </>}
    </>;
  }

  const readonly = !admin;
  const formDisabled = readonly || pending || currentUncertainSave || Boolean(source?.reassignment);
  const selectedUsage = source ? usageOf(source.id) : null;
  const forCollection = source && selectedUsage === null ? null : (selectedUsage?.count ?? 0) > 0;
  const vocabulary = source && canPreview ? datasourceVocabulary(source, agents, forCollection) : null;
  const sourceAgent = source ? agents.find((agent) => agent.id === source.agent_id) : undefined;
  const prometheusProvider = providers.find((provider) => provider.provider === 'prometheus' && provider.configuration_format === 'prometheus-http-v1');
  const dirty = source ? draft.agent_id !== source.agent_id || connectionDraftChanged(draft, source) || draft.display_name !== source.display_name || Number(draft.polling_interval_seconds) !== source.polling_interval_seconds : false;
  const agentOptions = agents.map((agent) => agentOption(agent, forCollection));
  const agentPicker = <Combobox id="datasource-agent" label="Assigned agent" options={agentOptions} value={draft.agent_id || null} onChange={(id) => selectAgent(id ?? '')} disabled={!canPreview || pending || currentUncertainSave || reassignmentStale} placeholder="Type an agent name…" hint={source ? 'Saving immediately assigns this datasource to the new agent. Results from the previous agent are no longer accepted.' : 'The agent runs every query for this datasource, so it must have network access to the endpoint.'} />;
  const secretField = (kind: 'password' | 'token') => {
    const action = kind === 'password' ? draft.passwordAction : draft.tokenAction;
    const configured = kind === 'password' ? draft.passwordConfigured : draft.tokenConfigured;
    const value = kind === 'password' ? draft.password : draft.token;
    const label = kind === 'password' ? 'Password' : 'Token';
    const setAction = (next: Draft['passwordAction']) => setDraft((current) => kind === 'password' ? { ...current, passwordAction: next, password: '' } : { ...current, tokenAction: next, token: '' });
    if (configured && action !== 'replace') return <div className="ui-field"><span className="ui-label">{label}</span><div className={`ds-secret ${action === 'clear' ? 'ds-secret--clearing' : ''}`.trim()}>
      <StatusMark tone={action === 'clear' ? 'warning' : 'healthy'} label={action === 'clear' ? 'Will be cleared on save' : 'Set'} />
      <span className="ds-secret__sub">{action === 'clear' ? 'the saved value is removed when you save' : `saved ${source ? new Date(source.updated_at).toLocaleDateString(undefined, { dateStyle: 'medium' }) : ''} · never shown`}</span>
      <span className="ds-secret__actions">{action === 'clear' ? <button type="button" className="ui-summary__action" disabled={formDisabled} onClick={() => setAction('retain')}>Keep saved {label.toLowerCase()}</button> : <><button type="button" className="ui-summary__action" disabled={formDisabled} onClick={() => setAction('replace')}>Replace</button><button type="button" className="ui-summary__action" disabled={formDisabled} onClick={() => setAction('clear')}>Clear</button></>}</span>
    </div></div>;
    return <div className="ds-secret-input"><TextField id={`datasource-${kind}`} label={label} type="password" autoComplete="new-password" value={value} disabled={formDisabled} hint={configured ? 'Replaces the saved value when you save. Never shown again.' : 'Encrypted at rest and sent only to the assigned agent. Never shown again.'} onChange={(event) => { const next = event.target.value; setDraft((current) => kind === 'password' ? { ...current, password: next, passwordAction: 'replace' } : { ...current, token: next, tokenAction: 'replace' }); }} />{configured && <button type="button" className="ui-summary__action" disabled={formDisabled} onClick={() => setAction('retain')}>Keep saved {label.toLowerCase()}</button>}</div>;
  };
  const connectionFields = <>
    <div className="ui-form-grid">
      <TextField id="datasource-display-name" label="Display name" value={draft.display_name} disabled={formDisabled} hint={isNew ? 'Name the system, not the URL.' : undefined} onChange={(event) => {
        const display_name = event.target.value;
        setDraft((current) => ({ ...current, display_name, ...(isNew && !current.nameEdited ? { name: suggestDatasourceName(display_name) } : {}) }));
      }} />
      <TextField id="datasource-name" label="Identifier" hint={source ? 'Fixed after creation.' : 'Used in the API and CLI. Suggested from the display name; fixed after creation.'} error={(draft.nameEdited || nameValidationAttempted) && !DATASOURCE_NAME.test(draft.name) ? 'Use 1–63 characters: start with a lowercase letter, then use lowercase letters, numbers, or hyphens.' : undefined} value={draft.name} disabled={formDisabled || Boolean(source)} autoComplete="off" onChange={(event) => setDraft((current) => ({ ...current, name: event.target.value, nameEdited: true }))} />
    </div>
  </>;
  const providerChoice = isNew
    ? <div className="ui-field"><span className="ui-label" id="datasource-provider-label">Provider</span><div role="radiogroup" aria-labelledby="datasource-provider-label" className="ds-providers">
        <button type="button" role="radio" aria-checked="true" className="ds-provider is-selected"><span className="ds-provider__title">Prometheus</span><span className="ds-provider__text">Also VictoriaMetrics, Mimir, Thanos and anything that speaks the Prometheus HTTP API.</span></button>
        <button type="button" role="radio" aria-checked="false" disabled className="ds-provider" title="Not available yet"><span className="ds-provider__title">Tempo</span><span className="ds-provider__text">Trace search with TraceQL.</span><span className="ds-provider__note">Not available yet</span></button>
        <button type="button" role="radio" aria-checked="false" disabled className="ds-provider" title="Not available yet"><span className="ds-provider__title">Loki</span><span className="ds-provider__text">Log metrics with LogQL.</span><span className="ds-provider__note">Not available yet</span></button>
      </div></div>
    : <div className="ui-field"><span className="ui-label">Provider</span><div className="ds-provider-fixed"><strong>Prometheus</strong><span>fixed after creation · create a new datasource to use another provider</span></div></div>;
  const connectionDetails = <>
    <TextField id="datasource-endpoint" className="datasource-endpoint" label="Prometheus endpoint" type="url" hint="The base URL the agent will call, for example https://prometheus.internal:9090. No credentials in the URL." value={draft.endpoint} disabled={formDisabled} onChange={(event) => setDraft({ ...draft, endpoint: event.target.value })} />
    <div className="ui-field"><span className="ui-label" id="datasource-auth-label">Authentication</span><div className="ds-authbox">
      <SegmentedControl label="Authentication" value={draft.authMode} onChange={(authMode) => { if (formDisabled) return; setDraft({ ...draft, authMode, ...(authMode === 'basic' && !draft.passwordConfigured ? { passwordAction: 'replace' as const } : {}), ...(authMode === 'bearer' && !draft.tokenConfigured ? { tokenAction: 'replace' as const } : {}) }); }} options={[{ value: 'none', label: 'None', disabled: formDisabled }, { value: 'basic', label: 'Basic', disabled: formDisabled }, { value: 'bearer', label: 'Bearer token', disabled: formDisabled }]} />
      {draft.authMode === 'basic' && <div className="ui-form-grid ds-authfields"><TextField id="datasource-username" label="Username" value={draft.username} disabled={formDisabled} autoComplete="off" onChange={(event) => setDraft({ ...draft, username: event.target.value })} />{secretField('password')}</div>}
      {draft.authMode === 'bearer' && <div className="ui-form-grid ds-authfields">{secretField('token')}</div>}
    </div></div>
    <div className="ui-form-grid">
      <ChoiceChips id="datasource-polling" label="Query every" value={String(draft.polling_interval_seconds)} disabled={formDisabled} onChange={(value) => setDraft({ ...draft, polling_interval_seconds: Number(value) })} options={CADENCE_PRESETS.map(([value, label]) => ({ value, label }))} custom={{ unit: 'seconds', placeholder: 'e.g. 45', inputMode: 'numeric', format: (value) => `${value} s`, validate: (value) => /^\d+$/.test(value) && Number(value) >= 15 && Number(value) <= 86400 ? undefined : 'Enter whole seconds from 15 to 86400.' }} hint={source && selectedUsage?.count ? `Every SLI on this datasource measures one interval of this length; ${selectedUsage.count} SLI${selectedUsage.count === 1 ? '' : 's'} use it.` : 'Every SLI on this datasource measures one interval of this length.'} />
      <div className="ui-field"><span className="ui-label">TLS</span><SegmentedControl label="TLS" value={tlsMode(draft.tls)} onChange={(mode) => { if (formDisabled) return; setDraft({ ...draft, tls: mode === 'skip' ? { ...draft.tls, insecure_skip_verify: true } : mode === 'ca' ? { insecure_skip_verify: false, ca_pem: draft.tls.ca_pem ?? '' } : { insecure_skip_verify: false, ca_pem: null } }); }} options={[{ value: 'verify', label: 'Verify certificate', disabled: formDisabled }, { value: 'ca', label: 'Custom CA', disabled: formDisabled }, { value: 'skip', label: 'Skip verification', disabled: formDisabled }]} /><p className="ui-hint">{tlsMode(draft.tls) === 'skip' ? 'Insecure: the certificate is not checked and the datasource is marked in lists.' : tlsMode(draft.tls) === 'ca' ? 'Paste the PEM bundle that signs the endpoint’s certificate.' : 'Verifying against the system roots.'}</p></div>
    </div>
    {tlsMode(draft.tls) === 'ca' && <TextareaField id="datasource-ca" label="Custom CA PEM" rows={4} value={draft.tls.ca_pem ?? ''} disabled={formDisabled} className="mono" onChange={(event) => setDraft({ ...draft, tls: { ...draft.tls, ca_pem: event.target.value } })} />}
  </>;
  const testTools = admin && <Button variant="outline" pending={pending} disabled={!connectionAvailable || !draft.agent_id || currentUncertainSave || Boolean(source?.reassignment)} onClick={() => void runDiagnostic('connection')}><span aria-hidden="true">▶ </span>Test connection</Button>;
  const testAvailability = source?.reassignment ? 'Waiting for the agent to acknowledge the configuration.' : connectionAvailable ? 'Runs on the assigned agent.' : datasourceAvailabilityMessage(selectedAgent, agents);
  const diagnosticResult = diagnostic && <div className="ds-preview"><h3>{diagnostic.operation === 'connection_test' ? 'Connection test' : 'Query preview'}</h3><DiagnosticView value={diagnostic} /></div>;
  const eligibilityAlert = isNew && admin && !agents.some(supportsDatasourceDiagnostics) && <p className="datasource-alert">{agents.length === 0 ? 'Register and start an Almanac agent before adding a datasource.' : agents.some((agent) => agent.enabled && datasourceAgentState(agent) === 'unreported') ? 'Start your registered agent before assigning this datasource.' : datasourceAvailabilityMessage(undefined, agents)} <Button variant="outline" onClick={() => navigate('/agents')}>View agents</Button></p>;

  if (isNew) {
    return <>
      {createHeader}
      {error && <p ref={errorSummary} role="alert" tabIndex={-1} className="datasource-alert">{error}</p>}{notice && <p role="status" className="datasource-notice">{notice}</p>}
      {readonly ? <Panel heading="Access required"><p>Administrators create datasources and manage connection settings and credentials.</p></Panel> : <form className="ui-form ds-form" onSubmit={(event) => void submit(event)} noValidate>
        <FormSection step={1} title="Identity" hint="What is this source?">{connectionFields}</FormSection>
        <FormSection step={2} title="Connection" hint="Where and how to reach it?">{providerChoice}{connectionDetails}</FormSection>
        <FormSection step={3} title="Agent" hint="Which agent can reach it?" tools={testTools}>
          {eligibilityAlert}
          {agentPicker}
          <p className="ui-form-note"><span>{testAvailability}</span> Tests run on the agent and do not save anything.</p>
          {diagnosticResult}
        </FormSection>
        <FormActions note="The agent receives the configuration as soon as you save.">
          <Button variant="outline" onClick={() => navigate(returnToSLI ? '/slis/new' : '/datasources')}>{returnToSLI ? 'Back to SLI' : 'Cancel'}</Button>
          <Button type="submit" variant={returnToSLI ? 'primary' : 'outline'} pending={pending && !createAndAddSli} disabled={pending && createAndAddSli} onClick={() => { continueToSli.current = false; setCreateAndAddSli(false); }}>{currentUncertainSave ? 'Retry same create' : 'Create datasource'}</Button>
          {!returnToSLI && !currentUncertainSave && <Button type="submit" pending={pending && createAndAddSli} disabled={pending && !createAndAddSli} onClick={() => { continueToSli.current = true; setCreateAndAddSli(true); }}>Create and add an SLI</Button>}
        </FormActions>
      </form>}
    </>;
  }

  const reassignment = source?.reassignment;
  return <>
    <div className="ds-head">
      <div className="ds-head__copy">
        <nav className="app-breadcrumbs" aria-label="Breadcrumb"><a href="/datasources" data-app-route onClick={(event) => { event.preventDefault(); navigate('/datasources'); }}>Datasources</a><span aria-hidden="true">/</span><span aria-current="page">{source?.display_name ?? 'Datasource'}</span></nav>
        <h1 ref={heading} className="app-page-heading ds-head__title" tabIndex={-1}>{source?.display_name ?? 'Datasource'}</h1>
        {vocabulary && <p className="ds-head__status"><StatusMark tone={vocabulary.tone} label={`${vocabulary.label} · ${vocabulary.reason}`} /></p>}
        {source && <p className="ds-head__meta">Prometheus · <span className="mono">{source.endpoint}</span> · every {source.polling_interval_seconds} s{selectedUsage ? ` · ${usageCount(selectedUsage)} SLI${selectedUsage.count === 1 ? '' : 's'}` : ''} · version {source.version}</p>}
      </div>
      <div className="ds-head__actions"><RefreshButton label="Refresh datasource" busy={pending} disabled={currentUncertainSave} lastRefreshed={updatedAt} onClick={() => void refreshReassignment()} />{testTools}</div>
    </div>
    {error && <p ref={errorSummary} role="alert" tabIndex={-1} className="datasource-alert">{error}</p>}{notice && <p role="status" className="datasource-notice">{notice}</p>}
    {reassignment && <section className="ds-banner" aria-labelledby="ds-updating-title"><h2 id="ds-updating-title">{reassignment.state === 'replacement_unavailable' ? 'Agent unavailable' : 'Updating datasource'}</h2>{reassignment.state === 'replacement_unavailable' ? <p>{agentName(reassignment.target_agent_id) ?? 'The replacement collector'} is unavailable. Choose another agent below or refresh after it recovers.</p> : <p>{`Waiting for ${agentName(reassignment.target_agent_id) ?? 'the replacement collector'} to apply its configuration.`}</p>}<p>The replacement can start without waiting for the previous agent. Results submitted by the previous agent are rejected.</p><div className="ds-banner__actions"><Button variant="outline" disabled={pending || currentUncertainSave} onClick={() => void refreshReassignment()}>Refresh reassignment</Button>{canPreview && <Button variant="outline" disabled={pending || currentUncertainSave || reassignmentStale} onClick={() => void cancelReassignment()}>{source?.agent_id === reassignment.target_agent_id ? 'Move back to previous agent' : 'Cancel reassignment'}</Button>}</div></section>}
    {!reassignment && sourceAgent && datasourceAgentState(sourceAgent, forCollection) === 'offline' && <section className="ds-banner" aria-label="Agent unreachable"><p>The assigned agent <b>{sourceAgent.display_name}</b> has not sent a heartbeat{sourceAgent.runtime?.last_heartbeat_at ? ` since ${clockFormat.format(new Date(sourceAgent.runtime.last_heartbeat_at))} UTC` : ''}. {selectedUsage?.count ? `Its ${selectedUsage.count} SLI${selectedUsage.count === 1 ? ' is' : 's are'} not collecting; intervals since then will show as missing. ` : ''}<a href={`/agents?agent=${sourceAgent.id}`} data-app-route>Open the agent</a> or move this datasource to another agent below.</p></section>}
    {source && <div className="ds-tiles">
      <div className="ds-tile"><span className="ds-tile__k">Agent</span><span className="ds-tile__v">{canPreview ? <StatusMark tone={sourceAgent ? agentOption(sourceAgent, forCollection).tone ?? 'none' : 'none'} label={sourceAgent?.display_name ?? 'Unknown'} /> : 'Assigned'}</span><span className="ds-tile__s">{canPreview ? (sourceAgent?.runtime?.last_heartbeat_at ? `last heartbeat ${clockFormat.format(new Date(sourceAgent.runtime.last_heartbeat_at))} UTC · ${relativeAge(sourceAgent.runtime.last_heartbeat_at)}` : sourceAgent ? agentOption(sourceAgent, forCollection).status : 'assigned agent not found') : 'agent details need editor or administrator access'}</span></div>
      <div className="ds-tile"><span className="ds-tile__k">Configuration</span><span className="ds-tile__v">{reassignment ? reassignment.state === 'replacement_unavailable' ? 'not confirmed' : 'updating' : canPreview ? previewReadiness === 'loading' ? 'checking…' : previewReadiness === 'available' ? `applied${prometheusProvider?.applied_configuration_revision ? ` · rev ${prometheusProvider.applied_configuration_revision}` : ''}` : previewReadiness === 'applying' || previewReadiness === 'still_applying' ? `applying${prometheusProvider?.desired_configuration_revision ? ` · rev ${prometheusProvider.applied_configuration_revision ?? '—'} of ${prometheusProvider.desired_configuration_revision}` : ''}` : previewReadiness.replaceAll('_', ' ') : 'not checked'}</span><span className="ds-tile__s">{reassignment ? reassignment.state === 'replacement_unavailable' ? 'assigned agent unavailable' : 'waiting for configuration acknowledgement' : canPreview ? previewReadiness === 'available' ? 'agent has the current settings' : previewReadiness === 'loading' ? 'reading agent readiness' : 'agent does not confirm the current settings' : 'editor or administrator access required'}</span></div>
      <div className="ds-tile"><span className="ds-tile__k">Collection</span><span className="ds-tile__v">every {source.polling_interval_seconds} s</span><span className="ds-tile__s">auth {source.authentication.mode}{source.tls.insecure_skip_verify ? ' · TLS unverified' : source.tls.ca_pem ? ' · custom CA' : ''}</span></div>
      <div className="ds-tile"><span className="ds-tile__k">Used by</span><span className="ds-tile__v">{selectedUsage ? `${usageCount(selectedUsage)} SLI${selectedUsage.count === 1 ? '' : 's'}` : '—'}</span><span className="ds-tile__s">{selectedUsage ? selectedUsage.count === 0 ? 'no SLI uses it yet' : `${selectedUsage.names.join(', ')}${selectedUsage.count > selectedUsage.names.length ? `, +${selectedUsage.count - selectedUsage.names.length}` : ''}` : 'reading SLIs'}</span></div>
    </div>}
    <form className="ui-form ds-form" onSubmit={(event) => void submit(event)} noValidate>
      <FormSection title="Connection" hint="Where and how to reach Prometheus">{connectionFields}{providerChoice}{connectionDetails}{readonly && <p className="ui-form-note">Your role can view redacted connection settings. Administrators manage connection settings and credentials. Editors can change the assigned agent.</p>}</FormSection>
      {canPreview && <FormSection title="Agent" hint="Which agent runs the queries">{agentPicker}</FormSection>}
      {source && canPreview && <FormSection title="Test" hint="Runs on the assigned agent · nothing is saved">
        {admin && <p className="ui-form-note">{testAvailability}</p>}
        {!reassignment && <>
          <p role="status" className={previewReadiness === 'loading' ? 'sr-only' : 'datasource-readiness'}>{providerReadinessMessage(previewReadiness, prometheusProvider)}</p>
          {previewReadiness !== 'available' && previewReadiness !== 'loading' && <div><Button variant="outline" disabled={pending} onClick={() => { setPreviewReadiness('loading'); setError(''); setProviderRefresh((value) => value + 1); }}>Check readiness</Button></div>}
          <QueryEditor id="datasource-query" label="PromQL query" value={query} disabled={pending} onChange={setQuery} rows={2} />
          <p className="ui-hint">Returns the latest value per series; use it to check labels before writing SLI queries. Results are diagnostic only and do not create observations.</p>
          <div className="ds-banner__actions"><Button pending={pending} disabled={pending || previewReadiness !== 'available'} onClick={() => void runDiagnostic('preview')}><span aria-hidden="true">▶ </span>Run preview</Button>{diagnostic && !terminal(diagnostic) && <Button variant="outline" pending={cancelSubmitting} disabled={cancelSubmitting} onClick={() => void cancelDiagnostic()}>Cancel diagnostic</Button>}</div>
        </>}
        {diagnosticResult}
      </FormSection>}
      {canPreview && <FormActions note="Changing agent sends configuration immediately; Updating clears when the agent acknowledges it. Collection freshness is shown separately.">
        <Button variant="outline" disabled={!dirty || pending || currentUncertainSave} onClick={() => { if (source) setDraft(fromDatasource(source)); }}>Discard changes</Button>
        <Button type="submit" pending={pending} disabled={Boolean(reassignment) && draft.agent_id === source?.agent_id && !currentUncertainSave}>{currentUncertainSave ? 'Reconcile save' : 'Save settings'}</Button>
      </FormActions>}
    </form>
  </>;
}

function DiagnosticView({ value }: { value: Diagnostic }) {
  const labels: Record<Diagnostic['state'], string> = {
    pending: 'Pending', running: 'Running', succeeded: 'Succeeded', failed: 'Failed',
    canceled: 'Canceled', timed_out: 'Timed out', unknown: 'Unknown',
  };
  const tone: Record<Diagnostic['state'], MarkTone> = { pending: 'none', running: 'none', succeeded: 'healthy', failed: 'breached', canceled: 'stale', timed_out: 'breached', unknown: 'stale' };
  return <div className="diagnostic-result" aria-live="polite"><div><StatusMark tone={tone[value.state]} label={labels[value.state]} />{value.result_class && <span className={`ui-pill datasource-result-class datasource-result-class--${value.result_class}`}>{value.result_class}</span>}<span className="diagnostic-result__deadline">deadline {new Date(value.deadline_at).toLocaleTimeString()}</span></div>{value.message && <p>{value.message}</p>}{value.error_code && <p>Code: <code>{value.error_code}</code></p>}{value.result && <pre>{JSON.stringify(value.result, null, 2)}</pre>}<span className="sr-only">Diagnostic {labels[value.state].toLowerCase()}</span></div>;
}

function PageTitle({ title, headingRef, isList = false }: { title: string; headingRef: RefObject<HTMLHeadingElement | null>; isList?: boolean }) {
  return <div className="app-page-context">
    <nav className="app-breadcrumbs" aria-label="Breadcrumb">{isList ? <span aria-current="page">Datasources</span> : <><a href="/datasources" data-app-route>Datasources</a><span aria-hidden="true">/</span><span aria-current="page">{title}</span></>}</nav>
    <h1 ref={headingRef} className="app-page-heading" tabIndex={-1}>{title}</h1>
  </div>;
}
