import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AuthPage } from '../src/auth/AuthPage.tsx';
import { type AuthContext, type createAuthClient } from '../src/auth/client.ts';

type AuthClient = ReturnType<typeof createAuthClient>;
const admin: AuthContext = { csrf_token: 'csrf-admin', session: { user_id: 'user-1', login: 'owner', role: 'administrator', restricted: false } };
const viewer: AuthContext = { csrf_token: 'csrf-viewer', session: { user_id: 'user-2', login: 'reader', role: 'Viewer', restricted: false } };
const agentID = '8ed93d7e-51e3-4a3e-9222-3e8a612de20a';
const agentIDB = 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c';
const credentialID = 'b6ad8995-d902-41b8-886c-bb7d5897104a';
const originalShowModal = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'showModal');
const originalClose = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'close');
function makeAuth(context: AuthContext = admin): AuthClient {
  return { context: vi.fn().mockResolvedValue(context), login: vi.fn().mockResolvedValue(admin), changePassword: vi.fn().mockResolvedValue(admin), logout: vi.fn().mockResolvedValue(undefined) };
}
function agent() { return { id: agentID, name: 'west-1', display_name: 'West collector', enabled: true, version: '3', created_at: '2026-10-20T12:00:00Z', updated_at: '2026-10-21T12:00:00Z' }; }
function agentB() { return { ...agent(), id: agentIDB, name: 'east-1', display_name: 'East collector' }; }
function credential() { return { id: credentialID, agent_id: agentID, issued_at: '2026-10-20T12:00:00.123456Z', expires_at: '2027-10-20T12:00:00.654321Z', revoked_at: null, retire_at: '2026-10-22T12:00:00.987654Z' }; }
function credentialB() { return { ...credential(), id: 'f0863cd5-79a6-4ef5-90d1-1c67765b17d2', agent_id: agentIDB }; }
function response(body: unknown, status = 200, headers: Record<string, string> = {}) { return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } }); }
function installDialogs() { Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value: function (this: HTMLDialogElement) { this.setAttribute('open', ''); } }); Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value: function (this: HTMLDialogElement) { this.removeAttribute('open'); } }); }
function installFetch(createResponse?: Response) {
  const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (init?.method === 'POST') return createResponse ?? response({ agent: agent(), credential: credential(), replayed: false, secret_state: 'issued', token: 'one-time-secret' }, 201);
    if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
    if (url.includes(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
    return response({ items: [agent()], next_cursor: '' });
  });
  vi.stubGlobal('fetch', fetcher);
  return fetcher;
}

afterEach(() => { cleanup(); history.replaceState({}, '', '/'); vi.unstubAllGlobals(); vi.restoreAllMocks(); if (originalShowModal) Object.defineProperty(HTMLDialogElement.prototype, 'showModal', originalShowModal); else delete (HTMLDialogElement.prototype as Partial<HTMLDialogElement>).showModal; if (originalClose) Object.defineProperty(HTMLDialogElement.prototype, 'close', originalClose); else delete (HTMLDialogElement.prototype as Partial<HTMLDialogElement>).close; vi.useRealTimers(); });

