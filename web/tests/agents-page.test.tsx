import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AuthPage } from '../src/auth/AuthPage.tsx';
import { AgentsPage } from '../src/agents/AgentsPage.tsx';
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
function runtime(overrides: Record<string, unknown> = {}) { return { reachability: 'connected', protocol: 'almanac-agent/1', software_version: '1.2.3', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['live_query'] }], last_heartbeat_at: '2026-10-22T13:14:15.123456Z', desired_configuration_revision: '9223372036854775807', applied_configuration_revision: '0', scheduler_health: 'ready', collection_progress: 'running', configuration_failure_code: null, configuration_failure_revision: null, last_attempt_at: '2026-10-22T13:13:00Z', last_successful_query_at: null, next_expected_execution_at: '2026-10-22T13:15:00Z', collection_explanation: 'no_assignment', ...overrides }; }
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
  it('keeps known blockers visible when a later dependency page fails', async () => {
    const user = userEvent.setup(); installDialogs();
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/datasources?')) { if (url.includes('cursor=next')) throw new Error('offline'); return response({ items: [{ id: agentIDB, name: 'first', display_name: 'First' }], next_cursor: 'next' }); }
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.includes(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    }));
    render(<AuthPage client={makeAuth()} />); await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Delete agent' }));
    const dialog = screen.getByRole('dialog', { name: 'Delete agent' });
    await within(dialog).findByRole('link', { name: 'First (first)' });
    await user.click(within(dialog).getByRole('button', { name: 'Load more datasources' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('could not be checked');
    expect(within(dialog).getByRole('link', { name: 'First (first)' })).toBeVisible();
    expect(within(dialog).queryByRole('checkbox')).not.toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Delete agent' })).toBeDisabled();
  });
  it('never enables deletion when datasource checks fail', async () => {
    const user = userEvent.setup(); installDialogs();
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/datasources?')) throw new Error('offline');
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.includes(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    }));
    render(<AuthPage client={makeAuth()} />); await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Delete agent' }));
    const dialog = screen.getByRole('dialog', { name: 'Delete agent' });
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('could not be checked');
    expect(within(dialog).queryByRole('checkbox')).not.toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Delete agent' })).toBeDisabled();
    expect(within(dialog).queryByText('No datasources use this agent.')).not.toBeInTheDocument();
  });
  it('blocks deletion until all datasource pages are reassigned and refreshed', async () => {
    const user = userEvent.setup(); installDialogs(); let assigned = true;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'DELETE') return response({ agent: { ...agent(), enabled: false }, credential: null, changed: true, revoked_credential_count: '1', replayed: false });
      if (url.includes('/datasources?')) return response(assigned ? (url.includes('cursor=next') ? { items: [{ id: agentIDB, name: 'second', display_name: 'Second source' }], next_cursor: '' } : { items: [{ id: agentID, name: 'first', display_name: 'First source' }], next_cursor: 'next' }) : { items: [], next_cursor: '' });
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.includes(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher); render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Delete agent' }));
    const dialog = screen.getByRole('dialog', { name: 'Delete agent' });
    expect(await within(dialog).findByRole('link', { name: 'First source (first)' })).toHaveAttribute('href', `/datasources/${agentID}`);
    expect(within(dialog).getByText(/are being moved to it/)).toBeVisible();
    expect(within(dialog).getByRole('link', { name: 'First source (first)' })).toHaveAttribute('data-app-route');
    await user.click(within(dialog).getByRole('button', { name: 'Load more datasources' }));
    expect(await within(dialog).findByRole('link', { name: 'Second source (second)' })).toBeVisible();
    expect(within(dialog).queryByRole('checkbox')).not.toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Delete agent' })).toBeDisabled();
    assigned = false; await user.click(within(dialog).getByRole('button', { name: 'Refresh assignments' }));
    await within(dialog).findByText('No datasources use this agent.');
    await user.click(within(dialog).getByRole('checkbox'));
    await user.click(within(dialog).getByRole('button', { name: 'Delete agent' }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith(`/api/v1/agents/${agentID}`, expect.objectContaining({ method: 'DELETE' })));
    expect(await screen.findByRole('status')).toHaveTextContent('Agent deleted');
    await screen.findByRole('button', { name: 'west-1' });
  });
  it('offers readable credential defaults without asking for a password', async () => {
    const user = userEvent.setup(); installDialogs(); installFetch();
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    const dialog = screen.getByRole('dialog', { name: 'Rotate credential' });
    expect(within(dialog).getByLabelText('New credential expires after')).toHaveValue('7776000');
    expect(within(dialog).getByLabelText('Keep the previous credential valid for')).toHaveValue('600');
    expect(within(dialog).queryByLabelText('Current password')).not.toBeInTheDocument();
  });

  it('rejects fractional seconds and excessive custom grace without sending a command', async () => {
    const user = userEvent.setup(); installDialogs(); const fetcher = installFetch();
    render(<AuthPage client={makeAuth()} />); await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    const dialog = screen.getByRole('dialog', { name: 'Rotate credential' });
    await user.selectOptions(within(dialog).getByLabelText('New credential expires after'), 'custom');
    await user.type(within(dialog).getByLabelText('Custom amount'), '0.5');
    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    expect(within(dialog).getByRole('alert')).toHaveTextContent('whole number of seconds');
    await user.selectOptions(within(dialog).getByLabelText('New credential expires after'), '7776000');
    await user.selectOptions(within(dialog).getByLabelText('Keep the previous credential valid for'), 'custom');
    await user.type(within(dialog).getByLabelText('Custom amount'), '601');
    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    expect(within(dialog).getByRole('alert')).toHaveTextContent('10 minutes');
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);
  });
  it('exposes one keyboard-scrollable agent table within its named panel', async () => {
    installFetch(); render(<AgentsPage context={admin} recheckSession={async () => ({ confirmed: true, context: admin })} suspended={false} draft={{ name: '', displayName: '', lifetime: '' }} onDraftChange={vi.fn()} intent={null} onIntentChange={vi.fn()} lifecycleIntent={null} onLifecycleIntentChange={vi.fn()} />);
    await screen.findByRole('button', { name: 'west-1' });
    const regions = screen.getAllByRole('region', { name: 'Agent records' });
    const scrollRegions = regions.filter((region) => region.classList.contains('agents-table-scroll'));
    expect(scrollRegions).toHaveLength(1);
    expect(scrollRegions[0]).toHaveAttribute('tabindex', '0');
  });

  it('keeps exact credential values and the action together', async () => {
    const user = userEvent.setup(); installDialogs(); installFetch();
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    const record = await screen.findByRole('region', { name: `Credential ${credentialID}` });
    expect(within(record).getByText(credentialID)).toBeVisible();
    expect(within(record).getByText('2026-10-20 12:00:00.123456Z')).toBeVisible();
    expect(within(record).getByRole('button', { name: 'Revoke credential' })).toBeVisible();
  });

  it('suggests a valid identifier from Local and preserves an explicit identifier choice', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = installFetch();
    render(<AuthPage client={makeAuth()} />);
    await screen.findByRole('button', { name: 'west-1' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    const dialog = screen.getByRole('dialog', { name: 'Register agent' });
    const displayName = within(dialog).getByLabelText('Display name');
    const name = within(dialog).getByLabelText('Agent name (identifier)');
    await user.type(displayName, 'Local');
    expect(name).toHaveValue('local');
    await user.clear(displayName);
    await user.type(displayName, 'Local Cluster');
    expect(name).toHaveValue('local-cluster');
    await user.clear(name);
    await user.type(name, 'local-primary');
    await user.clear(displayName);
    await user.type(displayName, 'Local Site');
    expect(name).toHaveValue('local-primary');
    await user.click(within(dialog).getByRole('button', { name: 'Register agent' }));
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
    const create = fetcher.mock.calls.find(([url, init]) => String(url) === '/api/v1/agents' && init?.method === 'POST');
    expect(JSON.parse(String(create?.[1]?.body))).toEqual({ name: 'local-primary', display_name: 'Local Site', lifetime_seconds: 7776000 });
  });

  it('identifies a duplicate agent identifier on the editable field', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = installFetch(response({ type: 'about:blank', title: 'Conflict', status: 409, code: 'conflict', message: 'private backend detail', request_id: 'request-id', field_errors: [] }, 409));
    render(<AuthPage client={makeAuth()} />);
    await screen.findByRole('button', { name: 'west-1' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    const dialog = screen.getByRole('dialog', { name: 'Register agent' });
    await user.type(within(dialog).getByLabelText('Display name'), 'Local');
    await user.click(within(dialog).getByRole('button', { name: 'Register agent' }));
    expect(within(dialog).getByLabelText('Agent name (identifier)')).toHaveAttribute('aria-invalid', 'true');
    expect(within(dialog).getByText('An agent with this identifier already exists. Choose a different name.')).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'Registration needs reconciliation' })).not.toBeInTheDocument();
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it('does not silently normalize an explicitly entered capitalized identifier', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = installFetch();
    render(<AuthPage client={makeAuth()} />);
    await screen.findByRole('button', { name: 'west-1' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    const dialog = screen.getByRole('dialog', { name: 'Register agent' });
    const name = within(dialog).getByLabelText('Agent name (identifier)');
    await user.type(within(dialog).getByLabelText('Display name'), 'Local');
    expect(name).toHaveValue('local');
    await user.clear(name);
    await user.type(name, 'Local');
    await user.click(within(dialog).getByRole('button', { name: 'Register agent' }));
    expect(name).toHaveValue('Local');
    expect(name).toHaveAttribute('aria-invalid', 'true');
    expect(within(dialog).getByText('Use 1–63 lowercase letters, numbers, or hyphens, starting with a letter.', { selector: '.ui-error' })).toBeVisible();
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(0);
  });

  it('registers from the signed-in administrator session without asking for a password again', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = installFetch();
    render(<AuthPage client={makeAuth()} />);
    await screen.findByRole('button', { name: 'west-1' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    const dialog = screen.getByRole('dialog', { name: 'Register agent' });
    expect(within(dialog).queryByLabelText('Current password')).not.toBeInTheDocument();
    await user.type(within(dialog).getByLabelText('Agent name (identifier)'), 'session-agent');
    await user.type(within(dialog).getByLabelText('Display name'), 'Session Agent');
    await user.click(within(dialog).getByRole('button', { name: 'Register agent' }));
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
    const create = fetcher.mock.calls.find(([url, init]) => String(url) === '/api/v1/agents' && init?.method === 'POST');
    expect(JSON.parse(String(create?.[1]?.body))).toEqual({ name: 'session-agent', display_name: 'Session Agent', lifetime_seconds: 7776000 });
  });

  it('shows only a validated request ID and safe guidance for an unavailable registration response', async () => {
    const user = userEvent.setup(); installDialogs();
    const requestID = '0123456789abcdef0123456789abcdef';
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({ items: [], next_cursor: '' }))
      .mockResolvedValueOnce(response({ type: 'about:blank', title: 'Unavailable', status: 503, code: 'management_unavailable', message: 'private backend detail', request_id: requestID, field_errors: [] }, 503));
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'outage-agent');
    await user.type(screen.getByLabelText('Display name'), 'Outage Agent');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Registration not confirmed' })).toBeVisible();
    expect(screen.getByText(requestID, { exact: true })).toBeVisible();
    expect(screen.getByText(/temporarily unavailable/)).toBeVisible();
    expect(screen.queryByText('private backend detail')).not.toBeInTheDocument();
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it('announces settled uncertainty and clears an earlier retry rejection before a later outage', async () => {
    const user = userEvent.setup(); installDialogs();
    const requestID = 'abcdef0123456789abcdef0123456789';
    const unavailable = response({ type: 'about:blank', title: 'Unavailable', status: 503, code: 'management_unavailable', message: 'private detail', request_id: requestID, field_errors: [] }, 503);
    let creates = 0;
    const fetcher = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method !== 'POST') return response({ items: [], next_cursor: '' });
      creates++;
      if (creates === 1) throw new Error('lost response');
      if (creates === 2) return response({ type: 'about:blank', title: 'Unauthorized', status: 401, code: 'unauthenticated', message: 'private detail', request_id: requestID, field_errors: [] }, 401);
      return unavailable;
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'retry-agent');
    await user.type(screen.getByLabelText('Display name'), 'Retry Agent');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Registration not confirmed' })).toBeVisible();
    expect(screen.getByRole('status')).toHaveFocus();
    await user.click(screen.getByRole('button', { name: 'Retry registration' }));
    await user.click(screen.getAllByRole('button', { name: 'Retry registration' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Session needs checking' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Retry registration' }));
    await user.click(screen.getAllByRole('button', { name: 'Retry registration' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Registration not confirmed' })).toBeVisible();
    expect(screen.getByText(/temporarily unavailable/)).toBeVisible();
    expect(screen.getByText(requestID, { exact: true })).toBeVisible();
    expect(screen.queryByText(/last retry was rejected because the session/)).not.toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveFocus();
    const posts = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect(posts).toHaveLength(3);
    const keys = posts.map(([, init]) => new Headers(init?.headers).get('Idempotency-Key'));
    expect(keys[0]).toBeTruthy(); expect(keys[1]).toBe(keys[0]); expect(keys[2]).toBe(keys[0]);
    expect(JSON.parse(String(posts[2]?.[1]?.body))).toEqual({ name: 'retry-agent', display_name: 'Retry Agent', lifetime_seconds: 7776000 });
    expect(screen.queryByLabelText('Current password')).not.toBeInTheDocument();
  });

  it('finishes a pending refresh after fragment-only history navigation', async () => {
    const user = userEvent.setup();
    let finish!: (value: Response) => void;
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(response({ items: [agent()], next_cursor: '' }))
      .mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve; })));
    render(<AuthPage client={makeAuth()} />);
    expect(await screen.findByRole('button', { name: 'west-1' })).toBeVisible();
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
    expect(alert).toHaveTextContent('Enter an agent name and display name, and an expiry between 1 second and 1 year as a whole number of seconds.');
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

  it('shows assigned active collection without claiming there are no datasources', async () => {
    const user = userEvent.setup();
    const live = { ...agent(), runtime: runtime({ collection_explanation: 'none', last_successful_query_at: '2026-10-22T13:13:30Z' }) };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/credentials?')) return response({ items: [], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(live, 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [live], next_cursor: '' });
    }));
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    const panel = await screen.findByRole('region', { name: 'Agent runtime' });
    expect(panel).toHaveTextContent('2026-10-22 13:13:30Z');
    expect(panel).not.toHaveTextContent('No datasources are assigned to this agent.');
    expect(panel).not.toHaveTextContent('Collection explanation');
  });

  it('shows connection, collection, capability, revision and query signals without merging them', async () => {
    const user = userEvent.setup();
    const live = { ...agent(), runtime: runtime({ configuration_failure_code: 'apply_failed', configuration_failure_revision: '2' }) };
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/credentials?')) return response({ items: [], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(live, 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [live], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    const panel = await screen.findByRole('region', { name: 'Agent runtime' });
    expect(panel.querySelector('.agents-runtime__state')).toHaveTextContent('Connected');
    expect(panel).toHaveTextContent('almanac-agent/1');
    expect(panel).toHaveTextContent('1.2.3');
    expect(panel).toHaveTextContent('prometheus');
    expect(panel).toHaveTextContent('live_query');
    expect(panel).toHaveTextContent('9223372036854775807');
    expect(panel).toHaveTextContent('Applied configuration revisionNot applied');
    expect(panel).toHaveTextContent('Configuration failureapply_failed (revision 2)');
    expect(panel).toHaveTextContent('2026-10-22 13:14:15.123456Z');
    expect(panel).toHaveTextContent('No successful query has been reported.');
    expect(panel).toHaveTextContent('No datasources are assigned to this agent.');
    expect(panel).toHaveTextContent('Backend observation acceptance freshness is not available.');
  });

  it.each([
    { name: 'never connected', enabled: true, runtime: undefined, expected: 'Never connected' },
    { name: 'unreachable', enabled: true, runtime: runtime({ reachability: 'unreachable' }), expected: 'Unreachable' },
    { name: 'incompatible', enabled: true, runtime: runtime({ reachability: 'incompatible' }), expected: 'Incompatible' },
    { name: 'disabled despite a recent connection', enabled: false, runtime: runtime(), expected: 'Disabled by administrator' },
  ])('presents $name without implying healthy operation', async ({ enabled, runtime: agentRuntime, expected }) => {
    const user = userEvent.setup();
    const live = { ...agent(), enabled, ...(agentRuntime === undefined ? {} : { runtime: agentRuntime }) };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/credentials?')) return response({ items: [], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(live, 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [live], next_cursor: '' });
    }));
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    const panel = await screen.findByRole('region', { name: 'Agent runtime' });
    expect(panel.querySelector('.agents-runtime__state')).toHaveTextContent(expected);
    if (expected === 'Never connected') expect(panel).toHaveTextContent('No heartbeat has been received');
    if (expected === 'Disabled by administrator') expect(panel).toHaveTextContent('not operational while disabled');
  });

  it('shows an explicit empty capability state', async () => {
    const user = userEvent.setup();
    const live = { ...agent(), runtime: runtime({ capabilities: [] }) };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/credentials?')) return response({ items: [], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(live, 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [live], next_cursor: '' });
    }));
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    expect(await screen.findByText('No provider capabilities were reported.')).toBeVisible();
  });

  it('renders repeated provider capability tuples as distinct entries', async () => {
    const user = userEvent.setup();
    const live = { ...agent(), runtime: runtime({ capabilities: [
      { provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['live_query'] },
      { provider: 'prometheus', configuration_formats: ['prometheus-remote-write-v1'], operations: ['backfill'] },
    ] }) };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/credentials?')) return response({ items: [], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(live, 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [live], next_cursor: '' });
    }));
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    const capabilities = await screen.findByRole('list');
    const entries = within(capabilities).getAllByRole('listitem');
    expect(entries).toHaveLength(2);
    expect(entries[0]).toHaveTextContent('prometheus-http-v1');
    expect(entries[1]).toHaveTextContent('prometheus-remote-write-v1');
  });

  it('rotates from the detail ETag with a stable command key and presents a fresh one-time token', async () => {
    const user = userEvent.setup(); installDialogs();
    let rotations = 0;
    let refreshReadsToDefer = 0;
    const deferredRefresh: Array<{ url: string; resolve: (value: Response) => void }> = [];
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) {
        rotations++;
        if (rotations === 1) refreshReadsToDefer = 2;
        return rotations === 1
          ? response({ agent: agent(), credential: { ...credential(), id: '9f6cf087-266e-44f5-9f93-74d8a048d987' }, previous_credential: credential(), replayed: false, secret_state: 'issued', token: 'replacement-secret' }, 200)
          : response({ agent: agent(), credential: { ...credential(), id: '9f6cf087-266e-44f5-9f93-74d8a048d987' }, previous_credential: credential(), replayed: true, secret_state: 'secret_unavailable' }, 200);
      }
      if (refreshReadsToDefer > 0 && init?.method === 'GET' && (url.includes('/credentials?') || url.endsWith(`/agents/${agentID}`))) {
        refreshReadsToDefer--;
        return new Promise<Response>((resolve) => deferredRefresh.push({ url, resolve }));
      }
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    vi.spyOn(crypto, 'randomUUID').mockReturnValueOnce('1a86bd22-c858-47c2-90d3-5d508ed1461d').mockReturnValueOnce('2a86bd22-c858-47c2-90d3-5d508ed1461d');
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    expect(fetcher).not.toHaveBeenCalledWith(expect.stringContaining('/credentials/rotate'), expect.anything());

    await user.click(within(screen.getByRole('dialog', { name: 'Rotate credential' })).getByRole('button', { name: 'Rotate credential' }));
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
    await waitFor(() => expect(deferredRefresh).toHaveLength(2));
    expect(screen.getByText('Refreshing agent records')).toHaveClass('sr-only');
    expect(screen.getByRole('heading', { name: 'Credential history' })).toBeVisible();
    expect(screen.getByLabelText('Agent token', { selector: 'output' })).toHaveTextContent('replacement-secret');
    const [url, request] = fetcher.mock.calls.find(([, init]) => init?.method === 'POST')!;
    expect(String(url)).toBe(`/api/v1/agents/${agentID}/credentials/rotate`);
    expect(request?.headers).toMatchObject({ 'If-Match': `"agent:${agentID}:3"`, 'Idempotency-Key': '1a86bd22-c858-47c2-90d3-5d508ed1461d' });
    expect(JSON.parse(String(request?.body))).toEqual({ lifetime_seconds: 7776000, overlap_seconds: 600 });
    expect(screen.getByLabelText('Agent token', { selector: 'output' })).toHaveTextContent('replacement-secret');
    await act(async () => { deferredRefresh.splice(0).forEach(({ url, resolve }) => resolve(url.includes('/credentials?') ? response({ items: [credential()], next_cursor: '' }) : response(agent(), 200, { ETag: `"agent:${agentID}:3"` }))); await new Promise((resolve) => setTimeout(resolve, 0)); });
    await user.click(within(screen.getByRole('dialog', { name: 'Agent token' })).getByRole('button', { name: 'Close token' }));
    expect(screen.queryByText('replacement-secret')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Rotate credential' }));
    const rotateDialog = screen.getByRole('dialog', { name: 'Rotate credential' });
    await user.selectOptions(within(rotateDialog).getByLabelText('Keep the previous credential valid for'), '0');

    await user.click(within(rotateDialog).getByRole('button', { name: 'Rotate credential' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(2));
    const retryRequest = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')[1]?.[1];
    expect(JSON.parse(String(retryRequest?.body))).toEqual({ lifetime_seconds: 7776000, overlap_seconds: 0 });
    expect(retryRequest?.headers).toMatchObject({ 'Idempotency-Key': '2a86bd22-c858-47c2-90d3-5d508ed1461d' });
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/one-time token is no longer available/i));
    expect(screen.queryByLabelText('Agent token', { selector: 'output' })).not.toBeInTheDocument();
  });

  it('keeps a fresh rotation token available when the authoritative refresh fails', async () => {
    const user = userEvent.setup(); installDialogs();
    let issued = false;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) {
        issued = true;
        return response({ agent: agent(), credential: credential(), previous_credential: null, replayed: false, secret_state: 'issued', token: 'fresh-token-survives-refresh' }, 200);
      }
      if (url.includes('/credentials?') && issued) return response({ type: 'about:blank', title: 'Unavailable', status: 503, code: 'management_unavailable', message: 'untrusted', request_id: 'request-6', field_errors: [] }, 503);
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    const dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    const tokenDialog = await screen.findByRole('dialog', { name: 'Agent token' });
    expect(await screen.findByRole('alert')).toHaveTextContent(/showing stale results/i);
    expect(within(tokenDialog).getByLabelText('Agent token', { selector: 'output' })).toHaveTextContent('fresh-token-survives-refresh');
    await user.click(within(tokenDialog).getByRole('button', { name: 'Copy token' }));
    expect(writeText).toHaveBeenCalledWith('fresh-token-survives-refresh');
    await user.click(within(tokenDialog).getByRole('button', { name: 'Close token' }));
    expect(screen.queryByText('fresh-token-survives-refresh')).not.toBeInTheDocument();
  });

  it('requires explicit confirmation before revoking the selected credential', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith(`/credentials/${credentialID}/revoke`)) return response({ agent: agent(), credential: { ...credential(), revoked_at: '2026-10-26T10:00:00Z' }, changed: true, revoked_credential_count: '1', replayed: false });
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Revoke credential' }));
    expect(screen.getByRole('dialog', { name: 'Revoke credential' })).toHaveTextContent(credentialID);
    expect(within(screen.getByRole('dialog', { name: 'Revoke credential' })).getByRole('button', { name: 'Revoke credential' })).toBeDisabled();
    expect(fetcher).not.toHaveBeenCalledWith(expect.stringContaining('/revoke'), expect.anything());
    fireEvent.submit(within(screen.getByRole('dialog', { name: 'Revoke credential' })).getByRole('button', { name: 'Revoke credential' }).closest('form')!);
    expect(fetcher).not.toHaveBeenCalledWith(expect.stringContaining('/revoke'), expect.anything());
    await user.click(within(screen.getByRole('dialog', { name: 'Revoke credential' })).getByRole('checkbox', { name: /understand/i }));

    await user.click(within(screen.getByRole('dialog', { name: 'Revoke credential' })).getByRole('button', { name: 'Revoke credential' }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith(`/api/v1/agents/${agentID}/credentials/${credentialID}/revoke`, expect.objectContaining({ method: 'POST' })));
  });

  it('requires explicit confirmation before disabling an agent and refreshes authoritative records', async () => {
    const user = userEvent.setup(); installDialogs();
    const disabledAgent = { ...agent(), enabled: false, version: '4' };
    let disabled = false;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/disable')) { disabled = true; return response({ agent: disabledAgent, credential: null, changed: true, revoked_credential_count: '1', replayed: false }); }
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(disabled ? disabledAgent : agent(), 200, { ETag: `"agent:${agentID}:${disabled ? '4' : '3'}"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Disable agent' }));
    expect(screen.getByRole('dialog', { name: 'Disable agent' })).toHaveTextContent(agentID);
    expect(fetcher).not.toHaveBeenCalledWith(expect.stringContaining('/disable'), expect.anything());
    await user.click(within(screen.getByRole('dialog', { name: 'Disable agent' })).getByRole('checkbox', { name: /understand/i }));

    await user.click(within(screen.getByRole('dialog', { name: 'Disable agent' })).getByRole('button', { name: 'Disable agent' }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith(`/api/v1/agents/${agentID}/disable`, expect.objectContaining({ method: 'POST' })));
    await waitFor(() => expect(screen.getByText('Disabled')).toBeVisible());
    expect(screen.queryByRole('button', { name: 'Disable agent' })).not.toBeInTheDocument();
  });

  it('disables new lifecycle actions when selected detail refresh is unavailable', async () => {
    const user = userEvent.setup(); installDialogs();
    let failDetail = false;
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return failDetail
        ? response({ type: 'about:blank', title: 'Unavailable', status: 503, code: 'management_unavailable', message: 'untrusted', request_id: 'request-7', field_errors: [] }, 503)
        : response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    failDetail = true;
    await user.click(screen.getByRole('button', { name: 'Refresh agent' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/showing stale results/i);
    expect(screen.getByRole('button', { name: 'Rotate credential' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Revoke credential' })).toBeDisabled();
    expect(fetcher).not.toHaveBeenCalledWith(expect.stringContaining('/credentials/rotate'), expect.anything());
    expect(fetcher).not.toHaveBeenCalledWith(expect.stringContaining('/revoke'), expect.anything());
  });

  it('ignores a late fresh rotation after abandonment and selection change', async () => {
    const user = userEvent.setup(); installDialogs();
    let resolveRotation!: (value: Response) => void;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) return new Promise<Response>((resolve) => { resolveRotation = resolve; });
      if (url.includes('/credentials?')) return response({ items: url.includes(agentIDB) ? [credentialB()] : [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentIDB}`)) return response(agentB(), 200, { ETag: `"agent:${agentIDB}:3"` });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent(), agentB()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    const dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1));
    fireEvent.submit(within(dialog).getByRole('button', { name: 'Rotate credential' }).closest('form')!);
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'Register agent' })).toBeDisabled();
    await user.click(await screen.findByRole('button', { name: 'Abandon command' }));
    await user.click(screen.getByRole('button', { name: 'Back to agents' }));
    await user.click(await screen.findByRole('button', { name: 'east-1' }));
    expect(await screen.findByText('East collector', { selector: 'dd' })).toBeVisible();
    await act(async () => { resolveRotation(response({ agent: agent(), credential: credential(), previous_credential: null, replayed: false, secret_state: 'issued', token: 'late-secret' }, 200)); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(screen.queryByText('late-secret')).not.toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Agent token' })).not.toBeInTheDocument();
  });

  it('discards a deferred rotation after same-agent credential cursor navigation and retains its unknown intent', async () => {
    const user = userEvent.setup(); installDialogs();
    let resolveRotation!: (value: Response) => void;
    const fetcher = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) return new Promise<Response>((resolve) => { resolveRotation = resolve; });
      if (url.includes('/credentials?')) return Promise.resolve(response({ items: [credential()], next_cursor: '' }));
      if (url.endsWith(`/agents/${agentID}`)) return Promise.resolve(response(agent(), 200, { ETag: `"agent:${agentID}:3"` }));
      return Promise.resolve(response({ items: [agent()], next_cursor: '' }));
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(screen.getByRole('button', { name: 'Rotate credential' }));
    const dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    await waitFor(() => expect(resolveRotation).toBeTypeOf('function'));
    await act(async () => { history.pushState({}, '', `/?agent=${agentID}&credentials_cursor=page-two`); dispatchEvent(new PopStateEvent('popstate')); });
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith(`/api/v1/agents/${agentID}/credentials?limit=50&cursor=page-two`, expect.objectContaining({ method: 'GET' })));
    await act(async () => { await Promise.resolve(); });
    await screen.findByRole('heading', { name: 'Credential action outcome unknown' });
    await act(async () => { resolveRotation(response({ agent: agent(), credential: { ...credential(), id: '9f6cf087-266e-44f5-9f93-74d8a048d987' }, previous_credential: credential(), replayed: false, secret_state: 'issued', token: 'late-cursor-token' }, 200)); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(screen.queryByRole('dialog', { name: 'Agent token' })).not.toBeInTheDocument();
    expect(screen.queryByText('late-cursor-token')).not.toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Credential action outcome unknown' })).toBeVisible();
    expect(screen.getByRole('button', { name: /Retry rotate credential/i })).toBeVisible();
  });

  it('does not restore a late rotation token after uncertain logout restores the same user', async () => {
    const user = userEvent.setup(); installDialogs();
    let resolveRotation!: (value: Response) => void;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) return new Promise<Response>((resolve) => { resolveRotation = resolve; });
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    const auth = makeAuth();
    auth.logout = vi.fn().mockRejectedValue(new Error('logout response unavailable'));
    render(<AuthPage client={auth} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    const dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1));
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/sign-out could not be confirmed/i);
    expect(screen.getByRole('heading', { name: 'Agent details' })).toBeVisible();
    await act(async () => { resolveRotation(response({ agent: agent(), credential: credential(), previous_credential: null, replayed: false, secret_state: 'issued', token: 'late-after-logout' }, 200)); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(screen.queryByText('late-after-logout')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Agent token', { selector: 'output' })).not.toBeInTheDocument();
  });

  it('blocks lifecycle actions while registration is already in flight', async () => {
    const user = userEvent.setup(); installDialogs();
    let resolveCreate!: (value: Response) => void;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/api/v1/agents')) return new Promise<Response>((resolve) => { resolveCreate = resolve; });
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    const registration = screen.getByRole('dialog', { name: 'Register agent' });
    await user.type(within(registration).getByLabelText('Agent name (identifier)'), 'new-agent');
    await user.type(within(registration).getByLabelText('Display name'), 'New agent');
    await user.click(within(registration).getByRole('button', { name: 'Register agent' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1));
    await user.click(within(registration).getByRole('button', { name: 'Close dialog' }));
    await user.click(screen.getByRole('button', { name: 'Keep request' }));
    await user.click(screen.getByRole('button', { name: 'Back to agents' }));
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    expect(await screen.findByRole('button', { name: 'Rotate credential' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Revoke credential' })).toBeDisabled();
    await act(async () => { resolveCreate(response({ agent: { ...agent(), id: '9d1b1d80-9863-4b4c-bf43-0a038643cad9' }, credential: { ...credential(), agent_id: '9d1b1d80-9863-4b4c-bf43-0a038643cad9' }, replayed: false, secret_state: 'issued', token: 'late-create-token' }, 201)); await new Promise((resolve) => setTimeout(resolve, 0)); });
    expect(screen.queryByText('late-create-token')).not.toBeInTheDocument();
  });

  it('keeps an uncertain rotation across a rejected retry, failed session recheck, and same-user sign-in', async () => {
    const user = userEvent.setup(); installDialogs();
    let rotations = 0;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) {
        rotations++;
        if (rotations === 1) throw new Error('simulated lost response');
        if (rotations === 2) return response({ type: 'about:blank', title: 'Unauthorized', status: 401, code: 'unauthenticated', message: 'untrusted', request_id: 'request-1', field_errors: [] }, 401);
        return response({ agent: agent(), credential: { ...credential(), id: '9f6cf087-266e-44f5-9f93-74d8a048d987' }, previous_credential: credential(), replayed: true, secret_state: 'secret_unavailable' }, 200);
      }
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    const auth = makeAuth();
    auth.context = vi.fn().mockResolvedValueOnce(admin).mockRejectedValueOnce(new Error('session unavailable')).mockResolvedValueOnce({ csrf_token: 'csrf-anonymous', session: null });
    render(<AuthPage client={auth} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    const rotateDialog = screen.getByRole('dialog', { name: 'Rotate credential' });
    await user.selectOptions(within(rotateDialog).getByLabelText('New credential expires after'), 'custom');
    await user.type(within(rotateDialog).getByLabelText('Custom amount'), '12345');
    await user.selectOptions(within(rotateDialog).getByLabelText('Keep the previous credential valid for'), 'custom');
    await user.type(within(rotateDialog).getAllByLabelText('Custom amount')[1], '17');

    await user.click(within(rotateDialog).getByRole('button', { name: 'Rotate credential' }));
    expect(await screen.findByRole('heading', { name: 'Credential action outcome unknown' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: /Retry rotate credential/i }));
    const rejectedRetryDialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(rejectedRetryDialog).getByRole('button', { name: 'Rotate credential' }));
    expect(await screen.findByRole('heading', { name: 'Unable to check your session' })).toBeVisible();
    expect(await screen.findByText(/original target, command key and resource version remain available/i)).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Check session' }));
    await screen.findByRole('heading', { name: 'Sign in' });
    auth.login = vi.fn().mockResolvedValue(admin);
    await user.type(screen.getByLabelText('Login'), 'owner');
    await user.type(screen.getByLabelText('Password'), 'login proof');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await user.click(await screen.findByRole('button', { name: /Retry rotate credential/i }));
    const retryDialog = screen.getByRole('dialog', { name: 'Rotate credential' });
    expect(within(retryDialog).getByText('12345 seconds', { selector: 'dd' })).toBeVisible();
    expect(within(retryDialog).getByText('17 seconds', { selector: 'dd' })).toBeVisible();
    expect(within(retryDialog).queryByLabelText('New credential expires after')).not.toBeInTheDocument();
    expect(within(retryDialog).queryByLabelText('Keep the previous credential valid for')).not.toBeInTheDocument();
    expect(within(retryDialog).getByText(/keeps the original target, resource version, and rotation options/i)).toBeVisible();

    await user.click(within(retryDialog).getByRole('button', { name: 'Rotate credential' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(3));
    const requests = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    const firstHeaders = requests[0]?.[1]?.headers as Record<string, string>;
    const secondHeaders = requests[1]?.[1]?.headers as Record<string, string>;
    const thirdHeaders = requests[2]?.[1]?.headers as Record<string, string>;
    expect(firstHeaders).toMatchObject({ 'If-Match': `"agent:${agentID}:3"` });
    expect(secondHeaders).toMatchObject({ 'If-Match': `"agent:${agentID}:3"`, 'Idempotency-Key': firstHeaders['Idempotency-Key'] });
    expect(thirdHeaders).toMatchObject({ 'If-Match': `"agent:${agentID}:3"`, 'Idempotency-Key': firstHeaders['Idempotency-Key'] });
    expect(JSON.parse(String(requests[0]?.[1]?.body))).toEqual({ lifetime_seconds: 12345, overlap_seconds: 17 });
    expect(JSON.parse(String(requests[1]?.[1]?.body))).toEqual({ lifetime_seconds: 12345, overlap_seconds: 17 });
    expect(JSON.parse(String(requests[2]?.[1]?.body))).toEqual({ lifetime_seconds: 12345, overlap_seconds: 17 });
    expect(await screen.findByRole('status')).toHaveTextContent(/one-time token is no longer available/i);
    expect(screen.queryByLabelText('Agent token', { selector: 'output' })).not.toBeInTheDocument();
  });

  it('keeps explicit recovery after a 403 clears protected data and the detail refresh fails', async () => {
    const user = userEvent.setup(); installDialogs();
    let revocations = 0;
    let protectedReadFails = false;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith(`/credentials/${credentialID}/revoke`)) {
        revocations++;
        if (revocations === 1) throw new Error('simulated response loss');
        return response({ type: 'about:blank', title: 'Forbidden', status: 403, code: 'forbidden', message: 'untrusted', request_id: 'request-2', field_errors: [] }, 403);
      }
      if (url.includes('/credentials?')) return protectedReadFails
        ? response({ type: 'about:blank', title: 'Unavailable', status: 503, code: 'management_unavailable', message: 'untrusted', request_id: 'request-3', field_errors: [] }, 503)
        : response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    const auth = makeAuth();
    auth.context = vi.fn().mockImplementation(async () => { if (revocations >= 2) protectedReadFails = true; return admin; });
    render(<AuthPage client={auth} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Revoke credential' }));
    const revokeDialog = screen.getByRole('dialog', { name: 'Revoke credential' });
    await user.click(within(revokeDialog).getByRole('checkbox', { name: /understand/i }));

    await user.click(within(revokeDialog).getByRole('button', { name: 'Revoke credential' }));
    expect(await screen.findByRole('heading', { name: 'Credential action outcome unknown' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: /Retry revoke credential/i }));
    const retryDialog = screen.getByRole('dialog', { name: 'Revoke credential' });
    await user.click(within(retryDialog).getByRole('checkbox', { name: /understand/i }));

    await user.click(within(retryDialog).getByRole('button', { name: 'Revoke credential' }));
    await waitFor(() => expect(screen.getAllByRole('alert').some((alert) => /protected agent records were cleared/i.test(alert.textContent ?? ''))).toBe(true));
    expect(screen.queryByRole('heading', { name: 'Agent details' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Check session' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Abandon command' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Check session' }));
    await waitFor(() => expect(screen.getAllByRole('alert').some((alert) => /Showing stale results|could not be completed/i.test(alert.textContent ?? ''))).toBe(true));
    expect(screen.getByRole('button', { name: 'Abandon command' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Abandon command' }));
    expect(screen.queryByRole('button', { name: 'Abandon command' })).not.toBeInTheDocument();
  });

  it('preserves an unknown command on conflict and requires abandonment before a new key', async () => {
    const user = userEvent.setup(); installDialogs();
    let rotations = 0;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) {
        rotations++;
        if (rotations === 1) throw new Error('simulated response loss');
        return response({ type: 'about:blank', title: 'Conflict', status: 409, code: 'conflict', message: 'untrusted', request_id: 'request-4', field_errors: [] }, 409);
      }
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    vi.spyOn(crypto, 'randomUUID').mockReturnValueOnce('3a86bd22-c858-47c2-90d3-5d508ed1461d').mockReturnValueOnce('4a86bd22-c858-47c2-90d3-5d508ed1461d');
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    let dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    await user.click(await screen.findByRole('button', { name: /Retry rotate credential/i }));
    dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    expect(await screen.findByRole('heading', { name: 'Credential action needs reconciliation' })).toBeVisible();
    expect(screen.queryByRole('button', { name: /Retry rotate credential/i })).not.toBeInTheDocument();
    const requests = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect(requests).toHaveLength(2);
    expect((requests[1]?.[1]?.headers as Record<string, string>)['Idempotency-Key']).toBe((requests[0]?.[1]?.headers as Record<string, string>)['Idempotency-Key']);
    expect((requests[1]?.[1]?.headers as Record<string, string>)['If-Match']).toBe(`"agent:${agentID}:3"`);
    await user.click(screen.getByRole('button', { name: 'Abandon command' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(3));
    const newRequest = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')[2];
    expect((newRequest?.[1]?.headers as Record<string, string>)['Idempotency-Key']).toBe('4a86bd22-c858-47c2-90d3-5d508ed1461d');
  });

  it('ignores a deferred auth recheck after the old command is abandoned and a new intent starts', async () => {
    const user = userEvent.setup(); installDialogs();
    let resolveRecheck!: (value: AuthContext) => void;
    let rotations = 0;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) {
        rotations++;
        if (rotations === 1) return response({ type: 'about:blank', title: 'Unauthorized', status: 401, code: 'unauthenticated', message: 'untrusted', request_id: 'request-8', field_errors: [] }, 401);
        return response({ agent: agent(), credential: credential(), previous_credential: null, replayed: false, secret_state: 'issued', token: 'new-intent-token' }, 200);
      }
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    vi.spyOn(crypto, 'randomUUID').mockReturnValueOnce('6a86bd22-c858-47c2-90d3-5d508ed1461d').mockReturnValueOnce('7a86bd22-c858-47c2-90d3-5d508ed1461d');
    const auth = makeAuth();
    auth.context = vi.fn().mockImplementationOnce(async () => admin).mockImplementationOnce(() => new Promise<AuthContext>((resolve) => { resolveRecheck = resolve; }));
    render(<AuthPage client={auth} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    let dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    await waitFor(() => expect(auth.context).toHaveBeenCalledTimes(2));
    await user.click(await screen.findByRole('button', { name: 'Abandon command' }));
    await act(async () => { resolveRecheck(admin); await new Promise((resolve) => setTimeout(resolve, 0)); });
    const rotate = await screen.findByRole('button', { name: 'Rotate credential' });
    await waitFor(() => expect(rotate).toBeEnabled());
    await user.click(rotate);
    dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
    expect(screen.queryByText(/current password was rejected/i)).not.toBeInTheDocument();
    const requests = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect((requests[0]?.[1]?.headers as Record<string, string>)['Idempotency-Key']).toBe('6a86bd22-c858-47c2-90d3-5d508ed1461d');
    expect((requests[1]?.[1]?.headers as Record<string, string>)['Idempotency-Key']).toBe('7a86bd22-c858-47c2-90d3-5d508ed1461d');
  });

  it('describes CSRF rejection as stale session proof and allows retry after the authority check succeeds', async () => {
    const user = userEvent.setup(); installDialogs();
    let rotations = 0;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) {
        rotations++;
        return rotations === 1
          ? response({ type: 'about:blank', title: 'Forbidden', status: 403, code: 'csrf_invalid', message: 'untrusted', request_id: 'request-9', field_errors: [] }, 403)
          : response({ agent: agent(), credential: credential(), previous_credential: null, replayed: true, secret_state: 'secret_unavailable' }, 200);
      }
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    vi.spyOn(crypto, 'randomUUID').mockReturnValue('8a86bd22-c858-47c2-90d3-5d508ed1461d');
    const auth = makeAuth(); auth.context = vi.fn().mockResolvedValueOnce(admin).mockResolvedValueOnce(admin);
    render(<AuthPage client={auth} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    let dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    expect(await screen.findByText(/session proof was stale/i)).toBeVisible();
    expect(screen.queryByText(/password proof failed/i)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /Retry rotate credential/i }));
    dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(2));
    const requests = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect((requests[1]?.[1]?.headers as Record<string, string>)['Idempotency-Key']).toBe((requests[0]?.[1]?.headers as Record<string, string>)['Idempotency-Key']);
  });

  it('replays an unknown revoke for its captured credential and ETag after refresh shows it revoked', async () => {
    const user = userEvent.setup(); installDialogs();
    let detailVersion = 3;
    let revocations = 0;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith(`/credentials/${credentialID}/revoke`)) {
        revocations++;
        if (revocations === 1) throw new Error('simulated committed-response loss');
        return response({ agent: { ...agent(), version: '4' }, credential: { ...credential(), revoked_at: '2026-10-26T10:00:00Z' }, changed: false, revoked_credential_count: '0', replayed: true }, 200);
      }
      if (url.includes('/credentials?')) return detailVersion === 3 ? response({ items: [credential()], next_cursor: '' }) : response({ items: [], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response({ ...agent(), version: String(detailVersion) }, 200, { ETag: `"agent:${agentID}:${detailVersion}"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    vi.spyOn(crypto, 'randomUUID').mockReturnValue('5a86bd22-c858-47c2-90d3-5d508ed1461d');
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Revoke credential' }));
    let dialog = screen.getByRole('dialog', { name: 'Revoke credential' });
    await user.click(within(dialog).getByRole('checkbox', { name: /understand/i }));

    await user.click(within(dialog).getByRole('button', { name: 'Revoke credential' }));
    expect(await screen.findByRole('heading', { name: 'Credential action outcome unknown' })).toBeVisible();
    detailVersion = 4;
    await user.click(screen.getByRole('button', { name: 'Refresh agent' }));
    await screen.findByText('No credentials are recorded.');
    await user.click(screen.getByRole('button', { name: /Retry revoke credential/i }));
    dialog = screen.getByRole('dialog', { name: 'Revoke credential' });
    expect(dialog).toHaveTextContent(credentialID);
    await user.click(within(dialog).getByRole('checkbox', { name: /understand/i }));

    await user.click(within(dialog).getByRole('button', { name: 'Revoke credential' }));
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(2));
    const requests = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    const firstHeaders = requests[0]?.[1]?.headers as Record<string, string>;
    const secondHeaders = requests[1]?.[1]?.headers as Record<string, string>;
    expect(String(requests[1]?.[0])).toBe(`/api/v1/agents/${agentID}/credentials/${credentialID}/revoke`);
    expect(secondHeaders).toMatchObject({ 'If-Match': `"agent:${agentID}:3"`, 'Idempotency-Key': firstHeaders['Idempotency-Key'] });
    expect(JSON.parse(String(requests[1]?.[1]?.body))).toEqual({ });
  });

  it('enforces Retry-After in the submit handler as well as the disabled button', async () => {
    const user = userEvent.setup(); installDialogs();
    let rotations = 0;
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) {
        rotations++;
        return rotations === 1
          ? response({ type: 'about:blank', title: 'Rate limited', status: 429, code: 'rate_limited', message: 'untrusted', request_id: 'request-5', field_errors: [] }, 429, { 'Retry-After': '1' })
          : response({ agent: agent(), credential: credential(), previous_credential: null, replayed: true, secret_state: 'secret_unavailable' }, 200);
      }
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(await screen.findByRole('button', { name: 'Rotate credential' }));
    let dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/wait 1 seconds/i);
    dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    const submit = within(dialog).getByRole('button', { name: 'Rotate credential' });
    expect(submit).toBeDisabled();
    fireEvent.submit(submit.closest('form')!);
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 1100)); });
    expect(submit).toBeEnabled();
    await user.click(submit);
    await waitFor(() => expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(2));
  });

  it('retains a lifecycle Retry-After deadline when navigating away and remounting its controls', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (init?.method === 'POST' && url.endsWith('/credentials/rotate')) return response({ type: 'about:blank', title: 'Rate limited', status: 429, code: 'rate_limited', message: 'untrusted', request_id: 'request-10', field_errors: [] }, 429, { 'Retry-After': '30' });
      if (url.includes('/credentials?')) return response({ items: [credential()], next_cursor: '' });
      if (url.endsWith(`/agents/${agentID}`)) return response(agent(), 200, { ETag: `"agent:${agentID}:3"` });
      return response({ items: [agent()], next_cursor: '' });
    });
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(screen.getByRole('button', { name: 'Rotate credential' }));
    let dialog = screen.getByRole('dialog', { name: 'Rotate credential' });

    await user.click(within(dialog).getByRole('button', { name: 'Rotate credential' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/wait 30 seconds/i);
    await user.click(screen.getByRole('button', { name: 'Back to agents' }));
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    const retry = await screen.findByRole('button', { name: /Retry rotate credential/i });
    expect(retry).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Refresh agent' }));
    expect(retry).toBeDisabled();
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
  });

  it('does not replay a canceled revoke dialog after navigating away and returning', async () => {
    const user = userEvent.setup(); installDialogs();
    vi.stubGlobal('fetch', installFetch());
    render(<AuthPage client={makeAuth()} />);
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    await user.click(screen.getByRole('button', { name: 'Revoke credential' }));
    expect(await screen.findByRole('dialog', { name: 'Revoke credential' })).toBeVisible();
    await user.click(within(screen.getByRole('dialog', { name: 'Revoke credential' })).getByRole('button', { name: 'Close dialog' }));
    expect(screen.queryByRole('dialog', { name: 'Revoke credential' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Back to agents' }));
    await user.click(await screen.findByRole('button', { name: 'west-1' }));
    expect(await screen.findByText('West collector', { selector: 'dd' })).toBeVisible();
    expect(screen.queryByRole('dialog', { name: 'Revoke credential' })).not.toBeInTheDocument();
    expect(vi.mocked(fetch)).not.toHaveBeenCalledWith(expect.stringContaining('/revoke'), expect.anything());
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
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'new-east');
    await user.type(screen.getByLabelText('Display name'), 'East collector');
    await user.selectOptions(screen.getByLabelText('New credential expires after'), '2592000');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
    expect(location.search).toContain('agents_cursor=page-two');
    expect(screen.getByLabelText('Agent token', { selector: 'output' })).toHaveTextContent('one-time-secret');
    const request = fetcher.mock.calls.find(([, init]) => init?.method === 'POST');
    expect(request?.[1]?.headers).toMatchObject({ 'Idempotency-Key': expect.any(String), 'X-CSRF-Token': 'csrf-admin' });
    expect(JSON.parse(String(request?.[1]?.body))).toEqual({ name: 'new-east', display_name: 'East collector', lifetime_seconds: 2592000 });
    expect(screen.queryByLabelText('Current password')).not.toBeInTheDocument();
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
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'new-agent'); await user.type(screen.getByLabelText('Display name'), 'New agent');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
    expect(location.search).toContain(`agent=${agentIDB}`);
    await user.click(screen.getByRole('button', { name: 'Close token' }));
    await waitFor(() => expect(location.search).toContain(`agent=${agentID}`));
    expect(await screen.findByText('west-1')).toBeVisible();
    expect(await screen.findByRole('heading', { name: 'Credential history' })).toBeVisible();
  });

  it('retains an uncertain intent and reuses its original key after deliberate retry', async () => {
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
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'new-east');
    await user.type(screen.getByLabelText('Display name'), 'East collector');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Registration not confirmed' })).toBeVisible();
    expect(screen.getByText(/No response was received from Almanac/)).toBeVisible();
    expect(screen.getByText(/refresh agent records before deciding/i)).toBeVisible();
    expect(screen.queryByLabelText('Current password')).not.toBeInTheDocument();
    await user.click(screen.getByRole('link', { name: 'Agents' }));
    expect(screen.getByRole('heading', { name: 'Registration not confirmed' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Retry registration' }));
    await user.click(screen.getAllByRole('button', { name: 'Retry registration' })[1]!);
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/token is no longer available/i));
    const posts = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect(posts).toHaveLength(2);
    const firstKey = new Headers(posts[0]?.[1]?.headers).get('Idempotency-Key');
    const retryKey = new Headers(posts[1]?.[1]?.headers).get('Idempotency-Key');
    expect(firstKey).toBeTruthy(); expect(retryKey).toBe(firstKey);
    expect(JSON.parse(String(posts[1]?.[1]?.body))).toEqual({ name: 'new-east', display_name: 'East collector', lifetime_seconds: 7776000 });
    expect(screen.queryByLabelText('Agent token', { selector: 'output' })).not.toBeInTheDocument();
    expect(location.search).toContain(`agent=${agentID}`);
    expect(await screen.findByRole('heading', { name: 'Credential history' })).toBeVisible();
  });

  it('keeps an unknown registration when its retry receives a definite 422', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({ items: [], next_cursor: '' }))
      .mockRejectedValueOnce(new Error('connection lost'))
      .mockResolvedValueOnce(response({ code: 'invalid_agent_input', message: 'safe', request_id: 'retry-422', field_errors: [] }, 422));
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Display name'), 'Lost agent');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Registration not confirmed' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Retry registration' }));
    await user.click(screen.getAllByRole('button', { name: 'Retry registration' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Registration not confirmed' })).toBeVisible();
    expect(screen.getByText(/earlier result remains unconfirmed/i)).toBeVisible();
    expect(screen.getByRole('alert')).toHaveTextContent(/original attempt remains unconfirmed/i);
    expect(screen.getByRole('button', { name: 'Abandon request' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Retry registration' })).toBeVisible();
    const posts = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect(posts).toHaveLength(2);
    const keys = posts.map(([, init]) => new Headers(init?.headers).get('Idempotency-Key'));
    expect(keys[0]).toBeTruthy(); expect(keys[1]).toBe(keys[0]);
    expect(JSON.parse(String(posts[1]?.[1]?.body))).toEqual({ name: 'lost-agent', display_name: 'Lost agent', lifetime_seconds: 7776000 });
  });

  it('rechecks session after a 401 without treating the proof failure itself as logout', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn().mockResolvedValueOnce(response({ items: [], next_cursor: '' })).mockResolvedValueOnce(response({ code: 'unauthenticated', message: 'unsafe', request_id: 'r', field_errors: [] }, 401)).mockResolvedValueOnce(response({ items: [], next_cursor: '' }));
    vi.stubGlobal('fetch', fetcher);
    const auth = makeAuth();
    render(<AuthPage client={auth} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'new-east'); await user.type(screen.getByLabelText('Display name'), 'East collector');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Agents' })).toBeVisible();
    expect(auth.context).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('heading', { name: 'Session needs checking' })).toBeVisible();
  });

  it('distinguishes an unavailable session recheck and requires a later successful check', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn().mockResolvedValueOnce(response({ items: [], next_cursor: '' })).mockResolvedValueOnce(response({ code: 'unauthenticated', message: 'unsafe', request_id: 'r', field_errors: [] }, 401));
    vi.stubGlobal('fetch', fetcher);
    const auth = makeAuth(); auth.context = vi.fn().mockResolvedValueOnce(admin).mockRejectedValueOnce(new Error('context unavailable')).mockResolvedValueOnce(admin);
    render(<AuthPage client={auth} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'safe-name'); await user.type(screen.getByLabelText('Display name'), 'Safe display');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Unable to check your session' })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Session status unavailable' })).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'Current password needs refreshing' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Check session' }));
    expect(await screen.findByRole('heading', { name: 'Agents' })).toBeVisible();
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
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'safe-name'); await user.type(screen.getByLabelText('Display name'), 'Safe display');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Registration not confirmed' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Retry registration' }));
    await user.click(screen.getAllByRole('button', { name: 'Retry registration' })[1]!);
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    await user.type(screen.getByLabelText('Login'), 'owner'); await user.type(screen.getByLabelText('Password'), 'login proof');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('heading', { name: 'Registration not confirmed' })).toBeVisible();
    expect(screen.getByText(/earlier result remains unconfirmed/i)).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Retry registration' }));
    expect(screen.queryByLabelText('Current password')).not.toBeInTheDocument();
    await user.click(screen.getAllByRole('button', { name: 'Retry registration' })[1]!);
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
    const posts = fetcher.mock.calls.filter(([, init]) => init?.method === 'POST');
    expect(posts).toHaveLength(3);
    const keys = posts.map(([, init]) => new Headers(init?.headers).get('Idempotency-Key'));
    expect(keys[0]).toBeTruthy(); expect(keys[1]).toBe(keys[0]); expect(keys[2]).toBe(keys[0]);
    expect(JSON.parse(String(posts[2]?.[1]?.body))).toMatchObject({ name: 'safe-name', display_name: 'Safe display' });
  });

  it('honors validated Retry-After by blocking registration and announcing the wait', async () => {
    const user = userEvent.setup(); installDialogs();
    const fetcher = vi.fn().mockResolvedValueOnce(response({ items: [], next_cursor: '' })).mockResolvedValueOnce(response({ code: 'rate_limited', message: 'unsafe', request_id: 'r', field_errors: [] }, 429, { 'Retry-After': '1' }));
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'safe-name'); await user.type(screen.getByLabelText('Display name'), 'Safe display');
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
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'safe-name'); await user.type(screen.getByLabelText('Display name'), 'Safe display');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    await user.click(screen.getByRole('button', { name: 'Close dialog' }));
    await user.click(screen.getAllByRole('button', { name: 'Abandon request' }).at(-1)!);
    await act(async () => { finish(response({ agent: agent(), credential: credential(), replayed: false, secret_state: 'issued', token: 'one-time-secret' }, 201)); });
    expect(screen.queryByLabelText('Agent token', { selector: 'output' })).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Registration not confirmed' })).not.toBeInTheDocument();
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
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'old-user-name'); await user.type(screen.getByLabelText('Display name'), 'Old user');
    await user.click(screen.getAllByRole('button', { name: 'Register agent' })[1]!);
    await user.click(screen.getByRole('button', { name: 'Close dialog' }));
    await user.click(screen.getByRole('button', { name: 'Keep request' }));
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/sign-out could not be confirmed/i);
    expect(screen.getByRole('heading', { name: 'Agents' })).toBeVisible();
    await act(async () => { finish(response({ agent: agent(), credential: credential(), replayed: false, secret_state: 'issued', token: 'one-time-secret' }, 201)); });
    expect(screen.queryByLabelText('Agent token', { selector: 'output' })).not.toBeInTheDocument();
    auth.logout = vi.fn().mockResolvedValue(undefined); auth.context = vi.fn().mockResolvedValueOnce({ csrf_token: 'csrf-anon', session: null }); auth.login = vi.fn().mockResolvedValue(newAdmin);
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    await user.type(screen.getByLabelText('Login'), 'second'); await user.type(screen.getByLabelText('Password'), 'password'); await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await screen.findByRole('button', { name: 'Register agent' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    expect(screen.getByLabelText('Agent name (identifier)')).toHaveValue('');
    expect(screen.getByLabelText('Display name')).toHaveValue('');
  });

  it('clears the safe draft on an explicit logout before same-user sign-in', async () => {
    const user = userEvent.setup(); installDialogs();
    const auth = makeAuth(); auth.context = vi.fn().mockResolvedValueOnce(admin).mockResolvedValueOnce({ csrf_token: 'csrf-anon', session: null });
    render(<AuthPage client={auth} />);
    await screen.findByRole('button', { name: 'Register agent' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'private-draft'); await user.type(screen.getByLabelText('Display name'), 'Private draft');
    await user.click(screen.getByRole('button', { name: 'Close dialog' }));
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    await user.type(screen.getByLabelText('Login'), 'owner'); await user.type(screen.getByLabelText('Password'), 'login password'); await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await screen.findByRole('button', { name: 'Register agent' });
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    expect(screen.getByLabelText('Agent name (identifier)')).toHaveValue('');
    expect(screen.getByLabelText('Display name')).toHaveValue('');
    expect(screen.queryByLabelText('Current password')).not.toBeInTheDocument();
  });

  it('blocks duplicate submits synchronously', async () => {
    const user = userEvent.setup(); installDialogs(); let finish!: (value: Response) => void;
    const fetcher = vi.fn().mockImplementationOnce(async () => response({ items: [], next_cursor: '' })).mockImplementationOnce(() => new Promise<Response>((resolve) => { finish = resolve; }));
    vi.stubGlobal('fetch', fetcher);
    render(<AuthPage client={makeAuth()} />);
    await screen.findByText('No agents have been registered.');
    await user.click(screen.getByRole('button', { name: 'Register agent' }));
    await user.type(screen.getByLabelText('Agent name (identifier)'), 'safe-name'); await user.type(screen.getByLabelText('Display name'), 'Safe display');
    const form = screen.getByLabelText('Agent name (identifier)').closest('form')!;
    fireEvent.submit(form); fireEvent.submit(form);
    expect(fetcher.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
    finish(response({ agent: agent(), credential: credential(), replayed: false, secret_state: 'issued', token: 'one-time-secret' }, 201));
    expect(await screen.findByRole('dialog', { name: 'Agent token' })).toBeVisible();
  });
});
