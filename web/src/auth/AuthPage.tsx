import { Component, lazy, Suspense, useEffect, useRef, useState, type ErrorInfo, type FormEvent, type ReactNode } from 'react';
import { AuthFailure, createAuthClient, type AuthContext } from './client.ts';
import { ApplicationShell, Button, Panel, TextField, ThemeToggle } from '../components/ui/index.ts';
import type { AgentCreateIntent, AgentLifecycleIntent, AgentSafeDraft, SessionRecheck } from '../agents/AgentsPage.tsx';
import './auth.css';

const LazyAgentsPage = lazy(() => import('../agents/AgentsPage.tsx').then((module) => ({ default: module.AgentsPage })));
const LazyDatasourcesPage = lazy(() => import('../datasources/DatasourcesPage.tsx').then((module) => ({ default: module.DatasourcesPage })));
const LazySlisPage = lazy(() => import('../slis/SlisPage.tsx').then((module) => ({ default: module.SlisPage })));
const LazySlosPage = lazy(() => import('../slos/SlosPage.tsx').then((module) => ({ default: module.SlosPage })));
const emptyAgentDraft: AgentSafeDraft = { name: '', displayName: '', lifetime: '' };

class ScreenBoundary extends Component<{ screen: string; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };
  static getDerivedStateFromError() { return { failed: true }; }
  componentDidCatch(_error: Error, _info: ErrorInfo) { /* Failure details may include user data. */ }
  render() { return this.state.failed ? <Panel heading={`${this.props.screen} screen unavailable`}><p>{this.props.screen} could not be loaded.</p><Button variant="outline" onClick={() => location.reload()}>Reload page</Button></Panel> : this.props.children; }
}

type Client = ReturnType<typeof createAuthClient>;
type FieldErrors = Record<string, string>;
type Notice = { kind: 'error' | 'uncertain' | 'success'; text: string } | null;
const defaultClient = createAuthClient();

const messages: Record<string, string> = {
  invalid_request: 'The request could not be accepted. Check the information and try again.',
  unauthenticated: 'Sign-in was not accepted. Check your login and password.',
  origin_rejected: 'This request was rejected. Check your session before trying again.',
  secure_transport_required: 'A secure connection is required.',
  csrf_invalid: 'Your session needs to be refreshed before you can continue.',
  session_present: 'A session is already active. Check your session before continuing.',
  password_unchanged: 'Choose a password different from your current password.',
  password_policy: 'The password does not meet the requirements. Choose a different password.',
  rate_limited: 'Too many attempts. Wait before trying again.',
  method_not_allowed: 'This operation is unavailable.',
  request_timeout: 'The request was rejected before it could be completed.',
  request_too_large: 'The request is too large.',
  unsupported_media_type: 'The request format is unsupported.',
  capability_unavailable: 'This operation is unavailable.',
};

function byteLength(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}

function validContext(value: unknown): value is AuthContext {
  if (typeof value !== 'object' || value === null || !('csrf_token' in value) || !('session' in value)) return false;
  const candidate = value as AuthContext;
  if (typeof candidate.csrf_token !== 'string' || candidate.csrf_token.length === 0) return false;
  if (candidate.session === null) return true;
  return typeof candidate.session === 'object' && typeof candidate.session.user_id === 'string'
    && typeof candidate.session.login === 'string' && typeof candidate.session.role === 'string'
    && typeof candidate.session.restricted === 'boolean';
}

