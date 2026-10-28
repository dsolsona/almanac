import { useEffect, useRef, useState, type FormEvent } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { AgentFailure, createAgentClient, type Agent, type AgentCredential, type AgentDetail, type RotationIssuance } from './client.ts';
import type { AgentLifecycleIntent, SessionRecheck } from './AgentsPage.tsx';
import { DurationControl, describeDuration } from './DurationControl.tsx';
import { Button, Dialog } from '../components/ui/index.ts';

type Props = { agent: Agent | null; detail: AgentDetail | null; credentials: AgentCredential[]; context: AuthContext; recheckSession: () => Promise<SessionRecheck>; suspended: boolean; selectedAgentID: string; navigationScope: string; actionAvailable: boolean; intent: AgentLifecycleIntent | null; onIntentChange: (intent: AgentLifecycleIntent | null) => void; busy: boolean; onBusyChange: (busy: boolean) => void; refresh: () => Promise<void>; revokeRequest: { credentialID: string; sequence: number } | null; onRevokeRequestHandled: (sequence: number) => void; mutationLock: { current: boolean }; onForbidden: () => void; onDeleted: () => void; onRegisterReplacement: () => void };
const safeFailure = 'The credential action could not be completed. Check the session and try again.';

export function AgentLifecycleControls({ agent, detail, credentials, context, recheckSession, suspended, selectedAgentID, navigationScope, actionAvailable, intent, onIntentChange, busy, onBusyChange, refresh, revokeRequest, onRevokeRequestHandled, mutationLock, onForbidden, onDeleted, onRegisterReplacement }: Props) {
  const clientRef = useRef<ReturnType<typeof createAgentClient> | null>(null);
  if (!clientRef.current) clientRef.current = createAgentClient();
  const client = clientRef.current;
  const [kind, setKind] = useState<AgentLifecycleIntent['kind'] | null>(null);
  const [credentialID, setCredentialID] = useState('');
  const [confirm, setConfirm] = useState(false);
  const [dependencies, setDependencies] = useState<{ id: string; name: string; display_name: string }[] | null>(null);
  const [dependencyError, setDependencyError] = useState('');
  const [dependencyCursor, setDependencyCursor] = useState('');
  const dependencyAbort = useRef<AbortController | null>(null);
  const [checkingDependencies, setCheckingDependencies] = useState(false);
  const [lifetime, setLifetime] = useState('7776000');
  const [overlap, setOverlap] = useState('600');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [proofUnavailable, setProofUnavailable] = useState(false);
  const [now, setNow] = useState(Date.now());
  const [token, setToken] = useState<RotationIssuance | null>(null);
  const [copyNotice, setCopyNotice] = useState('');
  const lock = useRef(false);
  const generation = useRef(0);
  const alive = useRef(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const tokenClose = useRef<HTMLButtonElement>(null);
  const userID = context.session?.user_id ?? '';
  const isAdministrator = context.session?.role === 'administrator' && !context.session.restricted;
  const agentID = agent?.id ?? intent?.agentID ?? selectedAgentID;
  const scope = useRef({ agentID, selectedAgentID, navigationScope, userID, administrator: isAdministrator, suspended });
  scope.current = { agentID, selectedAgentID, navigationScope, userID, administrator: isAdministrator, suspended };
  const strongDetail = agent && detail?.agent.id === agent.id && detail.agent.version === agent.version ? detail : null;
  const currentIntent = intent?.agentID === agentID && intent.userID === userID ? intent : null;
  const retryUntil = currentIntent?.retryAfterDeadline ?? 0;
  const activeCredential = credentials.find((item) => item.id === credentialID);
  const hasCurrentCredential = (id: string) => credentials.some((item) => item.id === id && item.revoked_at === null);

  useEffect(() => { alive.current = true; return () => { alive.current = false; generation.current++; dependencyAbort.current?.abort(); }; }, []);
  useEffect(() => { if (!retryUntil || now >= retryUntil) return; const timer = setInterval(() => setNow(Date.now()), 250); return () => clearInterval(timer); }, [retryUntil, now]);
  useEffect(() => { generation.current++; dependencyAbort.current?.abort(); setKind(null); setConfirm(false); setError(''); setNotice(''); setToken(null); setCopyNotice(''); }, [navigationScope]);
  useEffect(() => {
    if (!suspended && isAdministrator) return;
    generation.current++; dependencyAbort.current?.abort();
    setToken(null); setCopyNotice(''); setKind(null); setConfirm(false);
  }, [suspended, isAdministrator]);
  useEffect(() => { if (!revokeRequest) return; open('revoke', revokeRequest.credentialID); onRevokeRequestHandled(revokeRequest.sequence); }, [revokeRequest, onRevokeRequestHandled]);
  useEffect(() => { if (token && (suspended || !userID || context.session?.restricted || context.session?.role !== 'administrator')) setToken(null); }, [suspended, userID, context.session?.restricted, context.session?.role, token]);

  async function loadDependencies(id: string, cursor = '') {
    dependencyAbort.current?.abort(); const controller = new AbortController(); dependencyAbort.current = controller;
    const at = generation.current; if (!cursor) { setDependencies(null); setDependencyCursor(''); } setDependencyError(''); setCheckingDependencies(true);
    try {
      const page = await client.datasources(id, { ...(cursor ? { cursor } : {}), signal: controller.signal });
      if (page.next_cursor && page.next_cursor === cursor) throw new Error('Repeated cursor');
      if (!controller.signal.aborted && alive.current && at === generation.current && scope.current.agentID === id) { setDependencies((previous) => cursor ? [...(previous ?? []), ...page.items] : page.items); setDependencyCursor(page.next_cursor); }
    } catch { if (!controller.signal.aborted && alive.current && at === generation.current) setDependencyError('Datasource assignments could not be checked. Retry before deleting.'); }
    finally { if (alive.current && at === generation.current && !controller.signal.aborted) setCheckingDependencies(false); }
  }
  function open(action: AgentLifecycleIntent['kind'], id = '') {
    if (suspended || busy || intent || !agent || !actionAvailable || !strongDetail || (action === 'disable' && !agent.enabled) || (action === 'revoke' && !hasCurrentCredential(id))) return;
    generation.current++;
    if (action === 'delete') void loadDependencies(agent.id);
    setKind(action); setCredentialID(id); setConfirm(false); setError(''); setNotice(''); setProofUnavailable(false);
  }
  function close() { if (busy) return; generation.current++; dependencyAbort.current?.abort(); setKind(null); setConfirm(false); setError(''); }
  function title(action: AgentLifecycleIntent['kind']) { return action === 'rotate' ? 'Rotate credential' : action === 'revoke' ? 'Revoke credential' : action === 'delete' ? 'Delete agent' : 'Disable agent'; }

  async function recheckAfterAuthFailure(operation: AgentLifecycleIntent, before: boolean, generationAtCheck: number, rejectedCode: 'unauthenticated' | 'csrf_invalid' | 'permission'): Promise<boolean> {
    const checked = await recheckSession();
    if (!alive.current || generationAtCheck !== generation.current || operation.agentID !== scope.current.agentID || operation.agentID !== scope.current.selectedAgentID || operation.userID !== scope.current.userID) return false;
    if (!checked.confirmed || !checked.context.session || checked.context.session.user_id !== userID || checked.context.session.restricted || checked.context.session.role !== 'administrator') {
      setProofUnavailable(true);
      onIntentChange({ ...operation, retryRejected: rejectedCode === 'csrf_invalid' ? 'session' : rejectedCode === 'permission' ? 'permission' : 'session' });
      setError(before ? 'The credential action was rejected, and administrator access could not be confirmed. Check the session before retrying.' : 'This retry was rejected, but administrator access could not be confirmed. An earlier outcome remains unknown. Check the session before retrying.');
      return false;
    }
    setProofUnavailable(false);
    onIntentChange({ ...operation, retryRejected: undefined });
    setError(rejectedCode === 'permission' ? 'Your administrator session is confirmed. Retry deliberately with the original command key.' : rejectedCode === 'csrf_invalid'
      ? before ? 'The session proof was stale. Your administrator session is confirmed; recheck your session and retry deliberately.' : 'This retry was rejected because the session proof was stale. An earlier outcome remains unknown. Your administrator session is confirmed; check your session to retry the same command.'
      : before ? 'The session was rejected. Your administrator session is confirmed; retry deliberately.' : 'This retry was rejected because the session was rejected. An earlier outcome remains unknown. Your administrator session is confirmed; retry the same command.');
    return true;
  }

  async function checkCurrentSession() {
    if (!currentIntent || busy) return;
    const generationAtCheck = generation.current;
    const code = currentIntent.retryRejected === 'permission' ? 'permission' : currentIntent.retryRejected === 'session' ? 'csrf_invalid' : 'unauthenticated';
    const confirmed = await recheckAfterAuthFailure(currentIntent, false, generationAtCheck, code);
    if (confirmed && generationAtCheck === generation.current) await refresh();
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const existingIntent = currentIntent;
    if (!kind || (!existingIntent && (!strongDetail || !actionAvailable)) || lock.current || mutationLock.current || busy || suspended || proofUnavailable || !userID || Date.now() < retryUntil || (kind !== 'rotate' && !confirm) || (kind === 'delete' && !existingIntent && (!dependencies || dependencies.length > 0 || checkingDependencies || Boolean(dependencyCursor) || Boolean(dependencyError)))) {
      if (kind !== 'rotate' && !confirm) setError('Confirm this destructive action before continuing.');
      else if (kind === 'delete' && (!dependencies || dependencies.length > 0 || checkingDependencies || Boolean(dependencyCursor) || Boolean(dependencyError))) setError('Check and update all datasource assignments or moves before deleting this agent.');
      else if (Date.now() < retryUntil) setError(`Wait ${Math.ceil((retryUntil - Date.now()) / 1000)} seconds before retrying this command.`);
      else if (!existingIntent && (!strongDetail || !actionAvailable)) setError('Refresh the selected agent before starting a credential action.');
      return;
    }
    const old = existingIntent;
    const value = old ?? (() => {
      const life = kind === 'rotate' && lifetime !== '' ? Number(lifetime) : undefined;
      const over = kind === 'rotate' && overlap !== '' ? Number(overlap) : undefined;
      if (kind === 'rotate' && ((lifetime !== '' && (!Number.isInteger(life) || life! < 1 || life! > 31536000)) || (overlap !== '' && (!Number.isInteger(over) || over! < 0 || over! > 600)))) return null;
      return { kind, key: crypto.randomUUID(), userID, agentID: agent!.id, ...(kind === 'revoke' ? { credentialID } : {}), etag: strongDetail!.etag, ...(life === undefined || life === 0 ? {} : { lifetimeSeconds: life }), ...(over === undefined ? {} : { overlapSeconds: over }), outcome: 'unknown' as const };
    })();
    if (!value) { setError('Choose a whole number of seconds: expiry must be between 1 second and 1 year, and grace between 0 and 10 minutes.'); return; }
    const action = old?.kind ?? kind;
    if (action === 'revoke' && (!value.credentialID || (!old && !hasCurrentCredential(value.credentialID)))) { setError('That credential is already revoked or is no longer in the current history page. Refresh the history before continuing.'); return; }
    const payload = action === 'rotate' ? { ...(value.lifetimeSeconds === undefined ? {} : { lifetime_seconds: value.lifetimeSeconds }), ...(value.overlapSeconds === undefined ? {} : { overlap_seconds: value.overlapSeconds }) } : {};
    const generationAtStart = ++generation.current;
    const navigationAtStart = scope.current.navigationScope;
    const earlierUnknown = old?.outcome === 'unknown';
    onIntentChange({ ...value, outcome: 'unknown', retryRejected: undefined, retryAfterDeadline: undefined });
    setError(''); setNotice(''); lock.current = true; mutationLock.current = true; onBusyChange(true);
    try {
      const result = action === 'rotate'
        ? await client.rotate(value.agentID, payload, context, value.etag, value.key)
        : action === 'revoke'
          ? await client.revoke(value.agentID, value.credentialID!, {}, context, value.etag, value.key)
          : action === 'delete' ? await client.delete(value.agentID, context, value.etag, value.key) : await client.disable(value.agentID, {}, context, value.etag, value.key);
      if (!alive.current || generationAtStart !== generation.current || scope.current.navigationScope !== navigationAtStart || scope.current.agentID !== value.agentID || scope.current.selectedAgentID !== value.agentID || !scope.current.administrator || scope.current.suspended || scope.current.userID !== value.userID) return;
      onIntentChange(null); setKind(null); setConfirm(false); setNotice(result.replayed ? `${title(action)} was already processed. Refreshing the agent and credential history.` : `${title(action)} was accepted. Refreshing the agent and credential history.`);
      if (action === 'rotate' && !result.replayed) { setToken(result as RotationIssuance); setNotice(''); }
      else if (action === 'rotate') setNotice('Rotation was already processed. Its one-time token is no longer available. Refreshing the agent and credential history.');
      if (action === 'delete') { onDeleted(); return; }
      await refresh();
    } catch (caught) {
      if (!alive.current || generationAtStart !== generation.current || scope.current.navigationScope !== navigationAtStart || scope.current.agentID !== value.agentID || scope.current.selectedAgentID !== value.agentID || !scope.current.administrator || scope.current.suspended || scope.current.userID !== value.userID) return;
      const failure = caught instanceof AgentFailure ? caught : null;
      if (failure?.kind === 'uncertain' || failure?.kind === 'unavailable') {
        onIntentChange({ ...value, outcome: earlierUnknown ? 'unknown' : 'unknown' }); setKind(null); setError('Outcome unknown. The command may have completed. Retry with the same command key after checking the session.');
      } else if (failure?.code === 'unauthenticated' || failure?.code === 'csrf_invalid') {
        const preserved = { ...value, outcome: earlierUnknown ? 'unknown' as const : 'rejected' as const, retryRejected: earlierUnknown ? 'session' as const : undefined };
        onIntentChange(preserved); setKind(null); await recheckAfterAuthFailure(preserved, !earlierUnknown, generationAtStart, failure.code);
      } else if (failure?.code === 'agent_in_use' && action === 'delete') {
        onIntentChange(earlierUnknown ? { ...value, outcome: 'unknown', retryRejected: 'conflict' } : null); setError(earlierUnknown ? 'Datasources use this agent or are moving to it, but an earlier outcome is unknown. Reconcile before starting another command.' : 'Datasources use this agent or are moving to it. Update assignments or cancel moves before deleting.'); void loadDependencies(value.agentID);
      } else if (failure?.code === 'conflict') {
        onIntentChange({ ...value, outcome: earlierUnknown ? 'unknown' : 'conflict', retryRejected: earlierUnknown ? 'conflict' : undefined }); setKind(null); setError('The agent changed before this command was accepted. Refresh and reconcile its current state. Abandon this command explicitly before starting a new one.');
      } else if (failure?.code === 'rate_limited' && failure.retryAfterSeconds) {
        const retryAfterDeadline = Date.now() + failure.retryAfterSeconds * 1000;
        onIntentChange({ ...value, outcome: earlierUnknown ? 'unknown' : 'rejected', retryAfterDeadline }); setNow(Date.now()); setError(`Wait ${failure.retryAfterSeconds} seconds before retrying this command with the same key.`);
      } else if (failure?.code === 'forbidden') {
        onIntentChange(earlierUnknown ? { ...value, outcome: 'unknown', retryRejected: 'permission' } : null); setProofUnavailable(true); onForbidden(); setError(earlierUnknown ? 'This retry was rejected for lack of permission. An earlier outcome remains unknown; confirm authority before reconciling it.' : 'You are not allowed to perform this credential action.');
      } else {
        onIntentChange(earlierUnknown ? { ...value, outcome: 'unknown', retryRejected: 'other' } : null); setError(earlierUnknown ? 'This retry was rejected. An earlier outcome remains unknown. Reconcile current history before choosing to retry or abandon.' : safeFailure);
      }
    } finally { lock.current = false; mutationLock.current = false; onBusyChange(false); }
  }

  async function copyToken() {
    if (!token || token.replayed || !('token' in token)) return;
    try { await navigator.clipboard.writeText(token.token); setCopyNotice('Token copied.'); }
    catch { setCopyNotice('Copy was unavailable. Select and copy the token manually.'); }
  }
  function closeToken() { setToken(null); setCopyNotice(''); requestAnimationFrame(() => document.querySelector<HTMLElement>('.agents-heading')?.focus()); }
  function abandon() { generation.current++; onIntentChange(null); setKind(null); setConfirm(false); setError(''); setNotice(''); }

  return <>
    {agent && <div className="agents-actions">
      <Button ref={trigger} variant="outline" onClick={() => open('rotate')} disabled={suspended || busy || Boolean(intent) || !actionAvailable || !strongDetail || !agent.enabled}>Rotate credential</Button>
      <Button variant="danger" onClick={() => open('delete')} disabled={suspended || busy || Boolean(intent) || !actionAvailable || !strongDetail}>Delete agent</Button>
      {agent.enabled && <Button variant="danger" onClick={() => open('disable')} disabled={suspended || busy || Boolean(intent) || !actionAvailable || !strongDetail}>Disable agent</Button>}
    </div>}
    {currentIntent && <section className="agents-unknown" aria-labelledby="lifecycle-outcome-title"><h3 id="lifecycle-outcome-title">{currentIntent.retryRejected === 'conflict' || currentIntent.outcome === 'conflict' ? 'Credential action needs reconciliation' : currentIntent.outcome === 'rejected' ? 'Credential action was rejected' : 'Credential action outcome unknown'}</h3><p>{error || (currentIntent.retryRejected === 'conflict' || currentIntent.outcome === 'conflict' ? 'The server reported a conflict after an earlier uncertain attempt. Reconcile the current record and history, then abandon this command before starting a new one.' : currentIntent.retryRejected === 'session' ? 'The last retry had stale session proof. Check the session to retry with the same command key.' : currentIntent.retryRejected === 'permission' ? 'The last retry was rejected for lack of permission. Confirm administrator authority before retrying the original command.' : currentIntent.outcome === 'rejected' ? 'The request was rejected. Check your session and retry with the original command key.' : 'The command may have completed. A retry uses the same command key, original resource version and target.')}</p>{retryUntil > now && <p role="status">Try this command again in {Math.ceil((retryUntil - now) / 1000)} seconds.</p>}{proofUnavailable && <Button variant="outline" disabled={busy} onClick={() => void checkCurrentSession()}>Check session</Button>}{currentIntent.outcome !== 'conflict' && currentIntent.retryRejected !== 'conflict' && !proofUnavailable && <Button variant="outline" disabled={busy || retryUntil > now || !scope.current.administrator || scope.current.suspended} onClick={() => { setKind(currentIntent.kind); setCredentialID(currentIntent.credentialID ?? ''); setConfirm(false); setError(''); }}>Retry {title(currentIntent.kind).toLowerCase()}</Button>}<Button variant="ghost" onClick={abandon}>Abandon command</Button></section>}
    {notice && <p role="status">{notice}</p>}
    <Dialog open={kind !== null} onClose={close} title={kind ? title(kind) : 'Credential action'} triggerRef={trigger}>
      {kind && <form className="agents-form" noValidate onSubmit={(event) => void submit(event)}>
        {kind === 'rotate' && <><p>The replacement credential serves the same agent identity.</p>{currentIntent?.kind === 'rotate' ? <><p>This retry keeps the original target, resource version, and rotation options. To change them, abandon this command and reconcile its outcome first.</p><dl><dt>New credential expires after</dt><dd>{describeDuration(currentIntent.lifetimeSeconds ?? 7776000)}</dd><dt>Keep the previous credential valid for</dt><dd>{describeDuration(currentIntent.overlapSeconds ?? 600)}</dd></dl></> : <><DurationControl id="agent-rotation-lifetime" label="New credential expires after" value={lifetime} onChange={setLifetime} disabled={busy} /><DurationControl id="agent-rotation-overlap" label="Keep the previous credential valid for" value={overlap} onChange={setOverlap} grace disabled={busy} /></>}</>}
        {kind === 'revoke' && <p>Credential ID: <code>{activeCredential?.id ?? credentialID}</code>. This revokes it immediately without deleting the agent identity.</p>}
        {kind === 'delete' && <><p>Deleting revokes all credentials. Datasources using this agent, including disabled datasources, must be updated before deletion.</p>{checkingDependencies && <span className="sr-only" role="status">Checking datasource assignments</span>}{dependencyError && <p role="alert">{dependencyError}</p>}{dependencies && (dependencies.length ? <div className="agents-dependencies"><p>These datasources use this agent or are being moved to it. Open each datasource to update its assignment or cancel a move before deleting.</p><ul>{dependencies.map((source) => <li key={source.id}><a href={`/datasources/${source.id}`} data-app-route>{source.display_name} ({source.name})</a></li>)}</ul><Button variant="outline" onClick={() => { close(); onRegisterReplacement(); }}>Register replacement agent</Button></div> : !dependencyCursor && !dependencyError ? <p>No datasources use this agent.</p> : null)}{dependencyCursor && <Button variant="outline" disabled={checkingDependencies || busy} onClick={() => void loadDependencies(agentID, dependencyCursor)}>Load more datasources</Button>}<Button variant="outline" disabled={checkingDependencies || busy} onClick={() => void loadDependencies(agentID)}>Refresh assignments</Button></>}
        {kind === 'disable' && <p>Agent: <code>{agent?.name ?? 'selected agent'}</code> ({agent?.id ?? currentIntent?.agentID}). Disabling revokes all credentials and cancels future work.</p>}
        {kind !== 'rotate' && (kind !== 'delete' || currentIntent?.kind === 'delete' || (!checkingDependencies && !dependencyError && dependencies !== null && dependencies.length === 0 && !dependencyCursor)) && <label className="agents-confirm"><input type="checkbox" checked={confirm} onChange={(event) => setConfirm(event.target.checked)} />I understand this action takes effect immediately.</label>}
        {error && <p className="agents-error" role="alert">{error}</p>}
        {retryUntil > now && <p role="status">Try again in {Math.ceil((retryUntil - now) / 1000)} seconds.</p>}
        <Button type="submit" pending={busy} disabled={busy || retryUntil > now || (kind !== 'rotate' && !confirm) || (kind === 'delete' && !currentIntent && (checkingDependencies || !dependencies || dependencies.length > 0 || Boolean(dependencyCursor) || Boolean(dependencyError)))}>{kind === 'rotate' ? 'Rotate credential' : kind === 'revoke' ? 'Revoke credential' : kind === 'delete' ? 'Delete agent' : 'Disable agent'}</Button>
      </form>}
    </Dialog>
    <Dialog open={token !== null} onClose={closeToken} title="Agent token" triggerRef={trigger} initialFocusRef={tokenClose}>
      {token && <div className="agents-token"><p>{token.replayed ? 'This rotation was already processed. Its one-time token is no longer available. Review the refreshed credential history.' : 'This replacement token is shown once. It serves the existing agent identity. Copy it now and store it securely.'}</p>{!token.replayed && 'token' in token && <><label htmlFor="rotated-agent-token">Agent token</label><output id="rotated-agent-token" aria-label="Agent token" className="agents-token-value">{token.token}</output><Button onClick={() => void copyToken()}>Copy token</Button>{copyNotice && <p role="status">{copyNotice}</p>}</>}<Button ref={tokenClose} variant="outline" onClick={closeToken}>Close token</Button></div>}
    </Dialog>
  </>;
}