describe('agent administration screen', () => {
  it('finishes a pending refresh after fragment-only history navigation', async () => {
    const user = userEvent.setup();
    let finish!: (value: Response) => void;
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(response({ items: [agent()], next_cursor: '' }))
      .mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve; })));
    render(<AuthPage client={makeAuth()} />);
    await screen.findByRole('button', { name: 'west-1' });
    await user.click(screen.getByRole('button', { name: 'Refresh agents' }));
    await act(async () => { history.replaceState({}, '', '/#main'); dispatchEvent(new PopStateEvent('popstate')); });
    await act(async () => { finish(response({ items: [agentB()], next_cursor: '' })); });
    expect(await screen.findByRole('button', { name: 'east-1' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Refresh agents' })).toBeEnabled();
  });

  it('offers navigation to the list when a shared detail identifier is invalid', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/?agent=invalid');
    installFetch();
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('Invalid agent identifier.');
    await user.click(screen.getByRole('button', { name: 'Back to agents' }));
    expect(await screen.findByRole('button', { name: 'west-1' })).toBeVisible();
    expect(location.search).toBe('');
  });

  it('ignores an old access warning after a newer page read succeeds', async () => {
    let confirm!: (value: AuthContext) => void;
    const auth = makeAuth();
    vi.mocked(auth.context).mockResolvedValueOnce(admin).mockImplementationOnce(() => new Promise<AuthContext>((resolve) => { confirm = resolve; }));
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(response({ code: 'unauthenticated', message: 'rejected', request_id: 'r', field_errors: [] }, 401))
      .mockResolvedValueOnce(response({ items: [agentB()], next_cursor: '' })));
    render(<AuthPage client={auth} />);
    await waitFor(() => expect(auth.context).toHaveBeenCalledTimes(2));
    await act(async () => { history.pushState({}, '', '/?agents_cursor=next'); dispatchEvent(new PopStateEvent('popstate')); });
    await screen.findByRole('button', { name: 'east-1' });
    await act(async () => { confirm(admin); });
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'east-1' })).toBeVisible();
  });

  it('keeps local validation feedback inside the open registration dialog', async () => {
    const user = userEvent.setup(); installDialogs();
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(response({ items: [], next_cursor: '' })));
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Registration could not be submitted. Correct the highlighted field.');
    expect(screen.getByLabelText('Current password')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByText('Enter your current password to register the agent.')).toBeVisible();
    expect(screen.getByRole('dialog', { name: 'Register agent' })).toContainElement(alert);
    await waitFor(() => expect(alert).toHaveFocus());
    expect(screen.getAllByRole('alert')).toHaveLength(1);
  });

  it('keeps management unmounted for restricted and non-administrator sessions', async () => {
    const restricted: AuthContext = { ...admin, session: { ...admin.session!, restricted: true } };
    installFetch();
    const restrictedAuth = makeAuth(restricted);
    const first = render(<AuthPage client={restrictedAuth} />);
    expect(await screen.findByRole('heading', { name: 'Set your permanent password' })).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'Agents' })).not.toBeInTheDocument();
    first.unmount();
    const fetcher = installFetch();
    render(<AuthPage client={makeAuth(viewer)} />);
    expect(await screen.findByText('Agent administration is available to unrestricted administrators.')).toBeVisible();
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('loads a bounded real client page and uses URL cursors for forward and back navigation', async () => {
    const user = userEvent.setup();
    const fetcher = installFetch();
    render(<AuthPage client={makeAuth()} />);
    expect(await screen.findByRole('button', { name: 'west-1' })).toBeVisible();
    expect(fetcher).toHaveBeenCalledWith('/api/v1/agents?limit=50', expect.objectContaining({ method: 'GET' }));
    await user.click(screen.getByRole('button', { name: 'west-1' }));
    expect(await screen.findByRole('heading', { name: 'Credential history' })).toBeVisible();
    expect(location.search).toContain(`agent=${agentID}`);
    expect(screen.getByText('Scheduled retirement (UTC)')).toBeVisible();
    expect(screen.getByText('2026-10-22 12:00:00.987654Z')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Back to agents' }));
    await waitFor(() => expect(location.search).toBe(''));
  });

  it('keeps list and credential pagination in their bounded URL cursor parameters', async () => {
    const user = userEvent.setup();
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/credentials?')) return response({ items: [credentialB()], next_cursor: url.includes('cursor=history-page-2') ? '' : 'history-page-2' });
      if (url.includes(`/agents/${agentIDB}`)) return response(agentB(), 200, { ETag: `"agent:${agentIDB}:3"` });
      if (url.includes('cursor=agent-page-2')) return response({ items: [agentB()], next_cursor: '' });
      return response({ items: [agent()], next_cursor: 'agent-page-2' });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    expect(await screen.findByRole('button', { name: 'Next agents' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Next agents' }));
    expect(await screen.findByRole('button', { name: 'east-1' })).toBeVisible();
    expect(location.search).toContain('agents_cursor=agent-page-2');
    await user.click(screen.getByRole('button', { name: 'east-1' }));
    expect(await screen.findByRole('button', { name: 'Next credential history' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Next credential history' }));
    expect(location.search).toContain('credentials_cursor=history-page-2');
    expect(fetcher).toHaveBeenCalledWith(`/api/v1/agents/${agentIDB}/credentials?limit=50&cursor=history-page-2`, expect.objectContaining({ method: 'GET' }));
  });

  it('discloses fresh tokens once, copies only on demand, and clears them on dialog close', async () => {
    const user = userEvent.setup(); installDialogs(); history.replaceState({}, '', '/?agents_cursor=page-two'); const fetcher = installFetch();
    const writeText = vi.fn().mockResolvedValue(undefined); Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    render(<AuthPage client={makeAuth()} />);
    await screen.findByRole('button', { name: 'west-1' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'new-east');
    await user.type(screen.getByLabelText('Display name'), 'East collector');
    await user.type(screen.getByLabelText('Lifetime (seconds)'), '0');
    await user.type(screen.getByLabelText('Current password'), 'correct horse');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
    expect(location.search).toContain('agents_cursor=page-two');
    expect(screen.getByLabelText('Agent token', { selector: 'output' })).toHaveTextContent('one-time-secret');
    const request = fetcher.mock.calls.find(([, init]) => init?.method === 'POST');
    expect(request?.[1]?.headers).toMatchObject({ 'Idempotency-Key': expect.any(String), 'X-CSRF-Token': 'csrf-admin' });
    expect(JSON.parse(String(request?.[1]?.body))).toEqual({ name: 'new-east', display_name: 'East collector', current_password: 'correct horse', lifetime_seconds: 0 });
    expect(screen.getByLabelText('Current password')).toHaveValue('');
    expect(writeText).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Copy token' }));
    expect(writeText).toHaveBeenCalledWith('one-time-secret');
    expect(await screen.findByText('Token copied.')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Close token' }));
    expect(screen.queryByText('one-time-secret')).not.toBeInTheDocument();
    await waitFor(() => expect(location.search).toContain(`agent=${agentID}`));
    expect(await screen.findByRole('heading', { name: 'Credential history' })).toBeVisible();
    expect(location.search).not.toContain('agents_cursor');
  });

  it('navigates from an unrelated detail to the fresh result only after closing its token dialog', async () => {
    const user = userEvent.setup(); installDialogs(); history.replaceState({}, '', `/?agent=${agentIDB}`);
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST') return response({ agent: agent(), credential: credential(), replayed: false, secret_state: 'issued', token: 'one-time-secret' }, 201);
      if (url.includes('/credentials?')) return response({ items: [url.includes(agentIDB) ? credentialB() : credential()], next_cursor: '' });
      if (url.endsWith(agentIDB)) return response(agentB(), 200, { ETag: `"agent:${agentIDB}:3"` });
      return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    expect(await screen.findByText('east-1')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'new-agent'); await user.type(screen.getByLabelText('Display name'), 'New agent'); await user.type(screen.getByLabelText('Current password'), 'proof');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
    expect(location.search).toContain(`agent=${agentIDB}`);
    await user.click(screen.getByRole('button', { name: 'Close token' }));
    await waitFor(() => expect(location.search).toContain(`agent=${agentID}`));
    expect(await screen.findByText('west-1')).toBeVisible();
    expect(await screen.findByRole('heading', { name: 'Credential history' })).toBeVisible();
  });

  it('retains an uncertain intent and reuses its original key after deliberate retry with a fresh password', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({ items: [], next_cursor: '' }))
      .mockRejectedValueOnce(new Error('connection lost'))
      .mockResolvedValueOnce(response({ agent: agent(), credential: credential(), replayed: true, secret_state: 'secret_unavailable' }))
      .mockResolvedValueOnce(response(agent(), 200, { ETag: `"agent:${agentID}:3"` }))
      .mockResolvedValueOnce(response({ items: [credential()], next_cursor: '' }));
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'new-east');
    await user.type(screen.getByLabelText('Display name'), 'East collector');
    await user.type(screen.getByLabelText('Current password'), 'first proof');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Outcome unknown' })).toBeVisible();
    expect(screen.getByLabelText('Current password')).toHaveValue('');
    await user.click(screen.getByRole('button', { name: 'Retry registration' }));
    await user.type(screen.getByLabelText('Current password'), 'fresh proof');
    await user.click(screen.getAllByRole('button', { name: 'Retry registration' })[1]!);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/token is no longer available/i));
    const posts = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect(posts).toHaveLength(2);
    const firstKey = new Headers(posts[0]?.[1]?.headers).get('Idempotency-Key');
    const retryKey = new Headers(posts[1]?.[1]?.headers).get('Idempotency-Key');
    expect(firstKey).toBeTruthy(); expect(retryKey).toBe(firstKey);
    expect(JSON.parse(String(posts[1]?.[1]?.body))).toMatchObject({ name: 'new-east', display_name: 'East collector', current_password: 'fresh proof' });
    expect(screen.queryByLabelText('Agent token', { selector: 'output' })).not.toBeInTheDocument();
    expect(location.search).toContain(`agent=${agentID}`);
    expect(await screen.findByRole('heading', { name: 'Credential history' })).toBeVisible();
  });

  it('rechecks session after a 401 without treating the proof failure itself as logout', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn().mockResolvedValueOnce(response({ items: [], next_cursor: '' })).mockResolvedValueOnce(response({ code: 'unauthenticated', message: 'unsafe', request_id: 'r', field_errors: [] }, 401)).mockResolvedValueOnce(response({ items: [], next_cursor: '' }));
    vi.stubGlobal('fetch', fetcher);
    const auth = makeAuth();
    render(<AuthPage client={auth} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'new-east'); await user.type(screen.getByLabelText('Display name'), 'East collector'); await user.type(screen.getByLabelText('Current password'), 'proof');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Signed in' })).toBeVisible();
    expect(auth.context).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('heading', { name: 'Current password needs refreshing' })).toBeVisible();
  });

  it('distinguishes an unavailable session recheck and requires a later successful check', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn().mockResolvedValueOnce(response({ items: [], next_cursor: '' })).mockResolvedValueOnce(response({ code: 'unauthenticated', message: 'unsafe', request_id: 'r', field_errors: [] }, 401));
    vi.stubGlobal('fetch', fetcher);
    const auth = makeAuth(); auth.context = vi.fn().mockResolvedValueOnce(admin).mockRejectedValueOnce(new Error('context unavailable')).mockResolvedValueOnce(admin);
    render(<AuthPage client={auth} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'safe-name'); await user.type(screen.getByLabelText('Display name'), 'Safe display'); await user.type(screen.getByLabelText('Current password'), 'proof');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Unable to check your session' })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Session status unavailable' })).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'Current password needs refreshing' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Check session' }));
    expect(await screen.findByRole('heading', { name: 'Signed in' })).toBeVisible();
    expect(await screen.findByRole('heading', { name: 'Registration was not accepted' })).toBeVisible();
  });

  it('retains the same uncertain intent across a 401, anonymous recheck, and same-user sign-in', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({ items: [], next_cursor: '' }))
      .mockRejectedValueOnce(new Error('lost response'))
      .mockResolvedValueOnce(response({ code: 'unauthenticated', message: 'unsafe', request_id: 'r', field_errors: [] }, 401))
      .mockResolvedValueOnce(response({ items: [], next_cursor: '' }))
      .mockResolvedValueOnce(response({ agent: agent(), credential: credential(), replayed: false, secret_state: 'issued', token: 'one-time-secret' }, 201));
    vi.stubGlobal('fetch', fetcher);
    const auth = makeAuth(); auth.context = vi.fn().mockResolvedValueOnce(admin).mockResolvedValueOnce({ csrf_token: 'csrf-anon', session: null });
    render(<AuthPage client={auth} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'safe-name'); await user.type(screen.getByLabelText('Display name'), 'Safe display'); await user.type(screen.getByLabelText('Current password'), 'must not persist');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Outcome unknown' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Retry registration' }));
    await user.type(screen.getByLabelText('Current password'), 'wrong proof');
    await user.click(screen.getAllByRole('button', { name: 'Retry registration' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    await user.type(screen.getByLabelText('Login'), 'owner'); await user.type(screen.getByLabelText('Password'), 'login proof');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('heading', { name: 'Outcome unknown' })).toBeVisible();
    expect(screen.getByText(/earlier attempt remains unknown/i)).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Retry registration' }));
    expect(screen.getByLabelText('Current password')).toHaveValue('');
    await user.type(screen.getByLabelText('Current password'), 'fresh proof');
    await user.click(screen.getAllByRole('button', { name: 'Retry registration' })[1]!);
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
    const posts = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect(posts).toHaveLength(3);
    const keys = posts.map(([, init]) => new Headers(init?.headers).get('Idempotency-Key'));
    expect(keys[0]).toBeTruthy(); expect(keys[1]).toBe(keys[0]); expect(keys[2]).toBe(keys[0]);
    expect(JSON.parse(String(posts[2]?.[1]?.body))).toMatchObject({ name: 'safe-name', display_name: 'Safe display', current_password: 'fresh proof' });
    expect(screen.queryByText('must not persist')).not.toBeInTheDocument();
  });

  it('honors validated Retry-After by blocking registration and announcing the wait', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn().mockResolvedValueOnce(response({ items: [], next_cursor: '' })).mockResolvedValueOnce(response({ code: 'rate_limited', message: 'unsafe', request_id: 'r', field_errors: [] }, 429, { 'Retry-After': '1' }));
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'safe-name'); await user.type(screen.getByLabelText('Display name'), 'Safe display'); await user.type(screen.getByLabelText('Current password'), 'proof');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('status')).toHaveTextContent(/try registration again in (1|2) seconds/i);
    expect(screen.getByRole('button', { name: 'Retry registration' })).toBeDisabled();
    await new Promise((resolve) => setTimeout(resolve, 1100));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry registration' })).toBeEnabled());
  });

  it('clears a previous detail before a new selection fails and ignores a delayed old response', async () => {
    const user = userEvent.setup(); installDialogs();
    let finishOld!: (value: Response) => void;
    let listReads = 0; let agentReads = 0;
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url === '/api/v1/agents?limit=50') { listReads++; return response({ items: [agent(), agentB()], next_cursor: '' }); }
      if (url.endsWith(`/agents/${agentID}`)) { agentReads++; return agentReads === 1 ? response(agent(), 200, { ETag: `"agent:${agentID}:3"` }) : new Promise<Response>((resolve) => { finishOld = resolve; }); }
      if (url.includes(`/agents/${agentIDB}`) && url.includes('/credentials?')) return response({ items: [credentialB()], next_cursor: '' });
      if (url.includes(`/agents/${agentIDB}`)) return response({ code: 'not_found', message: 'unsafe', request_id: 'r', field_errors: [] }, 404);
      if (url.includes(`/agents/${agentID}/credentials?`)) return response({ items: [credential()], next_cursor: '' });
      return response({ items: [], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await waitFor(() => expect(location.search).toContain(`agent=${agentID}`));
    expect(await screen.findByRole('heading', { name: 'Credential history' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Back to agents' }));
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await waitFor(() => expect(location.search).toContain(`agent=${agentID}`));
    await waitFor(() => expect(finishOld).toBeTypeOf('function'));
    history.pushState({}, '', `/?agent=${agentIDB}`); window.dispatchEvent(new PopStateEvent('popstate'));
    expect(await screen.findByRole('alert')).toBeVisible();
    finishOld(response(agent(), 200, { ETag: `"agent:${agentID}:3"` }));
    await waitFor(() => expect(screen.queryByText('West collector')).not.toBeInTheDocument());
    expect(location.search).toContain(`agent=${agentIDB}`);
    expect(screen.queryByText('West collector')).not.toBeInTheDocument();
    expect(screen.queryByText('2026-10-22 12:00:00.987654Z')).not.toBeInTheDocument();
    expect(listReads).toBe(2);
  });

  it('invalidates a dispatched create when the user abandons its intent', async () => {
    const user = userEvent.setup(); installDialogs(); let finish!: (value: Response) => void;
    const fetcher = vi.fn().mockImplementationOnce(async () => response({ items: [], next_cursor: '' })).mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve; }));
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'safe-name'); await user.type(screen.getByLabelText('Display name'), 'Safe display'); await user.type(screen.getByLabelText('Current password'), 'proof');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    await user.click(screen.getByRole('button', { name: 'Close dialog' }));
    await user.click(screen.getAllByRole('button', { name: 'Abandon request' }).at(-1)!);
    await act(async () => { finish(response({ agent: agent(), credential: credential(), replayed: false, secret_state: 'issued', token: 'one-time-secret' }, 201)); });
    expect(screen.queryByLabelText('Agent token', { selector: 'output' })).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Outcome unknown' })).not.toBeInTheDocument();
  });

  it('invalidates a create across uncertain logout and a later identity change', async () => {
    const user = userEvent.setup(); installDialogs(); let finish!: (value: Response) => void;
    const newAdmin: AuthContext = { csrf_token: 'csrf-new-user', session: { user_id: 'user-2', login: 'second', role: 'administrator', restricted: false } };
    const fetcher = vi.fn().mockImplementationOnce(async () => response({ items: [], next_cursor: '' })).mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve; })).mockImplementation(async () => response({ items: [], next_cursor: '' }));
    vi.stubGlobal('fetch', fetcher);
    const auth = makeAuth(); auth.logout = vi.fn().mockRejectedValue(new Error('logout response unavailable'));
    render(<AuthPage client={auth} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'old-user-name'); await user.type(screen.getByLabelText('Display name'), 'Old user'); await user.type(screen.getByLabelText('Current password'), 'proof');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    await user.click(screen.getByRole('button', { name: 'Close dialog' }));
    await user.click(screen.getByRole('button', { name: 'Keep request' }));
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/sign-out could not be confirmed/i);
    expect(screen.getByRole('heading', { name: 'Signed in' })).toBeVisible();
    await act(async () => { finish(response({ agent: agent(), credential: credential(), replayed: false, secret_state: 'issued', token: 'one-time-secret' }, 201)); });
    expect(screen.queryByLabelText('Agent token', { selector: 'output' })).not.toBeInTheDocument();
    auth.logout = vi.fn().mockResolvedValue(undefined); auth.context = vi.fn().mockResolvedValueOnce({ csrf_token: 'csrf-anon', session: null }); auth.login = vi.fn().mockResolvedValue(newAdmin);
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    await user.type(screen.getByLabelText('Login'), 'second'); await user.type(screen.getByLabelText('Password'), 'password'); await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await screen.findByRole('button', { name: 'Register agent' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    expect(screen.getByLabelText('Agent name')).toHaveValue('');
    expect(screen.getByLabelText('Display name')).toHaveValue('');
  });

  it('clears the safe draft and password on an explicit logout before same-user sign-in', async () => {
    const user = userEvent.setup(); installDialogs();
    const auth = makeAuth(); auth.context = vi.fn().mockResolvedValueOnce(admin).mockResolvedValueOnce({ csrf_token: 'csrf-anon', session: null });
    render(<AuthPage client={auth} />);
    await screen.findByRole('button', { name: 'Register agent' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'private-draft'); await user.type(screen.getByLabelText('Display name'), 'Private draft'); await user.type(screen.getByLabelText('Current password'), 'do not retain');
    await user.click(screen.getByRole('button', { name: 'Close dialog' }));
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    await user.type(screen.getByLabelText('Login'), 'owner'); await user.type(screen.getByLabelText('Password'), 'login password'); await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await screen.findByRole('button', { name: 'Register agent' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    expect(screen.getByLabelText('Agent name')).toHaveValue('');
    expect(screen.getByLabelText('Display name')).toHaveValue('');
    expect(screen.getByLabelText('Current password')).toHaveValue('');
  });

  it('blocks duplicate submits synchronously and clears the password as soon as create dispatches', async () => {
    const user = userEvent.setup(); installDialogs(); let finish!: (value: Response) => void;
    const fetcher = vi.fn().mockImplementationOnce(async () => response({ items: [], next_cursor: '' })).mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve; }));
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name'), 'safe-name'); await user.type(screen.getByLabelText('Display name'), 'Safe display'); await user.type(screen.getByLabelText('Current password'), 'proof');
    const form = screen.getByLabelText('Current password').closest('form')!;
    fireEvent.submit(form); fireEvent.submit(form);
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(screen.getByLabelText('Current password')).toHaveValue('');
    finish(response({ agent: agent(), credential: credential(), replayed: false, secret_state: 'issued', token: 'one-time-secret' }, 201));
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
  });
});
