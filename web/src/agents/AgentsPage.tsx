import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { createAgentClient, AgentFailure, type Agent, type AgentCredential, type AgentIssuance, type AgentDetail } from './client.ts';
import { AgentRuntimePanel } from './AgentRuntimePanel.tsx';
import { AgentLifecycleControls } from './AgentLifecycleControls.tsx';
import { DurationControl, describeDuration } from './DurationControl.tsx';
import { Button, Dialog, Panel, TextField } from '../components/ui/index.ts';
import './agents.css';

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
        lastSuccessAt.current = new Date().toISOString(); hasCachedData.current = true;
      } else {
        const page = await api.list({ signal: controller.signal, limit: LIMIT, ...(state.agents_cursor ? { cursor: state.agents_cursor } : {}) });
        if (controller.signal.aborted || generation !== readGeneration.current) return;
        setAgents(page.items); setAgent(null); setAgentDetail(null); setCredentials([]); setAgentNext(page.next_cursor); setCredentialNext('');
        lastSuccessAt.current = new Date().toISOString(); hasCachedData.current = true;
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

  return <section className="agents-screen" aria-labelledby="agents-title">
    <div className="agents-title-row"><div className="agents-actions"><Button variant="outline" onClick={() => void load()} disabled={suspended || loading || submitBusy || lifecycleBusy}>{locationState.agent ? 'Refresh agent' : 'Refresh agents'}</Button><Button ref={registerTrigger} onClick={openRegister} disabled={suspended || lifecycleBusy || Boolean(lifecycleIntent)}>Register agent</Button></div></div>
    {error && !registerOpen && <div ref={errorRef} className="agents-error" role="alert" tabIndex={-1}>{error}</div>}
    {registrationNotice && <div className="agents-error" role="status">{registrationNotice}</div>}
    {intent && registrationState && <Panel heading={registrationState.heading} className="agents-unknown"><p>{registrationState.message}</p>{intent.requestID && <p>Request ID: <code>{intent.requestID}</code></p>}{proofRejected === 'unavailable' && <Button variant="outline" onClick={() => void checkAuthorityAgain()}>Check session</Button>}{!(conflicted || intent.outcome === 'conflict') && proofRejected !== 'unavailable' && <Button variant="outline" disabled={Date.now() < throttleUntil} onClick={() => setRegisterOpen(true)}>Retry registration</Button>}<Button variant="ghost" onClick={() => setAbandonPrompt(true)}>Abandon request</Button></Panel>}
    {announceUnconfirmed && locationState.agent && <p ref={registrationStatusRef} className="agents-error" role="status" aria-live="polite" tabIndex={-1}>Registration is not confirmed. The agent records currently shown may not include this request.</p>}
    {lifecycleIntent && (lifecycleIntent.userID !== currentUser || lifecycleIntent.agentID !== locationState.agent) && <Panel heading="Credential command needs attention" className="agents-unknown"><p>A {lifecycleIntent.kind} command for agent {lifecycleIntent.agentID} must be reconciled before another credential command can start. Its original command key and resource version are retained.</p><div className="agents-actions"><Button variant="outline" disabled={suspended || submitBusy || lifecycleBusy} onClick={() => setQuery({ agent: lifecycleIntent.agentID, agents_cursor: '', credentials_cursor: '' })}>Return to affected agent</Button><Button variant="ghost" disabled={submitBusy || lifecycleBusy} onClick={() => onLifecycleIntentChange(null)}>Abandon command</Button></div></Panel>}
    {rateLimited && now < throttleUntil && <p role="status">Try registration again in {Math.ceil((throttleUntil - now) / 1000)} seconds.</p>}
    {loading && <span className="sr-only" role="status">Refreshing agent records</span>}
    {!locationState.agent && <Panel heading="Agent records" className="agents-panel">
      {announceUnconfirmed && <p ref={registrationStatusRef} className="agents-error" role="status" aria-live="polite" tabIndex={-1}>Registration is not confirmed. The agent records currently shown may not include this request.</p>}
      {loading && !hasCachedData.current ? null : agents.length === 0 ? <p>{error && !hasCachedData.current ? 'Agent records could not be loaded.' : intent?.outcome === 'unknown' ? 'No agents are currently visible.' : 'No agents have been registered.'}</p> : <div className="agents-table-scroll" role="region" aria-label="Agent records" tabIndex={0}><table><thead><tr><th>Name</th><th>Display name</th><th>Administration</th><th>Created (UTC)</th><th>Updated (UTC)</th></tr></thead><tbody>{agents.map((item) => <tr key={item.id}><td><button className="agents-link" onClick={() => setQuery({ agent: item.id, agents_cursor: '', credentials_cursor: '' })}>{item.name}</button></td><td>{item.display_name}</td><td>{item.enabled ? 'Enabled' : 'Disabled'}</td><td>{date(item.created_at)}</td><td>{date(item.updated_at)}</td></tr>)}</tbody></table></div>}
      {agentNext && <Button variant="outline" onClick={() => setQuery({ agents_cursor: agentNext })}>Next agents</Button>}
    </Panel>}
    {locationState.agent && <Button variant="outline" onClick={() => setQuery({ agent: '', credentials_cursor: '' })}>Back to agents</Button>}
    {(!loading || hasCachedData.current) && locationState.agent && agent && <>
      <Panel heading="Agent details" className="agents-panel"><dl className="agents-meta"><div><dt>Name</dt><dd>{agent.name}</dd></div><div><dt>Display name</dt><dd>{agent.display_name}</dd></div><div><dt>Administration</dt><dd>{agent.enabled ? 'Enabled' : 'Disabled'}</dd></div><div><dt>Agent ID</dt><dd>{agent.id}</dd></div><div><dt>Created (UTC)</dt><dd>{date(agent.created_at)}</dd></div><div><dt>Updated (UTC)</dt><dd>{date(agent.updated_at)}</dd></div></dl></Panel>
      <AgentRuntimePanel agent={agent} />
      <Panel heading="Credential history" className="agents-panel"><p>Scheduled retirement and revocation are separate events. This history does not show whether a credential is currently connected or active.</p>
        {credentials.length === 0 ? <p>No credentials are recorded.</p> : <div className="agents-credentials" role="region" aria-label="Credential history">{credentials.map((item) => <section className="agents-credential" key={item.id} aria-label={`Credential ${item.id}`}><dl className="agents-meta"><div><dt>Credential ID</dt><dd>{item.id}</dd></div><div><dt>Issued (UTC)</dt><dd>{date(item.issued_at)}</dd></div><div><dt>Expires (UTC)</dt><dd>{date(item.expires_at)}</dd></div><div><dt>Scheduled retirement (UTC)</dt><dd>{item.retire_at ? date(item.retire_at) : '—'}</dd></div><div><dt>Revoked (UTC)</dt><dd>{item.revoked_at ? date(item.revoked_at) : '—'}</dd></div></dl>{item.revoked_at === null && <Button variant="danger" onClick={() => setRevokeRequest({ credentialID: item.id, sequence: ++revokeSequence.current })} disabled={suspended || lifecycleBusy || submitBusy || loading || Boolean(error) || !agentDetail || Boolean(intent) || Boolean(lifecycleIntent)}>Revoke credential</Button>}</section>)}</div>}
        {credentialNext && <Button variant="outline" onClick={() => setQuery({ credentials_cursor: credentialNext })}>Next credential history</Button>}
      </Panel>
    </>}
    {locationState.agent && (agent || lifecycleIntent?.agentID === locationState.agent) && <AgentLifecycleControls agent={agent} detail={agentDetail} credentials={credentials} context={context} recheckSession={recheckSession} suspended={suspended} selectedAgentID={locationState.agent} navigationScope={JSON.stringify(locationState)} actionAvailable={!loading && !error && locationState.agent === agent?.id} intent={lifecycleIntent} onIntentChange={onLifecycleIntentChange} busy={submitBusy || lifecycleBusy} onBusyChange={setLifecycleBusy} refresh={load} revokeRequest={revokeRequest} onRevokeRequestHandled={consumeRevokeRequest} mutationLock={createLock} onDeleted={() => { setRegistrationNotice('Agent deleted. Its credentials have been revoked.'); setQuery({ agent: '', agents_cursor: '', credentials_cursor: '' }); }} onRegisterReplacement={openRegister} onForbidden={() => { setAgents([]); setAgent(null); setAgentDetail(null); setCredentials([]); setAgentNext(''); setCredentialNext(''); hasCachedData.current = false; lastSuccessAt.current = ''; setError('You are not allowed to perform this credential action. Protected agent records were cleared.'); }} />}
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
