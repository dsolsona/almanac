import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { createAgentClient, AgentFailure, type Agent, type AgentCredential, type AgentIssuance } from './client.ts';
import { Button, Dialog, LoadingIndicator, Panel, TextField } from '../components/ui/index.ts';
import './agents.css';

export type AgentSafeDraft = { name: string; displayName: string; lifetime: string };
export type AgentCreateIntent = { key: string; name: string; displayName: string; lifetimeSeconds?: number; userID: string; outcome: 'unknown' | 'rejected' | 'conflict'; retryRejected?: 'password' | 'session' | 'permission' | 'other' };
export type SessionRecheck = { confirmed: true; context: AuthContext } | { confirmed: false };
type Props = { context: AuthContext; recheckSession: () => Promise<SessionRecheck>; suspended: boolean; draft: AgentSafeDraft; onDraftChange: (draft: AgentSafeDraft) => void; intent: AgentCreateIntent | null; onIntentChange: (intent: AgentCreateIntent | null) => void };
const emptyDraft: AgentSafeDraft = { name: '', displayName: '', lifetime: '' };
const safeError = 'The request could not be completed. Check the session and try again.';
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const LIMIT = 50;
function queryState() { const q = new URLSearchParams(location.search); return { agent: q.get('agent') ?? '', agents_cursor: q.get('agents_cursor') ?? '', credentials_cursor: q.get('credentials_cursor') ?? '' }; }
function setQuery(patch: Partial<ReturnType<typeof queryState>>, replace = false) { const q = new URLSearchParams(location.search); for (const [key, value] of Object.entries(patch)) value ? q.set(key, value) : q.delete(key); const url = `${location.pathname}${q.size ? `?${q}` : ''}`; history[replace ? 'replaceState' : 'pushState']({}, '', url); window.dispatchEvent(new PopStateEvent('popstate')); }
function date(value: string) { return value.replace('T', ' '); }

