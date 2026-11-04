import { useCallback, useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { createAgentClient, AgentFailure, type Agent, type AgentCredential, type AgentDatasourcePage, type AgentIssuance, type AgentDetail } from './client.ts';
import { AgentRuntimePanel } from './AgentRuntimePanel.tsx';
import { AgentLifecycleControls } from './AgentLifecycleControls.tsx';
import { DurationControl, describeDuration } from './DurationControl.tsx';
import { Button, Dialog, Pager, Panel, SearchField, SegmentedControl, StatusMark, TextField, Toolbar, type MarkTone } from '../components/ui/index.ts';
import { relativeAge } from '../slis/vocabulary.ts';
import './agents.css';
import { journeyContentReady } from '../telemetry/client.ts';

export type AgentSafeDraft = { name: string; displayName: string; lifetime: string; nameEdited?: boolean };
export type AgentRecoveryCause = 'connection' | 'service' | 'timeout' | 'response';
export type AgentCreateIntent = { key: string; name: string; displayName: string; lifetimeSeconds?: number; userID: string; outcome: 'unknown' | 'rejected' | 'conflict'; retryRejected?: 'session' | 'permission' | 'other'; recoveryCause?: AgentRecoveryCause; requestID?: string };
export type AgentLifecycleIntent = { kind: 'rotate' | 'revoke' | 'disable' | 'delete'; key: string; userID: string; agentID: string; credentialID?: string; etag: string; lifetimeSeconds?: number; overlapSeconds?: number; retryAfterDeadline?: number; outcome: 'unknown' | 'rejected' | 'conflict'; retryRejected?: 'session' | 'permission' | 'other' | 'conflict' };
export type SessionRecheck = { confirmed: true; context: AuthContext } | { confirmed: false };
type Props = { onBreadcrumbChange?: (value: { id: string; name: string } | null) => void; context: AuthContext; recheckSession: () => Promise<SessionRecheck>; suspended: boolean; draft: AgentSafeDraft; onDraftChange: (draft: AgentSafeDraft) => void; intent: AgentCreateIntent | null; onIntentChange: (intent: AgentCreateIntent | null) => void; lifecycleIntent: AgentLifecycleIntent | null; onLifecycleIntentChange: (intent: AgentLifecycleIntent | null) => void };
const emptyDraft: AgentSafeDraft = { name: '', displayName: '', lifetime: '', nameEdited: false };
const safeError = 'The request could not be completed. Check the session and try again.';
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const AGENT_NAME = /^[a-z][a-z0-9-]{0,62}$/;
const agentNameError = 'Use 1–63 lowercase letters, numbers, or hyphens, starting with a letter.';
const LIMIT = 50;
function suggestAgentName(displayName: string) {
  const slug = displayName.normalize('NFKD').replace(/\p{M}/gu, '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
  if (!slug) return '';
  const identifier = /^[a-z]/.test(slug) ? slug : `agent-${slug}`;
  return identifier.slice(0, 63).replace(/-+$/g, '');
}
function recoveryCause(code: string): AgentRecoveryCause {
  if (code === 'management_unavailable') return 'service';
  if (code === 'request_timeout') return 'timeout';
  if (code === 'connection_failed' || code === 'request_cancelled') return 'connection';
  return 'response';
}
function registrationFeedback(intent: AgentCreateIntent, conflicted: boolean, proofRejected: 'session' | 'unavailable' | false, rateLimited: boolean) {
  if (conflicted || intent.outcome === 'conflict') return { heading: 'Registration needs reconciliation', message: 'The server reported a conflict. Reconcile the agent and credential history before starting another request.' };
  if (proofRejected === 'session') return { heading: 'Session needs checking', message: intent.outcome === 'unknown' ? 'This retry was rejected because the session was not accepted. An earlier attempt remains unconfirmed. Check the session and refresh agent records before a deliberate retry.' : 'The request was rejected because the session was not accepted. Check the session before a deliberate retry.' };
  if (proofRejected === 'unavailable') return { heading: 'Session status unavailable', message: 'The request was rejected, but the current session could not be confirmed. Check the session before retrying.' };
  if (rateLimited) return { heading: 'Registration is rate limited', message: intent.outcome === 'unknown' ? 'This retry was rate limited. The earlier result remains unconfirmed. Wait, refresh agent records, then retry deliberately with the same request.' : 'The server rejected this request temporarily. Wait before retrying.' };
  if (intent.retryRejected === 'session') return { heading: 'Registration not confirmed', message: 'The last retry was rejected because the session was not accepted. An earlier result remains unconfirmed. Check the session, refresh agent records, then retry with this same request.' };
  if (intent.retryRejected === 'permission') return { heading: 'Registration not confirmed', message: 'The last retry was rejected for lack of permission. An earlier result remains unconfirmed; confirm administrator authority before retrying this request.' };
  if (intent.retryRejected === 'other') return { heading: 'Registration not confirmed', message: 'The last retry was rejected. An earlier result remains unconfirmed; refresh agent records and keep using this same request while reconciling it.' };
  if (intent.recoveryCause === 'service') return { heading: 'Registration not confirmed', message: 'Agent management reported that it is temporarily unavailable. Registration may still have completed. Refresh agent records or wait for service to recover; a deliberate retry reuses this same request and will not create a duplicate.' };
  if (intent.recoveryCause === 'connection') return { heading: 'Registration not confirmed', message: 'No response was received from Almanac. Registration may still have completed. Check your connection and refresh agent records before deciding; a deliberate retry reuses this same request and will not create a duplicate.' };
  if (intent.recoveryCause === 'timeout') return { heading: 'Registration not confirmed', message: 'The registration request timed out before a response arrived. It may still have completed. Refresh agent records before deciding; a deliberate retry reuses this same request and will not create a duplicate.' };
  if (intent.recoveryCause === 'response') return { heading: 'Registration not confirmed', message: 'Almanac returned a response that could not be verified. Registration may still have completed. Refresh agent records before deciding; a deliberate retry reuses this same request and will not create a duplicate.' };
  if (intent.outcome === 'rejected') return { heading: 'Registration was not accepted', message: 'The server rejected this request before confirming registration. Review the request and retry deliberately with the same request key.' };
  return { heading: 'Registration not confirmed', message: 'Registration may have completed. Refresh agent records before deciding. A deliberate retry reuses this same request key; a replay cannot return the one-time token.' };
}
function queryState() { const q = new URLSearchParams(location.search); return { agent: q.get('agent') ?? '', agents_cursor: q.get('agents_cursor') ?? '', credentials_cursor: q.get('credentials_cursor') ?? '' }; }
function setQuery(patch: Partial<ReturnType<typeof queryState>>, replace = false) { const q = new URLSearchParams(location.search); for (const [key, value] of Object.entries(patch)) value ? q.set(key, value) : q.delete(key); const url = `${location.pathname}${q.size ? `?${q}` : ''}`; history[replace ? 'replaceState' : 'pushState']({}, '', url); window.dispatchEvent(new PopStateEvent('popstate')); }
function date(value: string) { return value.replace('T', ' '); }
const clockFormat = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: 'UTC', hour12: false });
const dayFormat = new Intl.DateTimeFormat('en-GB', { day: '2-digit', month: 'short', year: 'numeric', timeZone: 'UTC' });
type AgentSource = AgentDatasourcePage['items'][number];
type AgentFilter = 'all' | 'attention' | 'disabled';
type Vocabulary = { tone: MarkTone; label: string; reason: string; attention: boolean };
// ADR-0028 vocabulary for agents, from the record and its reported runtime only.
function agentVocabulary(item: Agent, now = Date.now()): Vocabulary {
  const runtime = item.runtime ?? null;
  if (!item.enabled) return { tone: 'none', label: 'Disabled', reason: `by administrator · ${dayFormat.format(new Date(item.updated_at))}`, attention: false };
  if (!runtime) return { tone: 'none', label: 'Never connected', reason: 'not started yet', attention: false };
  const beat = runtime.last_heartbeat_at ? relativeAge(runtime.last_heartbeat_at, now) : null;
  if (runtime.reachability === 'unreachable') return { tone: 'stale', label: 'Unreachable', reason: beat ? `no heartbeat for ${beat.replace(' ago', '')}` : 'no heartbeat', attention: true };
  if (runtime.reachability === 'incompatible') return { tone: 'warning', label: 'Incompatible', reason: `protocol ${runtime.protocol}`, attention: true };
  if (runtime.configuration_failure_code) return { tone: 'warning', label: 'Connected', reason: `configuration ${runtime.configuration_failure_code.replaceAll('_', ' ')}`, attention: true };
  return { tone: 'healthy', label: 'Connected', reason: beat ? `heartbeat ${beat}` : 'heartbeat reported', attention: false };
}
function collectionSummary(item: Agent): { label: string; sub: string } {
  const runtime = item.runtime ?? null;
  if (!item.enabled) return { label: '—', sub: 'disabled' };
  if (!runtime) return { label: 'Not started', sub: 'no heartbeat yet' };
  const progress = runtime.collection_progress.replaceAll('_', ' ');
  const label = progress.charAt(0).toUpperCase() + progress.slice(1);
  const sub = runtime.collection_explanation === 'no_assignment' ? 'no datasource assigned' : runtime.last_successful_query_at ? `last query ${relativeAge(runtime.last_successful_query_at)}` : 'no successful query yet';
  return { label, sub };
}
function currentCredential(items: AgentCredential[], now = Date.now()): { label: string; sub: string; tone: MarkTone } {
  const live = items.filter((item) => item.revoked_at === null && Date.parse(item.expires_at) > now && (!item.retire_at || Date.parse(item.retire_at) > now)).sort((a, b) => Date.parse(b.issued_at) - Date.parse(a.issued_at))[0];
  if (!live) return { label: items.length ? 'none valid' : 'none', sub: items.length ? 'rotate to issue a new credential' : 'no credential recorded', tone: 'stale' };
  const days = Math.floor((Date.parse(live.expires_at) - now) / 86400000);
  return { label: `valid · ${days} d left`, sub: `issued ${dayFormat.format(new Date(live.issued_at))} · expires ${dayFormat.format(new Date(live.expires_at))}`, tone: days <= 7 ? 'warning' : 'healthy' };
}
function runCommands(agent: Agent): [string, string, string][] {
  return [
    ['local', 'Local (make)', 'make agent\n# paste the token at the private prompt'],
    ['binary', 'Binary', 'bin/almanac-agent run --backend-url=https://<backend-origin> \\\n  --token-file=/run/secrets/almanac-agent-token'],
  ].map(([value, label, command]) => [value!, label!, command!.replace('{name}', agent.name)]) as [string, string, string][];
}

