import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type RefObject } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { Button, LoadingIndicator, Panel, SelectField, StatusBadge, TextField } from '../components/ui/index.ts';
import { createDatasourceClient, DatasourceFailure, type Datasource, type DatasourceWrite, type Diagnostic, type Provider } from './client.ts';
import './datasources.css';

type Agent = { id: string; name: string; display_name: string; enabled: boolean; runtime?: { reachability?: string; capabilities?: { provider: string; configuration_formats: string[]; operations: string[] }[] } | null };
type ProviderReadinessState = Provider['availability'] | 'loading' | 'still_applying' | 'error';
type Draft = Omit<DatasourceWrite, 'authentication'> & { authMode: 'none' | 'basic' | 'bearer'; username: string; password: string; token: string; passwordAction: 'replace' | 'retain' | 'clear'; tokenAction: 'replace' | 'retain' | 'clear'; passwordConfigured: boolean; tokenConfigured: boolean };
type SaveIntent = { input: DatasourceWrite; key: string; etag?: string; isNew: boolean };

const client = createDatasourceClient();
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const object = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value);
const emptyDraft = (): Draft => ({ name: '', display_name: '', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: '', tls: { insecure_skip_verify: false, ca_pem: null }, polling_interval_seconds: 60, agent_id: '', authMode: 'none', username: '', password: '', token: '', passwordAction: 'retain', tokenAction: 'retain', passwordConfigured: false, tokenConfigured: false });
const safeError = (error: unknown) => error instanceof DatasourceFailure ? error.message : 'Datasource management is temporarily unavailable.';
const terminal = (value: Diagnostic) => ['succeeded', 'failed', 'canceled', 'timed_out', 'unknown'].includes(value.state);

async function loadAgents(signal: AbortSignal): Promise<Agent[]> {
  let response: Response;
  try { response = await fetch('/api/v1/agents?limit=200', { credentials: 'same-origin', cache: 'no-store', signal }); }
  catch { throw new Error('Agent assignments could not be loaded.'); }
  if (!response.ok) throw new Error(response.status === 403 ? 'Administrator access is required to manage agent assignments.' : 'Agent assignments could not be loaded.');
  const value: unknown = await response.json();
  if (!object(value) || !Array.isArray(value.items) || value.items.length > 200 || !value.items.every((item) => object(item) && typeof item.id === 'string' && UUID.test(item.id) && typeof item.display_name === 'string' && typeof item.enabled === 'boolean')) {
    throw new Error('The server returned invalid agent assignment data.');
  }
  return value.items as Agent[];
}

type DatasourceAgentState = 'disabled' | 'unreported' | 'incompatible' | 'unsupported' | 'offline' | 'connected';

function datasourceAgentState(agent: Agent): DatasourceAgentState {
  if (!agent.enabled) return 'disabled';
  const runtime = agent.runtime;
  if (!runtime) return 'unreported';
  if (runtime.reachability === 'incompatible') return 'incompatible';
  const capabilities = Array.isArray(runtime.capabilities) ? runtime.capabilities : [];
  if (capabilities.length === 0) return 'unreported';
  const supported = capabilities.some((capability) => capability.provider === 'prometheus'
    && capability.configuration_formats.includes('prometheus-http-v1')
    && capability.operations.includes('connection_test') && capability.operations.includes('query_preview'));
  if (!supported) return 'unsupported';
  if (runtime.reachability === 'connected') return 'connected';
  if (runtime.reachability === 'unreachable') return 'offline';
  return 'unreported';
}

function supportsDatasourceDiagnostics(agent: Agent): boolean {
  const state = datasourceAgentState(agent);
  return state === 'connected' || state === 'offline';
}