export function AgentsPage({ context, recheckSession, suspended, draft, onDraftChange, intent, onIntentChange }: Props) {
  const clientRef = useRef<ReturnType<typeof createAgentClient> | null>(null);
  if (!clientRef.current) clientRef.current = createAgentClient();
  const api = clientRef.current;
  const [locationState, setLocationState] = useState(queryState);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [agent, setAgent] = useState<Agent | null>(null);
  const [credentials, setCredentials] = useState<AgentCredential[]>([]);
  const [agentNext, setAgentNext] = useState(''); const [credentialNext, setCredentialNext] = useState('');
  const [loading, setLoading] = useState(true); const [error, setError] = useState('');
  const [registerOpen, setRegisterOpen] = useState(false); const [tokenResult, setTokenResult] = useState<AgentIssuance | null>(null);
  const [copyNotice, setCopyNotice] = useState(''); const [password, setPassword] = useState(''); const [submitBusy, setSubmitBusy] = useState(false);
  const [passwordFieldError, setPasswordFieldError] = useState('');
  const [throttleUntil, setThrottleUntil] = useState(0); const [now, setNow] = useState(Date.now());
  const [conflicted, setConflicted] = useState(false);
  const [registrationNotice, setRegistrationNotice] = useState('');
  const [proofRejected, setProofRejected] = useState<'password' | 'session' | 'unavailable' | false>(false);
  const [rateLimited, setRateLimited] = useState(false);
  const [abandonPrompt, setAbandonPrompt] = useState(false);
  const createLock = useRef(false); const readController = useRef<AbortController | null>(null);
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
        setAgents([]); setAgent(null); setCredentials([]); setAgentNext(''); setCredentialNext('');
        hasCachedData.current = false; lastSuccessAt.current = ''; setError('');
      }
      resultKey.current = nextKey;
      setLocationState(next);
    };
    addEventListener('popstate', update); return () => removeEventListener('popstate', update);
  }, []);
  useEffect(() => { if (tokenResult) closeToken(false); }, [locationState.agent, locationState.agents_cursor, locationState.credentials_cursor]);
  useEffect(() => { if (!throttleUntil || now >= throttleUntil) return; const timer = setInterval(() => setNow(Date.now()), 250); return () => clearInterval(timer); }, [throttleUntil, now]);
  const clearSecrets = useCallback(() => { setPassword(''); setTokenResult(null); setCopyNotice(''); }, []);
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
    if (suspended) { setPassword(''); clearSecrets(); }
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
        setAgent(detail.agent); setCredentials(historyPage.items); setCredentialNext(historyPage.next_cursor); setAgents([]); setAgentNext('');
        lastSuccessAt.current = new Date().toISOString(); hasCachedData.current = true;
      } else {
        const page = await api.list({ signal: controller.signal, limit: LIMIT, ...(state.agents_cursor ? { cursor: state.agents_cursor } : {}) });
        if (controller.signal.aborted || generation !== readGeneration.current) return;
        setAgents(page.items); setAgent(null); setCredentials([]); setAgentNext(page.next_cursor); setCredentialNext('');
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

  function resetSafeDraft() { onDraftChange(emptyDraft); }
  function openRegister() { setError(''); setPassword(''); setPasswordFieldError(''); setRegisterOpen(true); }
  function closeRegister() {
    setRegisterOpen(false); setPassword(''); setPasswordFieldError('');
    if (intent) setAbandonPrompt(true);
  }
  function abandonIntent() { actionGeneration.current++; onIntentChange(null); setAbandonPrompt(false); resetSafeDraft(); setPassword(''); setConflicted(false); setProofRejected(false); setRateLimited(false); }
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
    setProofRejected('password'); setError('The registration request was rejected. Reenter your current password before retrying with the same request key.');
  }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (createLock.current || submitBusy || suspended || !isAdmin || proofRejected === 'unavailable' || Date.now() < throttleUntil) return;
    setError(''); setRegistrationNotice(''); setProofRejected(false); setRateLimited(false);
    if (!password) { setPasswordFieldError('Enter your current password to register the agent.'); setError('Registration could not be submitted. Correct the highlighted field.'); return; }
    let operation = intent;
    if (operation && operation.userID !== currentUser) { onIntentChange(null); operation = null; }
    const earlierOutcomeUnknown = operation?.outcome === 'unknown';
    if (!operation) {
      const lifetime = draft.lifetime === '' ? undefined : Number(draft.lifetime);
      if (!draft.name || !draft.displayName || (draft.lifetime !== '' && (!Number.isInteger(lifetime) || lifetime! < 0 || lifetime! > 31536000))) { setError('Enter an agent name, display name, and a lifetime from 0 to 31536000 seconds when provided.'); return; }
      operation = { key: crypto.randomUUID(), name: draft.name, displayName: draft.displayName, ...(lifetime === undefined ? {} : { lifetimeSeconds: lifetime }), userID: currentUser, outcome: 'unknown' };
    }
    if (!password) { setError('Enter your current password to register the agent.'); return; }
    const input = { name: operation.name, display_name: operation.displayName, current_password: password, ...(operation.lifetimeSeconds === undefined ? {} : { lifetime_seconds: operation.lifetimeSeconds }) };
    operation = { ...operation, outcome: 'unknown' };
    onIntentChange(operation);
    setPassword(''); setPasswordFieldError(''); createLock.current = true; setSubmitBusy(true);
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
        onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'rejected', retryRejected: earlierOutcomeUnknown ? 'password' : undefined });
        const refreshed = await recheckSession();
        if (!mounted.current || generation !== actionGeneration.current || !currentAuthority.current.administrator || currentAuthority.current.userID !== operation.userID) return;
        if (!refreshed.confirmed) { setProofRejected('unavailable'); setError('The registration request was rejected, but your session could not be confirmed. Check the session before retrying.'); }
        else if (!refreshed.context.session || refreshed.context.session.user_id !== currentUser || refreshed.context.session.restricted || refreshed.context.session.role !== 'administrator') { onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'rejected', retryRejected: earlierOutcomeUnknown ? 'password' : undefined }); setError('The request was rejected and administrator access is unavailable. Sign in again before retrying this same request.'); }
        else { onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'rejected', retryRejected: earlierOutcomeUnknown ? 'password' : undefined }); setProofRejected('password'); setError('The current password proof was rejected. Your administrator session was confirmed; reenter the password and retry deliberately.'); }
      } else if (caught instanceof AgentFailure && caught.kind === 'rejected' && caught.code === 'csrf_invalid') {
        onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'rejected', retryRejected: earlierOutcomeUnknown ? 'session' : undefined });
        const refreshed = await recheckSession();
        if (!mounted.current || generation !== actionGeneration.current || !currentAuthority.current.administrator || currentAuthority.current.userID !== operation.userID) return;
        setProofRejected(refreshed.confirmed ? 'session' : 'unavailable'); setError(refreshed.confirmed ? 'The request was rejected because its session proof was stale. Check the session, then deliberately retry with your current password.' : 'The request was rejected, but your session could not be confirmed. Check the session before retrying.');
      } else if (caught instanceof AgentFailure && caught.kind === 'rejected' && caught.code === 'rate_limited' && caught.retryAfterSeconds) {
        onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'rejected' }); setThrottleUntil(Date.now() + caught.retryAfterSeconds * 1000); setRateLimited(true); setError('Registration is temporarily limited.');
      } else if (caught instanceof AgentFailure && caught.kind === 'rejected' && caught.code === 'conflict') { onIntentChange({ ...operation, outcome: earlierOutcomeUnknown ? 'unknown' : 'conflict' }); setConflicted(true); setError('This request conflicts with an earlier registration. Reconcile the agent and credential history before starting another request.'); }
      else if (caught instanceof AgentFailure && caught.kind === 'rejected' && caught.code === 'forbidden') { if (earlierOutcomeUnknown) onIntentChange({ ...operation, outcome: 'unknown', retryRejected: 'permission' }); else onIntentChange(null); setAgents([]); setAgent(null); setCredentials([]); hasCachedData.current = false; lastSuccessAt.current = ''; setError(earlierOutcomeUnknown ? 'This retry was rejected for lack of permission. An earlier attempt remains unknown; confirm authority before reconciling it.' : 'You are not allowed to register agents.'); }
      else if (caught instanceof AgentFailure && caught.kind === 'rejected') { if (earlierOutcomeUnknown) onIntentChange({ ...operation, outcome: 'unknown', retryRejected: 'other' }); else onIntentChange(null); setError(earlierOutcomeUnknown ? 'This retry was rejected. An earlier attempt remains unknown; keep using the same request key while reconciling it.' : 'Registration was rejected. Review the fields and try again.'); }
      else setError('Outcome unknown. Reenter your current password and choose Retry registration to safely check the same request.');
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

  if (!context.session || context.session.restricted) return <Panel heading="Agent administration unavailable"><p>Sign in with an unrestricted administrator account to manage agents.</p></Panel>;
  if (!isAdmin) return <Panel heading="Permission required"><p>Agent administration is available to unrestricted administrators.</p></Panel>;

  return <section className="agents-screen" aria-labelledby="agents-title">
    <div className="agents-title-row"><h2 id="agents-title" className="agents-heading" tabIndex={-1}>Agents</h2><div className="agents-actions"><Button variant="outline" onClick={() => void load()} disabled={suspended || loading}>{locationState.agent ? 'Refresh agent' : 'Refresh agents'}</Button><Button ref={registerTrigger} onClick={openRegister} disabled={suspended}>Register agent</Button></div></div>
    {error && !registerOpen && <div ref={errorRef} className="agents-error" role="alert" tabIndex={-1}>{error}</div>}
    {registrationNotice && <div className="agents-error" role="status">{registrationNotice}</div>}
    {intent && <Panel heading={conflicted || intent.outcome === 'conflict' ? 'Registration needs reconciliation' : proofRejected === 'password' ? 'Current password needs refreshing' : proofRejected === 'session' ? 'Session proof needs refreshing' : proofRejected === 'unavailable' ? 'Session status unavailable' : rateLimited ? 'Registration is rate limited' : intent.outcome === 'rejected' ? 'Registration was not accepted' : 'Outcome unknown'} className="agents-unknown"><p>{conflicted || intent.outcome === 'conflict' ? 'The server reported a conflict. Reconcile the agent and credential history before starting another request.' : proofRejected === 'password' ? intent.outcome === 'unknown' ? 'This retry was rejected because the current password proof was not accepted. An earlier attempt remains unknown. Reenter the password to retry with the same request key.' : 'The request was rejected because the current password proof was not accepted. Enter the password again to retry with the same request key.' : proofRejected === 'session' ? intent.outcome === 'unknown' ? 'This retry was rejected because the session proof was stale. An earlier attempt remains unknown. After checking the session, retry deliberately with your current password.' : 'The request was rejected because the session proof was stale. After checking the session, retry deliberately with your current password.' : proofRejected === 'unavailable' ? intent.outcome === 'unknown' ? 'The retry was rejected, but the current session could not be confirmed. An earlier attempt remains unknown. Check the session before retrying.' : 'The previous request was rejected, but the current session could not be confirmed. Check the session before retrying.' : rateLimited ? intent.outcome === 'unknown' ? 'This retry was rate limited. An earlier attempt remains unknown. After the wait, retry with your current password and the same request key.' : 'The server rejected this request temporarily. After the wait, retry with your current password and the same request key.' : intent.retryRejected === 'password' ? 'The last retry was rejected because its password proof was invalid. An earlier attempt remains unknown. Reenter your password to retry with the same request key.' : intent.retryRejected === 'session' ? 'The last retry was rejected because its session proof was stale. An earlier attempt remains unknown. Check the session, then retry with the same request key.' : intent.retryRejected === 'permission' ? 'The last retry was rejected for lack of permission. An earlier attempt remains unknown; confirm authority before reconciling it.' : intent.retryRejected === 'other' ? 'The last retry was rejected. An earlier attempt remains unknown; keep using the same request key while reconciling it.' : intent.outcome === 'rejected' ? 'The server rejected this request before completing it. Reenter your current password to retry with the same request key.' : 'The registration may have completed. Retry uses the same request key and requires your current password again. A replay cannot return the one-time token.'}</p>{proofRejected === 'unavailable' && <Button variant="outline" onClick={() => void checkAuthorityAgain()}>Check session</Button>}{!(conflicted || intent.outcome === 'conflict') && proofRejected !== 'unavailable' && <Button variant="outline" disabled={Date.now() < throttleUntil} onClick={() => { setRegisterOpen(true); setPassword(''); }}>Retry registration</Button>}<Button variant="ghost" onClick={() => setAbandonPrompt(true)}>Abandon request</Button></Panel>}
    {rateLimited && now < throttleUntil && <p role="status">Try registration again in {Math.ceil((throttleUntil - now) / 1000)} seconds.</p>}
    {loading && <LoadingIndicator label="Loading agent records" />}
    {!loading && !locationState.agent && <Panel heading="Agent records" className="agents-panel">
      {agents.length === 0 ? <p>{error && !hasCachedData.current ? 'Agent records could not be loaded.' : 'No agents have been registered.'}</p> : <div className="agents-table-scroll" role="region" aria-label="Agent records" tabIndex={0}><table><thead><tr><th>Name</th><th>Display name</th><th>Administration</th><th>Created (UTC)</th><th>Updated (UTC)</th></tr></thead><tbody>{agents.map((item) => <tr key={item.id}><td><button className="agents-link" onClick={() => setQuery({ agent: item.id, agents_cursor: '', credentials_cursor: '' })}>{item.name}</button></td><td>{item.display_name}</td><td>{item.enabled ? 'Enabled' : 'Disabled'}</td><td>{date(item.created_at)}</td><td>{date(item.updated_at)}</td></tr>)}</tbody></table></div>}
      {agentNext && <Button variant="outline" onClick={() => setQuery({ agents_cursor: agentNext })}>Next agents</Button>}
    </Panel>}
    {locationState.agent && <Button variant="outline" onClick={() => setQuery({ agent: '', credentials_cursor: '' })}>Back to agents</Button>}
    {!loading && locationState.agent && agent && <>
      <Panel heading="Agent details" className="agents-panel"><dl className="agents-meta"><div><dt>Name</dt><dd>{agent.name}</dd></div><div><dt>Display name</dt><dd>{agent.display_name}</dd></div><div><dt>Administration</dt><dd>{agent.enabled ? 'Enabled' : 'Disabled'}</dd></div><div><dt>Agent ID</dt><dd>{agent.id}</dd></div><div><dt>Created (UTC)</dt><dd>{date(agent.created_at)}</dd></div><div><dt>Updated (UTC)</dt><dd>{date(agent.updated_at)}</dd></div></dl></Panel>
      <Panel heading="Credential history" className="agents-panel"><p>Scheduled retirement and revocation are separate events. This history does not show whether a credential is currently connected or active.</p>
        {credentials.length === 0 ? <p>No credentials are recorded.</p> : <div className="agents-table-scroll" role="region" aria-label="Credential history" tabIndex={0}><table><thead><tr><th>Credential ID</th><th>Issued (UTC)</th><th>Expires (UTC)</th><th>Scheduled retirement (UTC)</th><th>Revoked (UTC)</th></tr></thead><tbody>{credentials.map((item) => <tr key={item.id}><td>{item.id}</td><td>{date(item.issued_at)}</td><td>{date(item.expires_at)}</td><td>{item.retire_at ? date(item.retire_at) : '—'}</td><td>{item.revoked_at ? date(item.revoked_at) : '—'}</td></tr>)}</tbody></table></div>}
        {credentialNext && <Button variant="outline" onClick={() => setQuery({ credentials_cursor: credentialNext })}>Next credential history</Button>}
      </Panel>
    </>}
    <Dialog open={registerOpen} onClose={closeRegister} title={intent ? 'Retry registration' : 'Register agent'} triggerRef={registerTrigger}>
      <form className="agents-form" noValidate onSubmit={(event) => void submit(event)}>
        {error && registerOpen && <div ref={errorRef} className="agents-error" role="alert" tabIndex={-1}>{error}</div>}
        {!intent && <><TextField id="agent-name" label="Agent name" value={draft.name} onChange={(event) => onDraftChange({ ...draft, name: event.target.value })} disabled={submitBusy} /><TextField id="agent-display-name" label="Display name" value={draft.displayName} onChange={(event) => onDraftChange({ ...draft, displayName: event.target.value })} disabled={submitBusy} /><TextField id="agent-lifetime" label="Lifetime (seconds)" type="number" min="0" max="31536000" value={draft.lifetime} onChange={(event) => onDraftChange({ ...draft, lifetime: event.target.value })} hint="Leave blank or enter 0 to use the server default (0–31536000 seconds)." disabled={submitBusy} /></>}
        <TextField id="agent-current-password" label="Current password" type="password" autoComplete="current-password" value={password} error={passwordFieldError} onChange={(event) => { setPassword(event.target.value); setPasswordFieldError(''); }} disabled={submitBusy} />
        <Button type="submit" pending={submitBusy} disabled={Date.now() < throttleUntil || conflicted}>{intent ? 'Retry registration' : 'Register agent'}</Button>
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