export function AuthPage({ client = defaultClient }: { client?: Client }) {
  const authClient = useRef(client).current;
  const [pathname, setPathname] = useState(() => window.location.pathname);
  const [agentCrumb, setAgentCrumb] = useState<{ id: string; name: string } | null>(null);
  const [agentDetailRoute, setAgentDetailRoute] = useState(() => Boolean(new URLSearchParams(window.location.search).get('agent')));
  const [context, setContext] = useState<AuthContext | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [checking, setChecking] = useState(true);
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState<Notice>(null);
  const [showCheckSession, setShowCheckSession] = useState(false);
  const [errors, setErrors] = useState<FieldErrors>({});
  const [login, setLogin] = useState('');
  const [password, setPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmation, setConfirmation] = useState('');
  const [agentDraft, setAgentDraft] = useState<AgentSafeDraft>(emptyAgentDraft);
  const [agentIntent, setAgentIntent] = useState<AgentCreateIntent | null>(null);
  const [agentLifecycleIntent, setAgentLifecycleIntent] = useState<AgentLifecycleIntent | null>(null);
  const controller = useRef<AbortController | null>(null);
  const live = useRef(false);
  const busy = useRef(false);
  const heading = useRef<HTMLHeadingElement>(null);
  const noticeRef = useRef<HTMLDivElement>(null);
  const identityRef = useRef<string | null>(null);
  const requestGeneration = useRef(0);

  useEffect(() => {
    const nextID = context?.session?.user_id ?? null;
    if (nextID !== null && identityRef.current !== null && nextID !== identityRef.current) {
      setAgentDraft(emptyAgentDraft);
      setAgentIntent(null);
    }
    if (nextID !== null) identityRef.current = nextID;
    if (agentIntent && nextID !== null && agentIntent.userID !== nextID) setAgentIntent(null);
    if (nextID !== null && agentLifecycleIntent && agentLifecycleIntent.userID !== nextID) setAgentLifecycleIntent(null);
  }, [context?.session?.user_id, context?.session?.restricted, context?.session?.role, agentIntent, agentLifecycleIntent]);

  useEffect(() => {
    live.current = true;
    const request = new AbortController();
    controller.current = request;
    void authClient.context({ signal: request.signal }).then((value) => {
      if (!live.current || request.signal.aborted) return;
      setContext(value);
      setUnavailable(false);
      setChecking(false);
    }, () => {
      if (!live.current || request.signal.aborted) return;
      setContext(null);
      setUnavailable(true);
      setChecking(false);
    });
    return () => {
      live.current = false;
      controller.current?.abort();
    };
  }, [authClient]);

  useEffect(() => {
    const changed = () => { setPathname(window.location.pathname); setAgentDetailRoute(Boolean(new URLSearchParams(window.location.search).get('agent'))); };
    window.addEventListener('popstate', changed);
    return () => window.removeEventListener('popstate', changed);
  }, []);

  useEffect(() => {
    heading.current?.focus();
  }, [context, unavailable, checking, pathname]);

  const operational = !checking && !unavailable && Boolean(context?.session && !context.session.restricted);
  useEffect(() => {
    if (!operational || pathname !== '/') return;
    const next = `/agents${location.search}${location.hash}`;
    history.replaceState({}, '', next);
    setPathname('/agents');
    window.dispatchEvent(new PopStateEvent('popstate'));
  }, [operational, pathname]);

  useEffect(() => {
    if (notice?.kind === 'error' || notice?.kind === 'uncertain') noticeRef.current?.focus();
  }, [notice]);

  function startRequest(): AbortController {
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    return request;
  }

  async function checkSession(keepNotice = false) {
    if (busy.current) return;
    busy.current = true;
    const request = startRequest();
    setPending(true);
    setChecking(true);
    setUnavailable(false);
    setErrors({});
    if (!keepNotice) setNotice(null);
    try {
      const value = await authClient.context({ signal: request.signal });
      if (!live.current || request.signal.aborted) return;
      setContext(value);
      setUnavailable(false);
    } catch {
      if (!live.current || request.signal.aborted) return;
      setContext(null);
      setUnavailable(true);
      if (!keepNotice) setNotice({ kind: 'error', text: 'Your session could not be checked. Try again.' });
    } finally {
      if (live.current && !request.signal.aborted) {
        busy.current = false;
        setChecking(false);
        setPending(false);
      }
    }
  }

  async function recheckAgentSession(): Promise<SessionRecheck> {
    if (busy.current) return { confirmed: false };
    const generation = ++requestGeneration.current;
    const request = startRequest();
    try {
      const value = await authClient.context({ signal: request.signal });
      if (!live.current || request.signal.aborted || generation !== requestGeneration.current) return { confirmed: false };
      setContext(value);
      setUnavailable(false);
      return { confirmed: true, context: value };
    } catch {
      if (!live.current || request.signal.aborted || generation !== requestGeneration.current) return { confirmed: false };
      setContext(null);
      setUnavailable(true);
      setShowCheckSession(true);
      setNotice({ kind: 'error', text: 'Your administrator session could not be confirmed. Check your session before continuing.' });
      return { confirmed: false };
    }
  }

  function handleFailure(error: unknown, operation: 'login' | 'password' | 'logout') {
    if (!(error instanceof AuthFailure)) {
      setNotice({ kind: 'error', text: operation === 'logout' ? 'Sign-out could not be confirmed.' : 'Authentication is temporarily unavailable. Try again.' });
      return;
    }
    if (error.kind === 'uncertain') {
      const recovered = validContext(error.recovery) ? error.recovery : null;
      setContext(recovered);
      setUnavailable(!recovered);
      setShowCheckSession(true);
      setNotice({ kind: 'uncertain', text: operation === 'password'
        ? recovered?.session === null
          ? 'The password change may have completed and the new password may already be active. This action was not repeated. Deliberately sign in again when ready.'
          : recovered
            ? 'The password change may have completed. This action was not repeated; checking your session does not confirm whether the new password was set.'
            : 'The password change may have completed. It was not repeated. Check your session; the new password may already be active.'
        : recovered?.session === null
          ? 'The sign-in may have completed, but there is no active session. This action was not repeated. Deliberately sign in again when ready.'
          : 'The sign-in may have completed. This action was not repeated; check your session before continuing.' });
      return;
    }
    if (error.kind === 'unavailable') {
      setShowCheckSession(operation === 'logout');
      setNotice({ kind: 'error', text: operation === 'logout' ? 'Sign-out could not be confirmed.' : 'Authentication is temporarily unavailable. Try again.' });
      return;
    }
    const text = messages[error.code] ?? 'The request was rejected. Check the information and try again.';
    setShowCheckSession(['unauthenticated', 'csrf_invalid', 'session_present', 'origin_rejected'].includes(error.code));
    const retry = error.code === 'rate_limited' && error.retryAfterSeconds
      ? ` Wait at least ${error.retryAfterSeconds} seconds before trying again.` : '';
    setNotice({ kind: 'error', text: `${text}${retry}` });
  }

  async function submitLogin(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy.current || !context || context.session !== null) return;
    setNotice(null);
    setShowCheckSession(false);
    const nextErrors: FieldErrors = {};
    if (!login || byteLength(login) > 256) nextErrors.login = !login ? 'Enter your login.' : 'Login is too long. Check it and try again.';
    if (!password || byteLength(password) > 512) nextErrors.password = !password ? 'Enter your password.' : 'Password is too long. Check it and try again.';
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) {
      setNotice({ kind: 'error', text: 'Review the highlighted fields.' });
      return;
    }
    const input = { login, password };
    setPassword('');
    setNotice(null);
    setShowCheckSession(false);
    busy.current = true;
    requestGeneration.current++;
    setPending(true);
    const request = startRequest();
    try {
      const value = await authClient.login(input, context, { signal: request.signal });
      if (!live.current || request.signal.aborted) return;
      setContext(value);
      setUnavailable(false);
      setErrors({});
    } catch (error) {
      if (live.current && !request.signal.aborted) handleFailure(error, 'login');
    } finally {
      if (live.current && !request.signal.aborted) { busy.current = false; setPending(false); }
    }
  }

  async function submitPassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy.current || !context?.session?.restricted) return;
    setNotice(null);
    setShowCheckSession(false);
    const nextErrors: FieldErrors = {};
    const points = [...newPassword].length;
    if (points < 15) nextErrors.newPassword = 'Use at least 15 characters.';
    else if (points > 128 || byteLength(newPassword) > 512) nextErrors.newPassword = 'Use 128 characters or fewer.';
    if (confirmation !== newPassword) nextErrors.confirmation = 'Enter the same new password again.';
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length) {
      setNotice({ kind: 'error', text: 'Review the highlighted fields.' });
      return;
    }
    const input = { new_password: newPassword };
    setNewPassword('');
    setConfirmation('');
    setNotice(null);
    setShowCheckSession(false);
    busy.current = true;
    requestGeneration.current++;
    setPending(true);
    const request = startRequest();
    try {
      const value = await authClient.changePassword(input, context, { signal: request.signal });
      if (!live.current || request.signal.aborted) return;
      setContext(value);
      setUnavailable(false);
      setErrors({});
      setNotice({ kind: 'success', text: 'Your permanent password is set.' });
    } catch (error) {
      if (live.current && !request.signal.aborted) handleFailure(error, 'password');
    } finally {
      if (live.current && !request.signal.aborted) { busy.current = false; setPending(false); }
    }
  }

  async function signOut() {
    if (busy.current || !context?.session) return;
    busy.current = true;
    requestGeneration.current++;
    setNewPassword('');
    setConfirmation('');
    setAgentDraft(emptyAgentDraft);
    setAgentIntent(null);
    setAgentLifecycleIntent(null);
    requestGeneration.current++;
    setErrors({});
    setNotice(null);
    setPending(true);
    const request = startRequest();
    let logoutSucceeded = false;
    try {
      await authClient.logout(context, { signal: request.signal });
      if (!live.current || request.signal.aborted) return;
      logoutSucceeded = true;
      setContext(null);
      setChecking(true);
      const refreshed = await authClient.context({ signal: request.signal });
      if (!live.current || request.signal.aborted) return;
      setContext(refreshed);
      setUnavailable(false);
    } catch (error) {
      if (live.current && !request.signal.aborted) {
        if (logoutSucceeded) setUnavailable(true);
        setNotice({ kind: 'uncertain', text: 'Sign-out could not be confirmed. It was not repeated. Check your session before continuing.' });
        setShowCheckSession(true);
        if (error instanceof AuthFailure && error.kind === 'uncertain' && validContext(error.recovery)) setContext(error.recovery);
      }
    } finally {
      if (live.current && !request.signal.aborted) {
        busy.current = false;
        setChecking(false);
        setPending(false);
      }
    }
  }

  const title = checking ? 'Welcome to Almanac' : unavailable ? 'Unable to check your session'
    : context?.session?.restricted ? 'Set your permanent password' : 'Sign in';

  const lifecycleNotice = agentLifecycleIntent && (!context?.session || context.session.user_id !== agentLifecycleIntent.userID || context.session.restricted || context.session.role !== 'administrator') && <Panel heading={agentLifecycleIntent.outcome === 'conflict' || agentLifecycleIntent.retryRejected === 'conflict' ? 'Credential command needs reconciliation' : agentLifecycleIntent.outcome === 'rejected' ? 'Credential command was rejected' : 'Credential command outcome unknown'} className="auth-panel"><p>{agentLifecycleIntent.outcome === 'conflict' || agentLifecycleIntent.retryRejected === 'conflict' ? `A ${agentLifecycleIntent.kind} command for agent ${agentLifecycleIntent.agentID} conflicted after an earlier uncertain attempt. Reconcile its original target, command key and resource version before starting another command.` : agentLifecycleIntent.outcome === 'rejected' ? `A ${agentLifecycleIntent.kind} command for agent ${agentLifecycleIntent.agentID} was rejected. Its original target, command key and resource version remain available for a deliberate retry.` : `A ${agentLifecycleIntent.kind} command for agent ${agentLifecycleIntent.agentID} may have completed. Its original target, command key and resource version remain available for a deliberate retry. No password or token was retained.`}</p></Panel>;

  if (operational && context?.session) return <ApplicationShell login={context.session.login} role={context.session.role} pending={pending} showCheckSession={showCheckSession} onCheckSession={() => void checkSession(true)} onSignOut={() => void signOut()}>
    {pathname.startsWith('/slos') || pathname.startsWith('/services/') ? <ScreenBoundary screen="SLO" key={`slos:${context.session.user_id}`}><Suspense fallback={<><h1 className="app-page-heading">SLOs</h1><span className="sr-only" role="status">Opening SLOs</span></>}><LazySlosPage context={context} /></Suspense></ScreenBoundary>
      : pathname.startsWith('/datasources') ? <ScreenBoundary screen="Datasource" key={`datasources:${context.session.user_id}`}><Suspense fallback={<><nav className="app-breadcrumbs" aria-label="Breadcrumb">{pathname === '/datasources' ? <span aria-current="page">Datasources</span> : <><a href="/datasources" data-app-route>Datasources</a><span aria-hidden="true">/</span><span aria-current="page">{pathname === '/datasources/new' ? 'Add datasource' : 'Datasource'}</span></>}</nav><h1 className="app-page-heading">Datasources</h1><span className="sr-only" role="status">Opening datasources</span></>}><LazyDatasourcesPage context={context} /></Suspense></ScreenBoundary>
      : pathname.startsWith('/slis') ? <ScreenBoundary screen="SLI" key={`slis:${context.session.user_id}`}><Suspense fallback={<><nav className="app-breadcrumbs" aria-label="Breadcrumb">{pathname === '/slis' ? <span aria-current="page">SLIs</span> : <><a href="/slis" data-app-route>SLIs</a><span aria-hidden="true">/</span><span aria-current="page">{pathname === '/slis/new' ? 'Create SLI' : 'SLI details'}</span></>}</nav><h1 className="app-page-heading">SLIs</h1><span className="sr-only" role="status">Opening SLIs</span></>}><LazySlisPage context={context} /></Suspense></ScreenBoundary> : <>
    <nav className="app-breadcrumbs" aria-label="Breadcrumb">{agentDetailRoute ? <><a href="/agents" data-app-route>Agents</a><span aria-hidden="true">/</span><span aria-current="page">{agentCrumb?.id === new URLSearchParams(window.location.search).get('agent') ? agentCrumb.name : 'Agent details'}</span></> : <span aria-current="page">Agents</span>}</nav>
    <h1 id="agents-title" className="agents-heading app-page-heading" ref={heading} tabIndex={-1}>Agents</h1>
    {notice && <div ref={noticeRef} className={`auth-notice auth-notice--${notice.kind}`} role={notice.kind === 'success' ? 'status' : 'alert'} tabIndex={-1}>{notice.text}</div>}
    {lifecycleNotice}
    {context.session.role === 'administrator' ? <ScreenBoundary screen="Agent" key={context.session.user_id}><Suspense fallback={<span className="sr-only" role="status">Opening agents</span>}><LazyAgentsPage context={context} recheckSession={recheckAgentSession} suspended={pending || checking || unavailable} draft={agentDraft} onDraftChange={setAgentDraft} intent={agentIntent} onIntentChange={setAgentIntent} lifecycleIntent={agentLifecycleIntent} onLifecycleIntentChange={setAgentLifecycleIntent} onBreadcrumbChange={setAgentCrumb} /></Suspense></ScreenBoundary> : <Panel heading="Agent administration unavailable"><p>Agent administration is available to unrestricted administrators.</p></Panel>}
    </>}
  </ApplicationShell>;

  return <>
    <a className="skip-link" href="#main">Skip to content</a>
    <header className="masthead"><p className="wordmark">ALMANAC</p><ThemeToggle /></header>
    <main id="main" className="auth-page">
      <h1 ref={heading} tabIndex={-1}>{title}</h1>
      {notice && <div ref={noticeRef} className={`auth-notice auth-notice--${notice.kind}`} role={notice.kind === 'success' ? 'status' : 'alert'} tabIndex={-1}>{notice.text}</div>}
      {lifecycleNotice}
      {checking && <span className="sr-only" role="status">Checking your session</span>}
      {!checking && unavailable && <Panel heading="Session status unavailable" className="auth-panel">
        <p>Almanac could not confirm whether you are signed in. Check again to continue.</p>
        <div className="auth-actions">
          <Button onClick={() => void checkSession(showCheckSession)} pending={pending}>{showCheckSession ? 'Check session' : 'Check again'}</Button>
        </div>
      </Panel>}
      {!checking && !unavailable && context?.session === null && <Panel heading="Use your Almanac account" className="auth-panel">
        <form noValidate onSubmit={(event) => void submitLogin(event)}>
          <TextField id="login" label="Login" autoComplete="username" value={login} onChange={(event) => setLogin(event.target.value)} error={errors.login} disabled={pending} />
          <TextField id="password" label="Password" type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} error={errors.password} disabled={pending} />
          <Button type="submit" pending={pending}>Sign in</Button>
          {showCheckSession && <Button variant="outline" onClick={() => void checkSession(true)} pending={pending}>Check session</Button>}
        </form>
      </Panel>}
      {!checking && !unavailable && context?.session?.restricted && <Panel heading="Choose a permanent password" className="auth-panel">
        <p>Choose a different password with 15–128 characters. Spaces are welcome.</p>
        <form noValidate onSubmit={(event) => void submitPassword(event)}>
          <TextField id="new-password" label="New password" type="password" autoComplete="new-password" value={newPassword} onChange={(event) => setNewPassword(event.target.value)} error={errors.newPassword} disabled={pending} />
          <TextField id="confirm-password" label="Confirm new password" type="password" autoComplete="new-password" value={confirmation} onChange={(event) => setConfirmation(event.target.value)} error={errors.confirmation} disabled={pending} />
          <div className="auth-actions"><Button type="submit" pending={pending}>Set password</Button><Button variant="outline" onClick={() => void signOut()} pending={pending}>Sign out</Button>
            {showCheckSession && <Button variant="outline" onClick={() => void checkSession(true)} pending={pending}>Check session</Button>}
          </div>
        </form>
      </Panel>}
    </main>
  </>;
}