function datasourceAgentLabel(agent: Agent): string {
  const labels: Record<DatasourceAgentState, string> = {
    disabled: 'disabled', unreported: 'capabilities not reported', incompatible: 'incompatible protocol',
    unsupported: 'diagnostics unsupported', offline: 'offline', connected: 'connected',
  };
  return `${agent.display_name} · ${labels[datasourceAgentState(agent)]}`;
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
  return { name: draft.name.trim(), display_name: draft.display_name.trim(), provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: draft.endpoint.trim(), tls: draft.tls,
      authentication: { mode: draft.authMode, username: draft.authMode === 'basic' ? draft.username : null, password_secret: secret(passwordAction, draft.password), token_secret: secret(tokenAction, draft.token) },
    polling_interval_seconds: Number(draft.polling_interval_seconds), agent_id: draft.agent_id };
}

function fromDatasource(source: Datasource): Draft {
  return { ...emptyDraft(), name: source.name, display_name: source.display_name, endpoint: source.endpoint, tls: source.tls, polling_interval_seconds: source.polling_interval_seconds, agent_id: source.agent_id,
    authMode: source.authentication.mode, username: source.authentication.username ?? '', passwordConfigured: source.authentication.password_configured, tokenConfigured: source.authentication.token_configured };
}

export function DatasourcesPage({ context }: { context: AuthContext }) {
  const role = context.session?.role.toLowerCase() ?? 'viewer';
  const admin = role === 'administrator';
  const canPreview = admin || role === 'editor';
  const [locationKey, setLocationKey] = useState(() => `${location.pathname}${location.search}`);
  const selectedID = useMemo(() => /^\/datasources\/([0-9a-f-]{36})$/.exec(new URL(locationKey, location.origin).pathname)?.[1] ?? null, [locationKey]);
  const isNew = new URL(locationKey, location.origin).pathname === '/datasources/new';
  const [items, setItems] = useState<Datasource[]>([]);
  const [cursor, setCursor] = useState('');
  const [nextCursor, setNextCursor] = useState('');
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
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [controller, setController] = useState<AbortController | null>(null);
  const providerRequest = useRef<AbortController | null>(null);
  const providerGeneration = useRef(0);
  const saveIntent = useRef<SaveIntent | null>(null);
  const [uncertainSave, setUncertainSave] = useState(false);
  const heading = useRef<HTMLHeadingElement>(null);
  const errorSummary = useRef<HTMLParagraphElement>(null);
  useEffect(() => () => { saveIntent.current = null; }, []);
  useEffect(() => { if (!loading) heading.current?.focus(); }, [locationKey, loading]);
  useEffect(() => { if (error) errorSummary.current?.focus(); }, [error]);

  const navigate = useCallback((path: string) => {
    history.pushState({}, '', path);
    setLocationKey(`${location.pathname}${location.search}`);
  }, []);
  useEffect(() => {
    const changed = () => setLocationKey(`${location.pathname}${location.search}`);
    window.addEventListener('popstate', changed);
    return () => window.removeEventListener('popstate', changed);
  }, []);

  const loadList = useCallback((next = '') => {
    const request = new AbortController(); setController(request); setLoading(true); setError('');
    void (async () => {
      try {
        const [page, agentItems] = await Promise.all([
          client.list(50, next, request.signal),
          admin ? loadAgents(request.signal) : Promise.resolve([]),
        ]);
        if (request.signal.aborted) return;
        setItems(page.items); setCursor(next); setNextCursor(page.next_cursor); setAgents(agentItems);
      } catch (reason) { if (!request.signal.aborted) setError(safeError(reason)); }
      finally { if (!request.signal.aborted) setLoading(false); }
    })();
    return request;
  }, [admin]);

  useEffect(() => {
    if (selectedID) {
      const request = new AbortController(); setController(request); setLoading(true); setError(''); setNotice('');
      setProviders([]); setPreviewReadiness('loading');
      void client.get(selectedID, request.signal).then(({ datasource, etag: tag }) => {
        if (request.signal.aborted) return [] as Agent[];
        setSource(datasource); setEtag(tag); setDraft(fromDatasource(datasource));
        return admin ? loadAgents(request.signal) : Promise.resolve([] as Agent[]);
      }).then((agentItems) => { if (!request.signal.aborted) setAgents(agentItems); })
        .catch((reason: unknown) => { if (!request.signal.aborted) setError(safeError(reason)); }).finally(() => { if (!request.signal.aborted) setLoading(false); });
      return () => request.abort();
    }
    setSource(null); setEtag(''); setDraft(emptyDraft()); setProviders([]); setPreviewReadiness('loading'); setDiagnostic(null);
    if (!isNew) { const request = loadList(cursor); return () => request.abort(); }
    const request = new AbortController(); setController(request); setLoading(true);
    void loadAgents(request.signal).then(async (result) => {
      if (request.signal.aborted) return;
      const eligible = result.filter(supportsDatasourceDiagnostics);
      setAgents(result); setDraft((current) => ({ ...current, agent_id: eligible[0]?.id ?? '' }));
      if (eligible[0]) setProviders(await client.providers(eligible[0].id, request.signal));
    }).catch((reason: unknown) => { if (!request.signal.aborted) setError(safeError(reason)); }).finally(() => { if (!request.signal.aborted) setLoading(false); });
    return () => request.abort();
  // locationKey is the route state; cursor is intentionally retained by Back.
  }, [locationKey, selectedID, isNew, loadList, admin, canPreview]);

  useEffect(() => {
    if (!source || !canPreview) return;
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
  }, [source?.agent_id, canPreview, providerRefresh]);

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
    if (uncertainSave) { await retryUncertainSave(); return; }
    const intent = { input: asWrite(draft, isNew), key: crypto.randomUUID(), ...(isNew ? {} : { etag }), isNew } satisfies SaveIntent;
    saveIntent.current = intent;
    await executeSave(intent);
  }

  async function executeSave(intent: SaveIntent) {
    setPending(true); setError(''); setNotice('');
    const request = new AbortController(); setController(request);
    try {
      if (admin && !intent.input.agent_id) throw new Error('Choose an assigned agent before saving.');
      const saved = await client.save(intent.input, context, intent.key, intent.etag, request.signal);
      saveIntent.current = null; setUncertainSave(false); setNotice(intent.isNew ? 'Datasource created.' : 'Datasource settings saved.'); setDiagnostic(null);
      setDraft((current) => ({ ...current, password: '', token: '' }));
      if (intent.isNew) navigate('/datasources');
      else {
        setSource(saved); setEtag(`"datasource:${saved.id}:${saved.version}"`); setDraft(fromDatasource(saved));
        setProviders([]); setPreviewReadiness('loading'); setProviderRefresh((value) => value + 1);
      }
    } catch (reason) {
      if (request.signal.aborted) return;
      if (reason instanceof DatasourceFailure && reason.uncertain) {
        setUncertainSave(true);
        setError(intent.isNew ? 'The create response was lost. Retry the same request to safely recover its result.' : 'The save outcome is unknown. Refresh this datasource to reconcile its current version before continuing.');
      } else { saveIntent.current = null; setUncertainSave(false); setError(reason instanceof Error && !(reason instanceof DatasourceFailure) ? reason.message : safeError(reason)); }
    }
    finally {
      setDraft((current) => ({ ...current, password: '', token: '' }));
      if (!request.signal.aborted) setPending(false);
    }
  }

  async function retryUncertainSave() {
    const intent = saveIntent.current;
    if (!intent) { setUncertainSave(false); setError('The original request is unavailable. Refresh the datasource before continuing.'); return; }
    if (!intent.isNew) {
      setPending(true); setError('');
      try {
        const current = await client.get(selectedID!);
        setSource(current.datasource); setEtag(current.etag); setDraft(fromDatasource(current.datasource));
        saveIntent.current = null; setUncertainSave(false); setNotice('The datasource was refreshed after an uncertain save. Review its current version and settings.');
      } catch (reason) { setError(safeError(reason)); }
      finally { setPending(false); }
      return;
    }
    await executeSave(intent);
  }

  async function runDiagnostic(kind: 'connection' | 'preview') {
    if (kind === 'preview' && previewReadiness !== 'available') return;
    setPending(true); setError(''); setNotice(''); setDiagnostic(null);
    const request = new AbortController(); setController(request);
    let deadlineTimer: number | undefined;
    try {
      const settings = asWrite(draft, isNew);
      const connectionInput = { provider: 'prometheus' as const, configuration_format: 'prometheus-http-v1' as const, endpoint: settings.endpoint, tls: settings.tls, authentication: settings.authentication, agent_id: settings.agent_id };
      setDraft((current) => ({ ...current, password: '', token: '' }));
      let created: Diagnostic;
      if (kind === 'connection') {
        created = await client.connectionTest(connectionInput, context, request.signal);
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
    finally { if (deadlineTimer !== undefined) window.clearTimeout(deadlineTimer); setPending(false); }
  }

  async function cancelDiagnostic() {
    if (!diagnostic || !context.session) return;
    setPending(true); setError('');
    try { const current = await client.cancelDiagnostic(diagnostic.id, context); setDiagnostic(current); }
    catch (reason) { setError(safeError(reason)); }
    finally { setPending(false); }
    controller?.abort();
  }

  if (loading) return <div className="datasources-loading"><LoadingIndicator label="Loading datasource management" /></div>;
  if (error && !source && selectedID) return <><PageTitle headingRef={heading} title="Datasources" /><Panel heading="Datasource unavailable"><p ref={errorSummary} role="alert" tabIndex={-1}>{error}</p><Button variant="outline" onClick={() => navigate('/datasources')}>Back to datasources</Button></Panel></>;
  if (!selectedID && !isNew) return <>
    <PageTitle headingRef={heading} title="Datasources" />
    {error && <p ref={errorSummary} role="alert" tabIndex={-1} className="datasource-alert">{error}</p>}{notice && <p role="status" className="datasource-notice">{notice}</p>}
    <div className="datasource-toolbar"><p>Prometheus sources assigned to collection agents.</p>{admin && <Button onClick={() => navigate('/datasources/new')}>Add datasource</Button>}</div>
    {items.length === 0 ? <Panel heading="No datasources yet"><p>{admin ? 'Add a Prometheus datasource to connect an agent.' : 'No datasources are available to your account.'}</p></Panel> : <div className="datasource-list" aria-label="Datasources">
      {items.map((item) => <button className="datasource-row" key={item.id} onClick={() => navigate(`/datasources/${item.id}`)}><span><strong>{item.display_name}</strong><small>{item.endpoint}</small></span><span className="datasource-row__meta"><small>{item.authentication.mode === 'none' ? 'No authentication' : `${item.authentication.mode} · secret ${item.authentication[ item.authentication.mode === 'basic' ? 'password_configured' : 'token_configured' ] ? 'configured' : 'not configured'}`}</small><small>Version {item.version}</small></span></button>)}
    </div>}
    {nextCursor && <div className="datasource-pagination"><Button variant="outline" onClick={() => void loadList(nextCursor)}>Next page</Button>{cursor && <Button variant="outline" onClick={() => void loadList('')}>First page</Button>}</div>}
  </>;

  const readonly = !admin;
  const formDisabled = readonly || pending || uncertainSave;
  return <>
    <PageTitle headingRef={heading} title={isNew ? 'Add datasource' : source?.display_name ?? 'Datasource'} />
    <div className="datasource-toolbar"><Button variant="outline" onClick={() => navigate('/datasources')}>Back to datasources</Button>{source && <p>Version {source.version} · assigned agent {agents.find((agent) => agent.id === source.agent_id)?.display_name ?? source.agent_id}</p>}</div>
    {error && <p ref={errorSummary} role="alert" tabIndex={-1} className="datasource-alert">{error}</p>}{notice && <p role="status" className="datasource-notice">{notice}</p>}
    <Panel heading="Connection settings"><form className="datasource-form" onSubmit={(event) => void submit(event)}>
      <TextField id="datasource-name" label="Name" value={draft.name} disabled={formDisabled} onChange={(event) => setDraft({ ...draft, name: event.target.value })} />
      <TextField id="datasource-display-name" label="Display name" value={draft.display_name} disabled={formDisabled} onChange={(event) => setDraft({ ...draft, display_name: event.target.value })} />
      <TextField id="datasource-endpoint" label="Prometheus endpoint" type="url" hint="Use an HTTP or HTTPS URL without embedded credentials." value={draft.endpoint} disabled={formDisabled} onChange={(event) => setDraft({ ...draft, endpoint: event.target.value })} />
      <SelectField id="datasource-agent" label="Assigned agent" value={draft.agent_id} disabled={formDisabled} onChange={(event) => selectAgent(event.target.value)} options={[{ value: '', label: 'Select an agent', disabled: true }, ...agents.map((agent) => ({ value: agent.id, label: datasourceAgentLabel(agent), disabled: !supportsDatasourceDiagnostics(agent) }))]} />
      <TextField id="datasource-polling" label="Polling interval (seconds)" type="number" min={15} max={86400} value={draft.polling_interval_seconds} disabled={formDisabled} onChange={(event) => setDraft({ ...draft, polling_interval_seconds: Number(event.target.value) })} />
      <SelectField id="datasource-auth-mode" label="Authentication" value={draft.authMode} disabled={formDisabled} onChange={(event) => { const authMode = event.target.value as Draft['authMode']; setDraft({ ...draft, authMode, ...(authMode === 'basic' && !draft.passwordConfigured ? { passwordAction: 'replace' as const } : {}), ...(authMode === 'bearer' && !draft.tokenConfigured ? { tokenAction: 'replace' as const } : {}) }); }} options={[{ value: 'none', label: 'None' }, { value: 'basic', label: 'Basic authentication' }, { value: 'bearer', label: 'Bearer token' }]} />
      {draft.authMode === 'basic' && <><TextField id="datasource-username" label="Username" value={draft.username} disabled={formDisabled} onChange={(event) => setDraft({ ...draft, username: event.target.value })} /><SecretInput id="datasource-password" label="Password" action={draft.passwordAction} configured={draft.passwordConfigured} value={draft.password} disabled={formDisabled} onAction={(passwordAction) => setDraft({ ...draft, passwordAction })} onValue={(password) => setDraft({ ...draft, password })} /></>}
      {draft.authMode === 'bearer' && <SecretInput id="datasource-token" label="Bearer token" action={draft.tokenAction} configured={draft.tokenConfigured} value={draft.token} disabled={formDisabled} onAction={(tokenAction) => setDraft({ ...draft, tokenAction })} onValue={(token) => setDraft({ ...draft, token })} />}
      <label className="datasource-check"><input type="checkbox" checked={draft.tls.insecure_skip_verify} disabled={formDisabled} onChange={(event) => setDraft({ ...draft, tls: { ...draft.tls, insecure_skip_verify: event.target.checked } })} />Skip TLS certificate verification <span>Insecure</span></label>
      <label className="datasource-check datasource-check--wide">Custom CA PEM<textarea rows={4} value={draft.tls.ca_pem ?? ''} disabled={formDisabled} onChange={(event) => setDraft({ ...draft, tls: { ...draft.tls, ca_pem: event.target.value || null } })} /></label>
      {admin && <div className="datasource-actions"><Button type="submit" pending={pending}>{uncertainSave ? (isNew ? 'Retry same create' : 'Reconcile save') : isNew ? 'Create datasource' : 'Save settings'}</Button><Button type="button" variant="outline" pending={pending} disabled={!connectionAvailable || !draft.agent_id || uncertainSave} onClick={() => void runDiagnostic('connection')}>Test connection</Button><span>{connectionAvailable ? 'Runs on the assigned agent.' : datasourceAvailabilityMessage(selectedAgent, agents)}</span></div>}
      {readonly && <p className="datasource-readonly">Your role can view redacted connection settings. Administrators manage credentials and assignments.</p>}
    </form></Panel>
    {source && canPreview && <Panel heading="PromQL preview"><p>Runs against this saved datasource through its assigned agent. Results are diagnostic only and do not create observations.</p><p role="status" className="datasource-readiness">{providerReadinessMessage(previewReadiness, providers.find((provider) => provider.provider === 'prometheus' && provider.configuration_format === 'prometheus-http-v1'))}</p>{(previewReadiness === 'still_applying' || previewReadiness === 'error') && <Button variant="outline" disabled={pending} onClick={() => { setPreviewReadiness('loading'); setError(''); setProviderRefresh((value) => value + 1); }}>Check readiness</Button>}<label className="ui-field"><span className="ui-label" id="datasource-query-label">PromQL query</span><textarea aria-labelledby="datasource-query-label" rows={3} value={query} disabled={pending} onChange={(event) => setQuery(event.target.value)} /></label><div className="datasource-actions"><Button pending={pending} disabled={pending || previewReadiness !== 'available'} onClick={() => void runDiagnostic('preview')}>Run preview</Button>{diagnostic && !terminal(diagnostic) && <Button variant="outline" disabled={pending} onClick={() => void cancelDiagnostic()}>Cancel diagnostic</Button>}</div>{diagnostic && <DiagnosticView value={diagnostic} />}</Panel>}
    {diagnostic && <Panel heading={diagnostic.operation === 'connection_test' ? 'Connection test' : 'Diagnostic result'}><DiagnosticView value={diagnostic} /></Panel>}
  </>;
}

function SecretInput({ id, label, configured, action, value, disabled, onAction, onValue }: { id: string; label: string; configured: boolean; action: Draft['passwordAction']; value: string; disabled: boolean; onAction: (value: Draft['passwordAction']) => void; onValue: (value: string) => void }) {
  return <div className="datasource-secret"><SelectField id={`${id}-action`} label={`${label} secret`} value={action} disabled={disabled} onChange={(event) => onAction(event.target.value as Draft['passwordAction'])} options={[{ value: 'retain', label: configured ? 'Keep configured secret' : 'No secret configured' }, { value: 'replace', label: 'Replace secret' }, { value: 'clear', label: 'Clear secret' }]} /><TextField id={id} label={`${label} value`} type="password" autoComplete="new-password" value={value} disabled={disabled || action !== 'replace'} hint={configured ? 'Saved values are never displayed.' : 'Secret values are sent only when replacing.'} onChange={(event) => onValue(event.target.value)} /></div>;
}

function DiagnosticView({ value }: { value: Diagnostic }) {
  const status: 'healthy' | 'warning' | 'breached' | 'no-data' | 'stale' = value.state === 'succeeded' ? 'healthy' : value.state === 'pending' || value.state === 'running' ? 'warning' : value.state === 'failed' || value.state === 'timed_out' ? 'breached' : 'stale';
  const resultStatus = value.result_class === 'complete' ? 'healthy' : value.result_class === 'empty' ? 'no-data' : value.result_class === 'incomplete' ? 'warning' : null;
  return <div className="diagnostic-result" aria-live="polite"><div><StatusBadge status={status} />{resultStatus && <StatusBadge status={resultStatus} />}</div><p>Deadline {new Date(value.deadline_at).toLocaleTimeString()}</p>{value.message && <p>{value.message}</p>}{value.error_code && <p>Code: <code>{value.error_code}</code></p>}{value.result && <pre>{JSON.stringify(value.result, null, 2)}</pre>}<span className="sr-only">Diagnostic {value.state.replace('_', ' ')}</span></div>;
}

function PageTitle({ title, headingRef }: { title: string; headingRef: RefObject<HTMLHeadingElement | null> }) { return <h1 ref={headingRef} className="agents-heading app-page-heading" tabIndex={-1}>{title}</h1>; }