export function AgentsPage({ context, recheckSession, suspended, draft, onDraftChange, intent, onIntentChange, lifecycleIntent, onLifecycleIntentChange, onBreadcrumbChange }: Props) {
  const clientRef = useRef<ReturnType<typeof createAgentClient> | null>(null);
  if (!clientRef.current) clientRef.current = createAgentClient();
  const api = clientRef.current;
  const [locationState, setLocationState] = useState(queryState);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [agent, setAgent] = useState<Agent | null>(null);
  const [agentDetail, setAgentDetail] = useState<AgentDetail | null>(null);
  useEffect(() => { onBreadcrumbChange?.(agent && agent.id === locationState.agent ? { id: agent.id, name: agent.display_name } : null); }, [agent, locationState.agent, onBreadcrumbChange]);
  const [credentials, setCredentials] = useState<AgentCredential[]>([]);
  const [agentNext, setAgentNext] = useState(''); const [credentialNext, setCredentialNext] = useState('');
  const [loading, setLoading] = useState(true); const [error, setError] = useState('');
  const [datasources, setDatasources] = useState<AgentSource[] | null>(null);
  const [datasourcesPartial, setDatasourcesPartial] = useState(false);
  const [updatedAt, setUpdatedAt] = useState('');
  const [listSearch, setListSearch] = useState('');
  const [listFilter, setListFilter] = useState<AgentFilter>('all');
  const [previousCursors, setPreviousCursors] = useState<string[]>([]);
  const trailTarget = useRef<string | null>(null);
  const [runOpen, setRunOpen] = useState(false); const [runVariant, setRunVariant] = useState('local');
  const runTrigger = useRef<HTMLButtonElement>(null);
  const [registerOpen, setRegisterOpen] = useState(false); const [tokenResult, setTokenResult] = useState<AgentIssuance | null>(null);
  const [copyNotice, setCopyNotice] = useState(''); const [submitBusy, setSubmitBusy] = useState(false);
  const [lifecycleBusy, setLifecycleBusy] = useState(false);
  const [revokeRequest, setRevokeRequest] = useState<{ credentialID: string; sequence: number } | null>(null);
  const revokeSequence = useRef(0);
  const consumeRevokeRequest = useCallback((sequence: number) => { setRevokeRequest((current) => current?.sequence === sequence ? null : current); }, []);
  const [throttleUntil, setThrottleUntil] = useState(0); const [now, setNow] = useState(Date.now());
  const [conflicted, setConflicted] = useState(false);
  const [registrationNotice, setRegistrationNotice] = useState('');
  const [nameError, setNameError] = useState('');
  const [proofRejected, setProofRejected] = useState<'session' | 'unavailable' | false>(false);
  const [rateLimited, setRateLimited] = useState(false);
  const [abandonPrompt, setAbandonPrompt] = useState(false);
  const createLock = useRef(false); const readController = useRef<AbortController | null>(null);
  const registrationStatusRef = useRef<HTMLParagraphElement | null>(null);
  const actionGeneration = useRef(0); const readGeneration = useRef(0); const mounted = useRef(false); const currentAuthority = useRef({ userID: '', administrator: false });
  const hasCachedData = useRef(false); const lastSuccessAt = useRef('');
  const resultKey = useRef(JSON.stringify(queryState()));
  const pendingAgentNavigation = useRef('');
  const suspendedBefore = useRef(suspended);
  const registerTrigger = useRef<HTMLButtonElement>(null); const tokenTrigger = useRef<HTMLElement>(null); const tokenClose = useRef<HTMLButtonElement>(null);
  const errorRef = useRef<HTMLDivElement>(null);
  const currentUser = context.session?.user_id ?? '';
  const isAdmin = context.session?.role === 'administrator' && !context.session.restricted;
  currentAuthority.current = { userID: currentUser, administrator: isAdmin && !suspended };

  useEffect(() => { mounted.current = true; return () => { mounted.current = false; actionGeneration.current++; readController.current?.abort(); setTokenResult(null); }; }, []);
  useEffect(() => {
    const update = () => {
      const next = queryState();
      const nextKey = JSON.stringify(next);
      actionGeneration.current++;
      // The pager's trail describes only pages reached through the pager; any other navigation clears it.
      if (trailTarget.current !== next.agents_cursor) setPreviousCursors([]);
      trailTarget.current = null;
      if (nextKey !== resultKey.current) {
        readGeneration.current++;
        setAgents([]); setAgent(null); setAgentDetail(null); setCredentials([]); setAgentNext(''); setCredentialNext(''); setLoading(true);
        hasCachedData.current = false; lastSuccessAt.current = ''; setError('');
      }
      resultKey.current = nextKey;
      setLocationState(next);
    };
    addEventListener('popstate', update); return () => removeEventListener('popstate', update);
  }, []);
  useEffect(() => { if (tokenResult) closeToken(false); }, [locationState.agent, locationState.agents_cursor, locationState.credentials_cursor]);
  useEffect(() => { if (!throttleUntil || now >= throttleUntil) return; const timer = setInterval(() => setNow(Date.now()), 250); return () => clearInterval(timer); }, [throttleUntil, now]);
  const clearSecrets = useCallback(() => { setTokenResult(null); setCopyNotice(''); }, []);
  useEffect(() => {
    actionGeneration.current++; readGeneration.current++; clearSecrets(); setError(''); setRegistrationNotice(''); readController.current?.abort();
    if (!isAdmin) { setAgents([]); setAgent(null); setCredentials([]); setAgentNext(''); setCredentialNext(''); hasCachedData.current = false; lastSuccessAt.current = ''; }
  }, [currentUser, context.session?.restricted, isAdmin, clearSecrets]);
  useEffect(() => {
    if (suspended && !suspendedBefore.current) {
      actionGeneration.current++;
      readGeneration.current++;
      pendingAgentNavigation.current = '';
      readController.current?.abort();
    }
    suspendedBefore.current = suspended;
    if (suspended) clearSecrets();
  }, [suspended, clearSecrets]);
  useEffect(() => { if (error) errorRef.current?.focus(); }, [error]);

  const load = useCallback(async () => {
    if (!isAdmin || suspended) { setLoading(false); return; }
    readController.current?.abort(); const controller = new AbortController(); readController.current = controller;
    const generation = readGeneration.current;
    setLoading(true); setError('');
    const state = queryState();
    try {
      if (state.agent) {
        if (!UUID.test(state.agent)) throw new Error('Invalid agent identifier.');
        const [detail, historyPage] = await Promise.all([
          api.get(state.agent, { signal: controller.signal }),
          api.credentials(state.agent, { signal: controller.signal, limit: LIMIT, ...(state.credentials_cursor ? { cursor: state.credentials_cursor } : {}) }),
        ]);
        if (controller.signal.aborted || generation !== readGeneration.current) return;
        setAgent(detail.agent); setAgentDetail(detail); setCredentials(historyPage.items); setCredentialNext(historyPage.next_cursor); setAgents([]); setAgentNext('');
        lastSuccessAt.current = new Date().toISOString(); hasCachedData.current = true; setUpdatedAt(clockFormat.format(new Date()));
        setDatasources(null); setDatasourcesPartial(false);
        void api.datasources(state.agent, { signal: controller.signal, limit: 50 }).then((page) => { if (!controller.signal.aborted && generation === readGeneration.current) { setDatasources(page.items); setDatasourcesPartial(Boolean(page.next_cursor)); } }).catch(() => undefined);
      } else {
        const page = await api.list({ signal: controller.signal, limit: LIMIT, ...(state.agents_cursor ? { cursor: state.agents_cursor } : {}) });
        if (controller.signal.aborted || generation !== readGeneration.current) return;
        setAgents(page.items); setAgent(null); setAgentDetail(null); setCredentials([]); setAgentNext(page.next_cursor); setCredentialNext('');
        lastSuccessAt.current = new Date().toISOString(); hasCachedData.current = true; setUpdatedAt(clockFormat.format(new Date()));
      }
    } catch (caught) {
      if (controller.signal.aborted || generation !== readGeneration.current) return;
      if (caught instanceof AgentFailure && caught.kind === 'rejected' && ['unauthenticated', 'forbidden'].includes(caught.code)) {
        setAgents([]); setAgent(null); setCredentials([]); setAgentNext(''); setCredentialNext(''); hasCachedData.current = false; lastSuccessAt.current = '';
        if (caught.code === 'unauthenticated') await recheckSession();
        if (controller.signal.aborted || generation !== readGeneration.current) return;
        setError('Your session no longer has access to these records. Check your session before continuing.');
        return;
      }
      setError(caught instanceof Error && caught.message === 'Invalid agent identifier.' ? caught.message : safeError);
      if (hasCachedData.current && resultKey.current === JSON.stringify(state)) setError(`${safeError} Showing stale results from ${date(lastSuccessAt.current)}.`);
      else { setAgents([]); setAgent(null); setCredentials([]); setAgentNext(''); setCredentialNext(''); hasCachedData.current = false; lastSuccessAt.current = ''; }
    } finally { if (!controller.signal.aborted && generation === readGeneration.current) setLoading(false); }
  }, [isAdmin, suspended, locationState.agent, locationState.agents_cursor, locationState.credentials_cursor]);
  useEffect(() => { void load(); return () => readController.current?.abort(); }, [load]);
  useEffect(() => {
    if (!loading && !suspended) journeyContentReady('management', !!error);
  }, [loading, suspended, error, locationState]);

  function resetSafeDraft() { onDraftChange(emptyDraft); setNameError(''); }
  function openRegister() { setError(''); setNameError(''); setRegisterOpen(true); }
  function closeRegister() {
    setRegisterOpen(false);
    if (intent) setAbandonPrompt(true);
  }
  function abandonIntent() { actionGeneration.current++; onIntentChange(null); setAbandonPrompt(false); resetSafeDraft(); setConflicted(false); setProofRejected(false); setRateLimited(false); }
  async function checkAuthorityAgain() {
    const generation = actionGeneration.current;
    const result = await recheckSession();
    if (!mounted.current || generation !== actionGeneration.current || !currentAuthority.current.administrator) return;
    if (!result.confirmed) { setProofRejected('unavailable'); setError('Your administrator session could not be confirmed. Check the session again before retrying registration.'); return; }
    const confirmed = result.context;
    if (!confirmed.session || confirmed.session.user_id !== currentUser || confirmed.session.restricted || confirmed.session.role !== 'administrator') {
      setAgents([]); setAgent(null); setCredentials([]); hasCachedData.current = false; lastSuccessAt.current = '';
      setProofRejected('unavailable'); setError('Administrator access could not be confirmed. Sign in again before retrying registration.'); return;
    }
    setProofRejected('session'); setError('The registration request was rejected. Your administrator session is confirmed now; refresh agent records and retry deliberately with the same request key if needed.');
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (createLock.current || submitBusy || lifecycleBusy || lifecycleIntent || suspended || !isAdmin || proofRejected === 'unavailable' || Date.now() < throttleUntil) return;
    setError(''); setRegistrationNotice(''); setProofRejected(false); setRateLimited(false);
    let operation = intent;
    if (operation && operation.userID !== currentUser) { onIntentChange(null); operation = null; }
    const earlierOutcomeUnknown = operation?.outcome === 'unknown';
    if (!operation) {
      const lifetime = draft.lifetime === '' ? 7776000 : Number(draft.lifetime);
      if (!draft.name || !draft.displayName || (!Number.isInteger(lifetime) || lifetime < 1 || lifetime > 31536000)) { setError('Enter an agent name and display name, and an expiry between 1 second and 1 year as a whole number of seconds.'); return; }
      if (!AGENT_NAME.test(draft.name)) { setNameError(agentNameError); document.getElementById('agent-name')?.focus(); return; }
      setNameError('');
      operation = { key: crypto.randomUUID(), name: draft.name, displayName: draft.displayName, ...(lifetime === undefined ? {} : { lifetimeSeconds: lifetime }), userID: currentUser, outcome: 'unknown' };
    }
    const input = { name: operation.name, display_name: operation.displayName, ...(operation.lifetimeSeconds === undefined ? {} : { lifetime_seconds: operation.lifetimeSeconds }) };
    operation = { ...operation, outcome: 'unknown', retryRejected: undefined, recoveryCause: undefined, requestID: undefined };
    onIntentChange(operation);
    createLock.current = true; setSubmitBusy(true);
    const generation = ++actionGeneration.current;
    try {
      const issuance = await api.create(input, context, operation.key);
      if (!mounted.current || generation !== actionGeneration.current || !currentAuthority.current.administrator || currentAuthority.current.userID !== operation.userID) return;
      if (issuance.replayed) { onIntentChange(null); resetSafeDraft(); setRegisterOpen(false); setAbandonPrompt(false); setRegistrationNotice('Registration was already processed. Its token is no longer available; review the agent and credential history.'); setQuery({ agent: issuance.agent.id, agents_cursor: '', credentials_cursor: '' }); }
      else { pendingAgentNavigation.current = issuance.agent.id; tokenTrigger.current = registerTrigger.current; setTokenResult(issuance); setRegisterOpen(false); setAbandonPrompt(false); onIntentChange(null); resetSafeDraft(); }
    } catch (caught) {
      if (!mounted.current || generation !== actionGeneration.current || !currentAuthority.current.administrator || currentAuthority.current.userID !== operation.userID) return;
      setRegisterOpen(false);
      if (caught instanceof AgentFailure && caught.kind === 'rejected' && caught.code === 'unauthenticated') {
        onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'rejected', retryRejected: earlierOutcomeUnknown ? 'session' : undefined });
        const refreshed = await recheckSession();
        if (!mounted.current || generation !== actionGeneration.current || !currentAuthority.current.administrator || currentAuthority.current.userID !== operation.userID) return;
        if (!refreshed.confirmed) { setProofRejected('unavailable'); setError('The registration request was rejected, but your session could not be confirmed. Check the session before retrying.'); }
        else if (!refreshed.context.session || refreshed.context.session.user_id !== currentUser || refreshed.context.session.restricted || refreshed.context.session.role !== 'administrator') { onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'rejected', retryRejected: earlierOutcomeUnknown ? 'session' : undefined }); setError('The request was rejected and administrator access is unavailable. Sign in again before retrying this same request.'); }
        else { onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'rejected', retryRejected: earlierOutcomeUnknown ? 'session' : undefined }); setProofRejected('session'); setError('The request was not accepted with the current session. Your administrator session is confirmed now; refresh agent records before a deliberate retry.'); }
      } else if (caught instanceof AgentFailure && caught.kind === 'rejected' && caught.code === 'csrf_invalid') {
        onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'rejected', retryRejected: earlierOutcomeUnknown ? 'session' : undefined });
        const refreshed = await recheckSession();
        if (!mounted.current || generation !== actionGeneration.current || !currentAuthority.current.administrator || currentAuthority.current.userID !== operation.userID) return;
        setProofRejected(refreshed.confirmed ? 'session' : 'unavailable'); setError(refreshed.confirmed ? 'The request was rejected because its session protection was stale. Check the session, then deliberately retry with the same request.' : 'The request was rejected, but your session could not be confirmed. Check the session before retrying.');
      } else if (caught instanceof AgentFailure && caught.kind === 'rejected' && caught.code === 'rate_limited' && caught.retryAfterSeconds) {
        onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'rejected' }); setThrottleUntil(Date.now() + caught.retryAfterSeconds * 1000); setRateLimited(true); setError('Registration is temporarily limited.');
      } else if (caught instanceof AgentFailure && caught.kind === 'rejected' && caught.code === 'conflict') {
        if (earlierOutcomeUnknown) { onIntentChange({ ...operation, outcome: 'unknown' }); setConflicted(true); setError('This request conflicts with an earlier registration. Reconcile the agent and credential history before starting another request.'); }
        else { onIntentChange(null); setNameError('An agent with this identifier already exists. Choose a different name.'); setRegisterOpen(true); document.getElementById('agent-name')?.focus(); }
      }
      else if (caught instanceof AgentFailure && caught.kind === 'rejected' && caught.code === 'invalid_agent_input') {
        if (earlierOutcomeUnknown) {
          onIntentChange({ ...operation, outcome: 'unknown', retryRejected: 'other' });
          setError('The retry was rejected because the registration details were invalid. The original attempt remains unconfirmed. Refresh agent records before deciding; the original request key is retained.');
        } else {
          onIntentChange(null);
          setError('Agent details were rejected. Check the agent name, display name, and lifetime fields.');
          setRegisterOpen(true);
        }
      }
      else if (caught instanceof AgentFailure && caught.kind === 'rejected' && caught.code === 'forbidden') { if (earlierOutcomeUnknown) onIntentChange({ ...operation, outcome: 'unknown', retryRejected: 'permission' }); else onIntentChange(null); setAgents([]); setAgent(null); setCredentials([]); hasCachedData.current = false; lastSuccessAt.current = ''; setError(earlierOutcomeUnknown ? 'This retry was rejected for lack of permission. An earlier attempt remains unknown; confirm authority before reconciling it.' : 'You are not allowed to register agents.'); }
      else if (caught instanceof AgentFailure && caught.kind === 'rejected') { if (earlierOutcomeUnknown) onIntentChange({ ...operation, outcome: 'unknown', retryRejected: 'other' }); else onIntentChange(null); setError(earlierOutcomeUnknown ? 'This retry was rejected. An earlier attempt remains unknown; keep using the same request key while reconciling it.' : 'Registration was rejected. Review the fields and try again.'); }
      else if (caught instanceof AgentFailure && caught.kind === 'uncertain') {
        onIntentChange({ ...operation, outcome: 'unknown', recoveryCause: recoveryCause(caught.code), requestID: caught.requestID ?? undefined });
        setError('');
      } else {
        onIntentChange({ ...operation, outcome: 'unknown', recoveryCause: 'response' });
        setError('');
      }
    } finally { createLock.current = false; if (mounted.current) setSubmitBusy(false); }
  }
  async function copyToken() { if (!tokenResult || tokenResult.replayed) return; try { await navigator.clipboard.writeText(tokenResult.token); setCopyNotice('Token copied.'); } catch { setCopyNotice('Copy was unavailable. Select and copy the token manually.'); } }
  function closeToken(navigateToCreatedAgent = true) {
    setTokenResult(null); setCopyNotice('');
    const destination = pendingAgentNavigation.current; pendingAgentNavigation.current = '';
    requestAnimationFrame(() => {
      if (destination && navigateToCreatedAgent) setQuery({ agent: destination, agents_cursor: '', credentials_cursor: '' });
      else if (tokenTrigger.current?.isConnected) tokenTrigger.current.focus();
      else document.querySelector<HTMLElement>('.agents-heading')?.focus();
    });
  }

  const registrationState = intent ? registrationFeedback(intent, conflicted, proofRejected, rateLimited) : null;
  const announceUnconfirmed = intent?.outcome === 'unknown' && !submitBusy;
  useEffect(() => {
    if (announceUnconfirmed && !loading) registrationStatusRef.current?.focus();
  }, [announceUnconfirmed, loading, intent?.key, intent?.recoveryCause, intent?.retryRejected]);
  if (!context.session || context.session.restricted) return <Panel heading="Agent administration unavailable"><p>Sign in with an unrestricted administrator account to manage agents.</p></Panel>;
  if (!isAdmin) return <Panel heading="Permission required"><p>Agent administration is available to unrestricted administrators.</p></Panel>;

  const vocabularies = new Map(agents.map((item) => [item.id, agentVocabulary(item)] as const));
  const term = listSearch.trim().toLowerCase();
  const visibleAgents = agents.filter((item) => (!term || `${item.name} ${item.display_name}`.toLowerCase().includes(term))
    && (listFilter === 'all' || (listFilter === 'attention' ? vocabularies.get(item.id)?.attention : !item.enabled)));
  const counts = { connected: agents.filter((item) => vocabularies.get(item.id)?.label === 'Connected').length, unreachable: agents.filter((item) => vocabularies.get(item.id)?.label === 'Unreachable').length, never: agents.filter((item) => vocabularies.get(item.id)?.label === 'Never connected').length, disabled: agents.filter((item) => !item.enabled).length, attention: agents.filter((item) => vocabularies.get(item.id)?.attention).length };
  const listSummary = [`${agents.length} agent${agents.length === 1 ? '' : 's'}${agentNext || locationState.agents_cursor ? ' on this page' : ''}`, counts.connected ? `${counts.connected} connected` : '', counts.unreachable ? `${counts.unreachable} unreachable` : '', counts.never ? `${counts.never} never connected` : '', counts.disabled ? `${counts.disabled} disabled` : '', 'all times UTC'].filter(Boolean).join(' · ');
  const detailVocabulary = agent ? agentVocabulary(agent) : null;
  const runtime = agent?.runtime ?? null;
  const historyComplete = !credentialNext && !locationState.credentials_cursor;
  const credentialTile = historyComplete ? currentCredential(credentials) : { label: '—', sub: 'history spans pages; see Credential history', tone: 'none' as MarkTone };
  const agentSources = datasources ?? [];
  const blockers = agentSources;
  // The status block keeps one tree position whether or not the agent record is loaded yet.
  const lifecycleLayout = ({ rotate, disable, remove, status }: { rotate: ReactNode; disable: ReactNode; remove: ReactNode; status: ReactNode }) => <>
    {status}
    {agent && <>
    <div className="agents-detail-head">
      <div className="agents-detail-head__copy">
        {detailVocabulary && <p className="agents-detail-status"><StatusMark tone={detailVocabulary.tone} label={`${detailVocabulary.label} · ${detailVocabulary.reason}`} /></p>}
        <p className="agents-detail-meta"><span className="mono">{agent.name}</span>{runtime ? ` · ${runtime.protocol} · ${runtime.software_version}` : ''} · registered {dayFormat.format(new Date(agent.created_at))}{datasources ? ` · ${agentSources.length}${datasourcesPartial ? '+' : ''} datasource${agentSources.length === 1 && !datasourcesPartial ? '' : 's'}` : ''}</p>
      </div>
      <div className="agents-detail-head__actions">{updatedAt && <span className="ui-page-head__updated">Updated {updatedAt}</span>}<Button variant="outline" aria-label="Refresh agent" onClick={() => void load()} disabled={suspended || loading || submitBusy || lifecycleBusy}>↻ Refresh</Button><Button ref={runTrigger} variant="outline" onClick={() => setRunOpen(true)}>? How to run</Button>{rotate}<Button ref={registerTrigger} variant="outline" onClick={openRegister} disabled={suspended || lifecycleBusy || Boolean(lifecycleIntent)}>Register agent</Button></div>
    </div>
    {detailVocabulary?.label === 'Unreachable' && <section className="agents-banner" aria-label="Agent unreachable"><p>The agent last reported{runtime?.last_heartbeat_at ? <> at <b>{clockFormat.format(new Date(runtime.last_heartbeat_at))} UTC</b></> : ''}; the backend has heard nothing since{runtime?.last_heartbeat_at ? ` (${relativeAge(runtime.last_heartbeat_at)})` : ''}. {blockers.length ? `Its ${blockers.length}${datasourcesPartial ? '+' : ''} datasource${blockers.length === 1 && !datasourcesPartial ? ' is' : 's are'} not collecting. ` : ''}If the process is down, restart it with its current credential; it replaces the old process immediately. If the host is gone, move its datasources to another agent below.</p></section>}
    {!agent.enabled && <section className="agents-banner" aria-label="Agent disabled"><p>This agent is disabled by an administrator. Its credentials are revoked and it is not operational, even if its latest report showed a connection.</p></section>}
    <div className="agents-tiles">
      <div className="agents-tile"><span className="agents-tile__k">Connection</span><span className="agents-tile__v">{detailVocabulary && <StatusMark tone={detailVocabulary.tone} label={detailVocabulary.label} />}</span><span className="agents-tile__s">{runtime?.last_heartbeat_at ? `last heartbeat ${clockFormat.format(new Date(runtime.last_heartbeat_at))} · ${relativeAge(runtime.last_heartbeat_at)}` : 'no heartbeat received'}</span></div>
      <div className="agents-tile"><span className="agents-tile__k">Configuration</span><span className="agents-tile__v">{!runtime ? '—' : runtime.configuration_failure_code ? `rev ${runtime.configuration_failure_revision ?? runtime.desired_configuration_revision} failed` : runtime.applied_configuration_revision === runtime.desired_configuration_revision ? `rev ${runtime.applied_configuration_revision} applied` : `rev ${runtime.applied_configuration_revision === '0' ? '—' : runtime.applied_configuration_revision} of ${runtime.desired_configuration_revision}`}</span><span className="agents-tile__s">{!runtime ? 'nothing reported yet' : runtime.configuration_failure_code ? runtime.configuration_failure_code.replaceAll('_', ' ') : runtime.applied_configuration_revision === runtime.desired_configuration_revision ? 'matches desired · nothing pending' : 'applying the desired revision'}</span></div>
      <div className="agents-tile"><span className="agents-tile__k">Collection</span><span className="agents-tile__v">{collectionSummary(agent).label}</span><span className="agents-tile__s">{collectionSummary(agent).sub}{runtime?.next_expected_execution_at ? ` · next ${clockFormat.format(new Date(runtime.next_expected_execution_at))}` : ''}</span></div>
      <div className="agents-tile"><span className="agents-tile__k">Credential</span><span className="agents-tile__v"><StatusMark tone={credentialTile.tone} label={credentialTile.label} /></span><span className="agents-tile__s">{credentialTile.sub}</span></div>
    </div>
    <section className="agents-card" aria-labelledby="agents-datasources-title">
      <header><h2 id="agents-datasources-title">Datasources</h2><span className="agents-card__hint">collection this agent is responsible for</span><a className="agents-card__tool" href="/datasources/new" data-app-route>New datasource</a></header>
      {!datasources ? <p className="agents-card__note">Datasource assignments could not be read.</p> : agentSources.length === 0 ? <p className="agents-card__note">No datasources use this agent.</p> : <table className="agents-table">
        <thead><tr><th scope="col">Agent state</th><th scope="col">Datasource</th><th scope="col"></th></tr></thead>
        <tbody>{agentSources.map((item) => <tr key={item.id}>
          <td><StatusMark tone={detailVocabulary?.tone ?? 'none'} label={detailVocabulary?.label ?? 'Unknown'} reason="assigned or moving here · collection state on the datasource page" /></td>
          <td className="agents-table__name"><a href={`/datasources/${item.id}`} data-app-route>{item.display_name}</a><span className="agents-table__sub mono">{item.name}</span></td>
          <td className="agents-table__action"><a href={`/datasources/${item.id}`} data-app-route>Move to another agent</a></td>
        </tr>)}</tbody>
      </table>}
      {datasourcesPartial && <p className="agents-card__note">Showing the first {agentSources.length}; more datasources use this agent. Open the datasource list to see them all.</p>}
    </section>
    <Panel heading="Credential history" className="agents-panel agents-card--panel"><p>Tokens this agent can authenticate with; values are never shown. Scheduled retirement and revocation are separate events. This history does not show whether a credential is currently connected or active.</p>
      {credentials.length === 0 ? <p>No credentials are recorded.</p> : <div className="agents-credentials" role="region" aria-label="Credential history">{credentials.map((item) => { const state = item.revoked_at ? ['none', 'Revoked'] as const : Date.parse(item.expires_at) <= Date.now() ? ['stale', 'Expired'] as const : item.retire_at && Date.parse(item.retire_at) <= Date.now() ? ['stale', 'Retired'] as const : item.retire_at ? ['warning', 'Retiring'] as const : ['healthy', 'Current'] as const; return <section className="agents-credential" key={item.id} aria-label={`Credential ${item.id}`}><div className="agents-credential__state"><StatusMark tone={state[0]} label={state[1]} /></div><dl className="agents-meta"><div><dt>Credential ID</dt><dd>{item.id}</dd></div><div><dt>Issued (UTC)</dt><dd>{date(item.issued_at)}</dd></div><div><dt>Expires (UTC)</dt><dd>{date(item.expires_at)}</dd></div><div><dt>Scheduled retirement (UTC)</dt><dd>{item.retire_at ? date(item.retire_at) : '—'}</dd></div><div><dt>Revoked (UTC)</dt><dd>{item.revoked_at ? date(item.revoked_at) : '—'}</dd></div></dl>{item.revoked_at === null && <Button variant="danger" onClick={() => setRevokeRequest({ credentialID: item.id, sequence: ++revokeSequence.current })} disabled={suspended || lifecycleBusy || submitBusy || loading || Boolean(error) || !agentDetail || Boolean(intent) || Boolean(lifecycleIntent)}>Revoke credential</Button>}</section>; })}</div>}
      {credentialNext && <Button variant="outline" onClick={() => setQuery({ credentials_cursor: credentialNext })}>Next credential history</Button>}
      <p className="agents-card__note">Rotation issues a new token and keeps the current one valid for a grace period so you can restart the agent without a gap. Revoking takes effect immediately.</p>
    </Panel>
    <details className="agents-runtime-details"><summary>Runtime details</summary><dl className="agents-meta agents-record"><div><dt>Agent ID</dt><dd>{agent.id}</dd></div><div><dt>Created (UTC)</dt><dd>{date(agent.created_at)}</dd></div><div><dt>Updated (UTC)</dt><dd>{date(agent.updated_at)}</dd></div><div><dt>Record version</dt><dd>{agent.version}</dd></div></dl><AgentRuntimePanel agent={agent} /></details>
    <div className="agents-dangerline"><span className="agents-dangerline__text"><b>Disable</b> stops the agent and revokes its credentials; <b>delete</b> also removes it.{blockers.length ? ` Move ${blockers.map((row) => row.display_name).join(', ')} first.` : ''}</span><span className="agents-dangerline__actions">{disable}{remove}</span></div>
    <Dialog open={runOpen} onClose={() => setRunOpen(false)} title={`Run ${agent.name}`} triggerRef={runTrigger}>
      <div className="agents-run">
        <p className="ui-form-note">A restarted process replaces the previous one as soon as it authenticates; you do not need to stop the old one first. The token is only shown at registration or rotation. If you no longer have it, rotate the credential.</p>
        <SegmentedControl size="sm" label="Run method" value={runVariant} onChange={setRunVariant} options={runCommands(agent).map(([value, label]) => ({ value, label }))} />
        <pre className="agents-run__command">{runCommands(agent).find(([value]) => value === runVariant)?.[2]}</pre>
        <p className="ui-hint">{runVariant === 'local' ? 'Starts the packaged agent next to the local app and asks for the token at a private prompt. Ctrl-C stops it; set AGENT_TOKEN_FILE for a non-interactive start.' : 'Run the built binary on the host that owns this credential. Replace <backend-origin> with the HTTPS origin agents connect to; in a separate UI deployment that is the backend, not this page’s address. The token file must be a private regular file; pass --ca-file for a private CA.'}</p>
        <p className="ui-form-note"><b>Expected:</b> within a few seconds the agent shows Connected here{runtime ? `, applies configuration rev ${runtime.desired_configuration_revision}` : ''}, and collection starts on the next interval of each assigned datasource.</p>
        <div className="agents-actions"><Button variant="outline" onClick={() => setRunOpen(false)}>Close</Button></div>
      </div>
    </Dialog>
    </>}
  </>;

  return <section className="agents-screen" aria-labelledby="agents-title">
    {!locationState.agent && <div className="agents-head"><p className="ui-page-head__summary agents-head__summary">{hasCachedData.current ? listSummary : 'all times UTC'}</p><div className="ui-page-head__actions">{updatedAt && <span className="ui-page-head__updated">Updated {updatedAt}</span>}<Button variant="outline" aria-label="Refresh agents" onClick={() => void load()} disabled={suspended || loading || submitBusy || lifecycleBusy}>↻ Refresh</Button><Button ref={registerTrigger} onClick={openRegister} disabled={suspended || lifecycleBusy || Boolean(lifecycleIntent)}>Register agent</Button></div></div>}
    {locationState.agent && !agent && <div className="agents-head"><div className="ui-page-head__actions"><Button ref={registerTrigger} variant="outline" onClick={openRegister} disabled={suspended || lifecycleBusy || Boolean(lifecycleIntent)}>Register agent</Button></div></div>}
    {error && !registerOpen && <div ref={errorRef} className="agents-error" role="alert" tabIndex={-1}>{error}{locationState.agent && !agent && <> <Button variant="outline" onClick={() => setQuery({ agent: '', credentials_cursor: '' })}>Back to agents</Button></>}</div>}
    {registrationNotice && <div className="agents-notice" role="status">{registrationNotice}</div>}
    {intent && registrationState && <Panel heading={registrationState.heading} className="agents-unknown"><p>{registrationState.message}</p>{intent.requestID && <p>Request ID: <code>{intent.requestID}</code></p>}{proofRejected === 'unavailable' && <Button variant="outline" onClick={() => void checkAuthorityAgain()}>Check session</Button>}{!(conflicted || intent.outcome === 'conflict') && proofRejected !== 'unavailable' && <Button variant="outline" disabled={Date.now() < throttleUntil} onClick={() => setRegisterOpen(true)}>Retry registration</Button>}<Button variant="ghost" onClick={() => setAbandonPrompt(true)}>Abandon request</Button></Panel>}
    {announceUnconfirmed && locationState.agent && <p ref={registrationStatusRef} className="agents-error" role="status" aria-live="polite" tabIndex={-1}>Registration is not confirmed. The agent records currently shown may not include this request.</p>}
    {lifecycleIntent && (lifecycleIntent.userID !== currentUser || lifecycleIntent.agentID !== locationState.agent) && <Panel heading="Credential command needs attention" className="agents-unknown"><p>A {lifecycleIntent.kind} command for agent {lifecycleIntent.agentID} must be reconciled before another credential command can start. Its original command key and resource version are retained.</p><div className="agents-actions"><Button variant="outline" disabled={suspended || submitBusy || lifecycleBusy} onClick={() => setQuery({ agent: lifecycleIntent.agentID, agents_cursor: '', credentials_cursor: '' })}>Return to affected agent</Button><Button variant="ghost" disabled={submitBusy || lifecycleBusy} onClick={() => onLifecycleIntentChange(null)}>Abandon command</Button></div></Panel>}
    {rateLimited && now < throttleUntil && <p role="status">Try registration again in {Math.ceil((throttleUntil - now) / 1000)} seconds.</p>}
    {loading && <span className="sr-only" role="status">Refreshing agent records</span>}
    {!locationState.agent && <>
      {announceUnconfirmed && <p ref={registrationStatusRef} className="agents-error" role="status" aria-live="polite" tabIndex={-1}>Registration is not confirmed. The agent records currently shown may not include this request.</p>}
      {loading && !hasCachedData.current ? null : agents.length === 0 ? <Panel heading="Agent records" className="agents-panel"><p>{error && !hasCachedData.current ? 'Agent records could not be loaded.' : intent?.outcome === 'unknown' ? 'No agents are currently visible.' : 'No agents have been registered.'}</p></Panel> : <>
        <Toolbar>
          <SearchField id="agents-search" label="Filter agents" placeholder="Filter by name" value={listSearch} onChange={(event) => setListSearch(event.target.value)} />
          <SegmentedControl label="Filter by status" value={listFilter} onChange={setListFilter} options={[{ value: 'all', label: 'All', count: agents.length }, { value: 'attention', label: 'Needs attention', count: counts.attention }, { value: 'disabled', label: 'Disabled', count: counts.disabled }]} />
        </Toolbar>
        {visibleAgents.length === 0 ? <p className="agents-card__note">No agents match these filters on this page.</p> : <div className="agents-table-scroll" role="region" aria-label="Agent records" tabIndex={0}><table className="agents-table agents-table--list"><thead><tr><th scope="col">Status</th><th scope="col">Agent</th><th scope="col">Collection</th></tr></thead><tbody>{visibleAgents.map((item) => { const vocabulary = vocabularies.get(item.id)!; const collection = collectionSummary(item); return <tr key={item.id}>
          <td data-label="Status"><StatusMark tone={vocabulary.tone} label={vocabulary.label} reason={vocabulary.reason} /></td>
          <td data-label="Agent" className="agents-table__name"><button className="agents-link" onClick={() => setQuery({ agent: item.id, agents_cursor: '', credentials_cursor: '' })}>{item.name}</button><span className="agents-table__sub">{item.display_name !== item.name ? `${item.display_name} · ` : ''}{item.runtime?.software_version ? `${item.runtime.software_version} · ` : ''}since {dayFormat.format(new Date(item.created_at))}</span></td>
          <td data-label="Collection"><b className={collection.label === '—' || collection.label === 'Not started' ? 'agents-table__muted' : undefined}>{collection.label}</b><span className="agents-table__sub mono">{collection.sub}</span></td>
        </tr>; })}</tbody></table></div>}
        <Pager id="agents-page-size" label="Agent result pages" position={`Showing ${visibleAgents.length} of ${agents.length}${agentNext || locationState.agents_cursor ? ' on this page' : ''}`} pageSize={LIMIT} pageSizes={[LIMIT]} onPageSizeChange={() => undefined} hasPrevious={previousCursors.length > 0} hasNext={Boolean(agentNext)} busy={loading}
          onPrevious={() => { const previous = previousCursors[previousCursors.length - 1] ?? ''; trailTarget.current = previous; setPreviousCursors((stack) => stack.slice(0, -1)); setQuery({ agents_cursor: previous }); }}
          onNext={() => { trailTarget.current = agentNext; setPreviousCursors((stack) => [...stack, locationState.agents_cursor]); setQuery({ agents_cursor: agentNext }); }} />
      </>}
    </>}
    {locationState.agent && (agent || lifecycleIntent?.agentID === locationState.agent) && (!loading || hasCachedData.current || !agent) && <AgentLifecycleControls layout={lifecycleLayout} agent={agent} detail={agentDetail} credentials={credentials} context={context} recheckSession={recheckSession} suspended={suspended} selectedAgentID={locationState.agent} navigationScope={JSON.stringify(locationState)} actionAvailable={!loading && !error && locationState.agent === agent?.id} intent={lifecycleIntent} onIntentChange={onLifecycleIntentChange} busy={submitBusy || lifecycleBusy} onBusyChange={setLifecycleBusy} refresh={load} revokeRequest={revokeRequest} onRevokeRequestHandled={consumeRevokeRequest} mutationLock={createLock} onDeleted={() => { setRegistrationNotice('Agent deleted. Its credentials have been revoked.'); setQuery({ agent: '', agents_cursor: '', credentials_cursor: '' }); }} onRegisterReplacement={openRegister} onForbidden={() => { setAgents([]); setAgent(null); setAgentDetail(null); setCredentials([]); setAgentNext(''); setCredentialNext(''); hasCachedData.current = false; lastSuccessAt.current = ''; setError('You are not allowed to perform this credential action. Protected agent records were cleared.'); }} />}
    <Dialog open={registerOpen} onClose={closeRegister} title={intent ? 'Retry registration' : 'Register agent'} triggerRef={registerTrigger}>
      <form className="agents-form" noValidate onSubmit={(event) => void submit(event)}>
        {error && registerOpen && <div ref={errorRef} className="agents-error" role="alert" tabIndex={-1}>{error}</div>}
        {!intent && <><TextField id="agent-display-name" label="Display name" value={draft.displayName} onChange={(event) => onDraftChange({ ...draft, displayName: event.target.value, ...(!draft.nameEdited ? { name: suggestAgentName(event.target.value) } : {}) })} disabled={submitBusy} /><TextField id="agent-name" label="Agent name (identifier)" value={draft.name} onChange={(event) => { setNameError(''); onDraftChange({ ...draft, name: event.target.value, nameEdited: true }); }} hint="Suggested from the display name. This stable identifier uses 1–63 lowercase letters, numbers, or hyphens, starting with a letter." error={nameError} disabled={submitBusy} /><DurationControl id="agent-lifetime" label="New credential expires after" value={draft.lifetime} onChange={(value) => onDraftChange({ ...draft, lifetime: value })} disabled={submitBusy} /></>}
        {intent && <p>Retrying {intent.displayName} ({intent.name}) with credential expiry after {describeDuration(intent.lifetimeSeconds ?? 7776000)}.</p>}

        <Button type="submit" pending={submitBusy} disabled={lifecycleBusy || Boolean(lifecycleIntent) || Date.now() < throttleUntil || conflicted}>{intent ? 'Retry registration' : 'Register agent'}</Button>
      </form>
    </Dialog>
    <Dialog open={tokenResult !== null} onClose={closeToken} title="Agent token" triggerRef={tokenTrigger} initialFocusRef={tokenClose}>
      {tokenResult && <div className="agents-token"><p>This token is shown once. Copy it now and store it securely.</p><label htmlFor="issued-agent-token">Agent token</label><output id="issued-agent-token" aria-label="Agent token" className="agents-token-value">{tokenResult.token}</output><Button onClick={() => void copyToken()}>Copy token</Button>{copyNotice && <p role="status">{copyNotice}</p>}<Button ref={tokenClose} variant="outline" onClick={() => closeToken()}>Close token</Button></div>}
    </Dialog>
    <Dialog open={abandonPrompt} onClose={() => setAbandonPrompt(false)} title="Abandon registration request" triggerRef={registerTrigger}>
      <p>Abandoning clears this request key. If registration completed, its token cannot be recovered.</p><div className="agents-actions"><Button variant="danger" onClick={abandonIntent}>Abandon request</Button><Button variant="outline" onClick={() => setAbandonPrompt(false)}>Keep request</Button></div>
    </Dialog>
  </section>;
}
