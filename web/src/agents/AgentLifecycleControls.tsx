import { useEffect, useRef, useState, type FormEvent } from 'react';
import type { AuthContext } from '../auth/client.ts';
import { AgentFailure, createAgentClient, type Agent, type AgentCredential, type AgentDetail, type RotationIssuance } from './client.ts';
import type { AgentLifecycleIntent, SessionRecheck } from './AgentsPage.tsx';
import { Button, Dialog, TextField } from '../components/ui/index.ts';

type Props = { agent: Agent | null; detail: AgentDetail | null; credentials: AgentCredential[]; context: AuthContext; recheckSession: () => Promise<SessionRecheck>; suspended: boolean; selectedAgentID: string; navigationScope: string; actionAvailable: boolean; intent: AgentLifecycleIntent | null; onIntentChange: (intent: AgentLifecycleIntent | null) => void; busy: boolean; onBusyChange: (busy: boolean) => void; refresh: () => Promise<void>; revokeRequest: { credentialID: string; sequence: number } | null; onRevokeRequestHandled: (sequence: number) => void; mutationLock: { current: boolean }; onForbidden: () => void };
const safeFailure = 'The credential action could not be completed. Check the session and try again.';

export function AgentLifecycleControls({ agent, detail, credentials, context, recheckSession, suspended, selectedAgentID, navigationScope, actionAvailable, intent, onIntentChange, busy, onBusyChange, refresh, revokeRequest, onRevokeRequestHandled, mutationLock, onForbidden }: Props) {
  const clientRef = useRef<ReturnType<typeof createAgentClient> | null>(null);
  if (!clientRef.current) clientRef.current = createAgentClient();
  const client = clientRef.current;
  const [kind, setKind] = useState<AgentLifecycleIntent['kind'] | null>(null);
  const [credentialID, setCredentialID] = useState('');
  const [confirm, setConfirm] = useState(false);
  const [password, setPassword] = useState('');
  const [lifetime, setLifetime] = useState('');
  const [overlap, setOverlap] = useState('');
  const [error, setError] = useState('');
  const [passwordError, setPasswordError] = useState('');
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

  useEffect(() => { alive.current = true; return () => { alive.current = false; generation.current++; }; }, []);
  useEffect(() => { if (!retryUntil || now >= retryUntil) return; const timer = setInterval(() => setNow(Date.now()), 250); return () => clearInterval(timer); }, [retryUntil, now]);
  useEffect(() => { generation.current++; setKind(null); setConfirm(false); setPassword(''); setError(''); setNotice(''); setToken(null); setCopyNotice(''); }, [navigationScope]);
  useEffect(() => {
    if (!suspended && isAdministrator) return;
    generation.current++;
    setPassword(''); setToken(null); setCopyNotice(''); setKind(null); setConfirm(false);
  }, [suspended, isAdministrator]);
  useEffect(() => { if (!revokeRequest) return; open('revoke', revokeRequest.credentialID); onRevokeRequestHandled(revokeRequest.sequence); }, [revokeRequest, onRevokeRequestHandled]);
  useEffect(() => { if (token && (suspended || !userID || context.session?.restricted || context.session?.role !== 'administrator')) setToken(null); }, [suspended, userID, context.session?.restricted, context.session?.role, token]);

  function open(action: AgentLifecycleIntent['kind'], id = '') {
    if (suspended || busy || intent || !agent || !actionAvailable || !strongDetail || (action === 'disable' && !agent.enabled) || (action === 'revoke' && !hasCurrentCredential(id))) return;
    setKind(action); setCredentialID(id); setConfirm(false); setPassword(''); setPasswordError(''); setError(''); setNotice(''); setProofUnavailable(false);
  }
  function close() { if (busy) return; setKind(null); setConfirm(false); setPassword(''); setPasswordError(''); setError(''); }
  function title(action: AgentLifecycleIntent['kind']) { return action === 'rotate' ? 'Rotate credential' : action === 'revoke' ? 'Revoke credential' : 'Disable agent'; }

  async function recheckAfterAuthFailure(operation: AgentLifecycleIntent, before: boolean, generationAtCheck: number, rejectedCode: 'unauthenticated' | 'csrf_invalid' | 'permission'): Promise<boolean> {
    const checked = await recheckSession();
    if (!alive.current || generationAtCheck !== generation.current || operation.agentID !== scope.current.agentID || operation.agentID !== scope.current.selectedAgentID || operation.userID !== scope.current.userID) return false;
    if (!checked.confirmed || !checked.context.session || checked.context.session.user_id !== userID || checked.context.session.restricted || checked.context.session.role !== 'administrator') {
      setProofUnavailable(true);
      onIntentChange({ ...operation, retryRejected: rejectedCode === 'csrf_invalid' ? 'session' : rejectedCode === 'permission' ? 'permission' : 'password' });
      setError(before ? 'The credential action was rejected, and administrator access could not be confirmed. Check the session before retrying.' : 'This retry was rejected, but administrator access could not be confirmed. An earlier outcome remains unknown. Check the session before retrying.');
      return false;
    }
    setProofUnavailable(false);
    onIntentChange({ ...operation, retryRejected: undefined });
    setError(rejectedCode === 'permission' ? 'Your administrator session is confirmed. Retry deliberately with your current password and the original command key.' : rejectedCode === 'csrf_invalid'
      ? before ? 'The session proof was stale. Your administrator session is confirmed; reenter your current password and retry deliberately.' : 'This retry was rejected because the session proof was stale. An earlier outcome remains unknown. Your administrator session is confirmed; enter your current password to retry the same command.'
      : before ? 'The current password was rejected. Your administrator session is confirmed; enter the password again and retry deliberately.' : 'This retry was rejected because the current password proof failed. An earlier outcome remains unknown. Your administrator session is confirmed; reenter the password to retry the same command.');
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
    if (!kind || (!existingIntent && (!strongDetail || !actionAvailable)) || lock.current || mutationLock.current || busy || suspended || proofUnavailable || !password || !userID || Date.now() < retryUntil || (kind !== 'rotate' && !confirm)) {
      if (!password) { setPasswordError('Enter your current password.'); setError('Enter your current password to continue.'); }
      else if (kind !== 'rotate' && !confirm) setError('Confirm this destructive action before continuing.');
      else if (Date.now() < retryUntil) setError(`Wait ${Math.ceil((retryUntil - Date.now()) / 1000)} seconds before retrying this command.`);
      else if (!existingIntent && (!strongDetail || !actionAvailable)) setError('Refresh the selected agent before starting a credential action.');
      return;
    }
    const old = existingIntent;
    const value = old ?? (() => {
      const life = kind === 'rotate' && lifetime !== '' ? Number(lifetime) : undefined;
      const over = kind === 'rotate' && overlap !== '' ? Number(overlap) : undefined;
      if (kind === 'rotate' && ((lifetime !== '' && (!Number.isInteger(life) || life! < 0 || life! > 31536000)) || (overlap !== '' && (!Number.isInteger(over) || over! < 0 || over! > 600)))) return null;
      return { kind, key: crypto.randomUUID(), userID, agentID: agent!.id, ...(kind === 'revoke' ? { credentialID } : {}), etag: strongDetail!.etag, ...(life === undefined || life === 0 ? {} : { lifetimeSeconds: life }), ...(over === undefined ? {} : { overlapSeconds: over }), outcome: 'unknown' as const };
    })();
    if (!value) { setError('Enter a lifetime from 0 to 31536000 seconds and overlap from 0 to 600 seconds.'); return; }
    const action = old?.kind ?? kind;
    if (action === 'revoke' && (!value.credentialID || (!old && !hasCurrentCredential(value.credentialID)))) { setError('That credential is already revoked or is no longer in the current history page. Refresh the history before continuing.'); return; }
    const payload = action === 'rotate' ? { current_password: password, ...(value.lifetimeSeconds === undefined ? {} : { lifetime_seconds: value.lifetimeSeconds }), ...(value.overlapSeconds === undefined ? {} : { overlap_seconds: value.overlapSeconds }) } : { current_password: password };
    const generationAtStart = ++generation.current;
    const navigationAtStart = scope.current.navigationScope;
    const earlierUnknown = old?.outcome === 'unknown';
    onIntentChange({ ...value, outcome: 'unknown', retryRejected: undefined, retryAfterDeadline: undefined });
    setPassword(''); setPasswordError(''); setError(''); setNotice(''); lock.current = true; mutationLock.current = true; onBusyChange(true);
    try {
      const result = action === 'rotate'
        ? await client.rotate(value.agentID, payload, context, value.etag, value.key)
        : action === 'revoke'
          ? await client.revoke(value.agentID, value.credentialID!, payload, context, value.etag, value.key)
          : await client.disable(value.agentID, payload, context, value.etag, value.key);
      if (!alive.current || generationAtStart !== generation.current || scope.current.navigationScope !== navigationAtStart || scope.current.agentID !== value.agentID || scope.current.selectedAgentID !== value.agentID || !scope.current.administrator || scope.current.suspended || scope.current.userID !== value.userID) return;
      onIntentChange(null); setKind(null); setConfirm(false); setNotice(result.replayed ? `${title(action)} was already processed. Refreshing the agent and credential history.` : `${title(action)} was accepted. Refreshing the agent and credential history.`);
      if (action === 'rotate' && !result.replayed) { setToken(result as RotationIssuance); setNotice(''); }
      else if (action === 'rotate') setNotice('Rotation was already processed. Its one-time token is no longer available. Refreshing the agent and credential history.');
      await refresh();
    } catch (caught) {
      if (!alive.current || generationAtStart !== generation.current || scope.current.navigationScope !== navigationAtStart || scope.current.agentID !== value.agentID || scope.current.selectedAgentID !== value.agentID || !scope.current.administrator || scope.current.suspended || scope.current.userID !== value.userID) return;
      const failure = caught instanceof AgentFailure ? caught : null;
      if (failure?.kind === 'uncertain' || failure?.kind === 'unavailable') {
        onIntentChange({ ...value, outcome: earlierUnknown ? 'unknown' : 'unknown' }); setKind(null); setError('Outcome unknown. The command may have completed. Retry with the same command key after checking the session and entering your current password.');
      } else if (failure?.code === 'unauthenticated' || failure?.code === 'csrf_invalid') {
        const preserved = { ...value, outcome: earlierUnknown ? 'unknown' as const : 'rejected' as const, retryRejected: earlierUnknown ? 'password' as const : undefined };
        onIntentChange(preserved); setKind(null); await recheckAfterAuthFailure(preserved, !earlierUnknown, generationAtStart, failure.code);
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
  function abandon() { generation.current++; onIntentChange(null); setKind(null); setConfirm(false); setPassword(''); setError(''); setNotice(''); }

  return <>
    {agent && <div className="agents-actions">
      <Button ref={trigger} variant="outline" onClick={() => open('rotate')} disabled={suspended || busy || Boolean(intent) || !actionAvailable || !strongDetail || !agent.enabled}>Rotate credential</Button>
      {agent.enabled && <Button variant="danger" onClick={() => open('disable')} disabled={suspended || busy || Boolean(intent) || !actionAvailable || !strongDetail}>Disable agent</Button>}
    </div>}
    {currentIntent && <section className="agents-unknown" aria-labelledby="lifecycle-outcome-title"><h3 id="lifecycle-outcome-title">{currentIntent.retryRejected === 'conflict' || currentIntent.outcome === 'conflict' ? 'Credential action needs reconciliation' : currentIntent.outcome === 'rejected' ? 'Credential action was rejected' : 'Credential action outcome unknown'}</h3><p>{error || (currentIntent.retryRejected === 'conflict' || currentIntent.outcome === 'conflict' ? 'The server reported a conflict after an earlier uncertain attempt. Reconcile the current record and history, then abandon this command before starting a new one.' : currentIntent.retryRejected === 'session' ? 'The last retry had stale session proof. Check the session, then enter your current password to retry with the same command key.' : currentIntent.retryRejected === 'password' ? 'The last retry had rejected password proof. Enter your current password to retry with the same command key.' : currentIntent.retryRejected === 'permission' ? 'The last retry was rejected for lack of permission. Confirm administrator authority before retrying the original command.' : currentIntent.outcome === 'rejected' ? 'The request was rejected. Enter your current password and retry with the original command key.' : 'The command may have completed. A retry uses the same command key, original resource version and target.')}</p>{retryUntil > now && <p role="status">Try this command again in {Math.ceil((retryUntil - now) / 1000)} seconds.</p>}{proofUnavailable && <Button variant="outline" disabled={busy} onClick={() => void checkCurrentSession()}>Check session</Button>}{currentIntent.outcome !== 'conflict' && currentIntent.retryRejected !== 'conflict' && !proofUnavailable && <Button variant="outline" disabled={busy || retryUntil > now || !scope.current.administrator || scope.current.suspended} onClick={() => { setKind(currentIntent.kind); setCredentialID(currentIntent.credentialID ?? ''); setConfirm(false); setPassword(''); setError(''); }}>Retry {title(currentIntent.kind).toLowerCase()}</Button>}<Button variant="ghost" onClick={abandon}>Abandon command</Button></section>}
    {notice && <p role="status">{notice}</p>}
    <Dialog open={kind !== null} onClose={close} title={kind ? title(kind) : 'Credential action'} triggerRef={trigger}>
      {kind && <form className="agents-form" noValidate onSubmit={(event) => void submit(event)}>
        {kind === 'rotate' && <><p>The replacement credential serves the same agent identity. The old credential retires within the requested overlap.</p>{currentIntent?.kind === 'rotate' ? <><p>This retry keeps the original target, resource version, and rotation options. To change them, abandon this command and reconcile its outcome first.</p><dl><dt>Lifetime (seconds)</dt><dd>{currentIntent.lifetimeSeconds === undefined ? 'Server default' : currentIntent.lifetimeSeconds}</dd><dt>Overlap (seconds)</dt><dd>{currentIntent.overlapSeconds === undefined ? '600 (server default)' : currentIntent.overlapSeconds}</dd></dl></> : <><TextField id="agent-rotation-lifetime" label="Lifetime (seconds)" type="number" min="0" max="31536000" value={lifetime} onChange={(event) => setLifetime(event.target.value)} hint="Leave blank or enter 0 to use the server default (maximum 31536000 seconds)." disabled={busy} /><TextField id="agent-rotation-overlap" label="Overlap (seconds)" type="number" min="0" max="600" value={overlap} onChange={(event) => setOverlap(event.target.value)} hint="Leave blank for 600 seconds; enter 0 for no overlap (0–600 seconds)." disabled={busy} /></>}</>}
        {kind === 'revoke' && <p>Credential ID: <code>{activeCredential?.id ?? credentialID}</code>. This revokes it immediately without deleting the agent identity.</p>}
        {kind === 'disable' && <p>Agent: <code>{agent?.name ?? 'selected agent'}</code> ({agent?.id ?? currentIntent?.agentID}). Disabling revokes all credentials and cancels future work.</p>}
        {kind !== 'rotate' && <label className="agents-confirm"><input type="checkbox" checked={confirm} onChange={(event) => setConfirm(event.target.checked)} />I understand this action takes effect immediately.</label>}
        <TextField id="agent-lifecycle-password" label="Current password" type="password" autoComplete="current-password" value={password} onChange={(event) => { setPassword(event.target.value); setPasswordError(''); }} error={passwordError} disabled={busy} />
        {error && <p className="agents-error" role="alert">{error}</p>}
        {retryUntil > now && <p role="status">Try again in {Math.ceil((retryUntil - now) / 1000)} seconds.</p>}
        <Button type="submit" pending={busy} disabled={busy || retryUntil > now || (kind !== 'rotate' && !confirm)}>{kind === 'rotate' ? 'Rotate credential' : kind === 'revoke' ? 'Revoke credential' : 'Disable agent'}</Button>
      </form>}
    </Dialog>
    <Dialog open={token !== null} onClose={closeToken} title="Agent token" triggerRef={trigger} initialFocusRef={tokenClose}>
      {token && <div className="agents-token"><p>{token.replayed ? 'This rotation was already processed. Its one-time token is no longer available. Review the refreshed credential history.' : 'This replacement token is shown once. It serves the existing agent identity. Copy it now and store it securely.'}</p>{!token.replayed && 'token' in token && <><label htmlFor="rotated-agent-token">Agent token</label><output id="rotated-agent-token" aria-label="Agent token" className="agents-token-value">{token.token}</output><Button onClick={() => void copyToken()}>Copy token</Button>{copyNotice && <p role="status">{copyNotice}</p>}</>}<Button ref={tokenClose} variant="outline" onClick={closeToken}>Close token</Button></div>}
    </Dialog>
  </>;
}
