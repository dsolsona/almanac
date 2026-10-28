import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { AuthFailure, type AuthContext, type createAuthClient } from '../src/auth/client.ts';
import { AuthPage } from '../src/auth/AuthPage.tsx';

type Client = ReturnType<typeof createAuthClient>;
const anonymous: AuthContext = { csrf_token: 'csrf-anonymous', session: null };
const restricted: AuthContext = { csrf_token: 'csrf-restricted', session: { user_id: 'u1', login: 'owner', role: 'administrator', restricted: true } };
const signedIn: AuthContext = { csrf_token: 'csrf-normal', session: { user_id: 'u1', login: 'owner', role: 'administrator', restricted: false } };

function clientFor(context: AuthContext = anonymous): Client {
	vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => new Response(JSON.stringify({ items: [], next_cursor: '' }), { status: 200, headers: { 'Content-Type': 'application/json' } })));
  return {
    context: vi.fn().mockResolvedValue(context),
    login: vi.fn().mockResolvedValue(signedIn),
    changePassword: vi.fn().mockResolvedValue(signedIn),
    logout: vi.fn().mockResolvedValue(undefined),
  };
}

afterEach(() => {
  cleanup();
  history.replaceState({}, '', '/');
  document.documentElement.classList.remove('dark');
  document.documentElement.style.colorScheme = '';
  localStorage.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('authentication page', () => {
  // This suite tests shell behavior; the real Compose journey covers cold route loading.
  beforeAll(async () => { await Promise.all([import('../src/datasources/DatasourcesPage.tsx'), import('../src/slis/SlisPage.tsx')]); });
  it('returns from datasource creation to the actual SLI form and updates shell navigation', async () => {
    const user = userEvent.setup(); history.replaceState({}, '', '/datasources/new?return=slis');
    const push = vi.spyOn(history, 'pushState');
    const client = clientFor(signedIn);
    const sourceID = '8ed93d7e-51e3-4a3e-9222-3e8a612de20a';
    const agentID = 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c';
    const source = { id: sourceID, name: 'metrics-prod', display_name: 'Production metrics', version: '4', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test', tls: { insecure_skip_verify: false, ca_pem: null }, authentication: { mode: 'none', username: null, password_configured: false, token_configured: false }, polling_interval_seconds: 60, settling_delay_seconds: 60, agent_id: agentID, created_at: '2026-10-20T12:00:00Z', updated_at: '2026-10-21T12:00:00Z' };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const body = url.startsWith('/api/v1/agents') ? { items: [{ id: agentID, name: 'west', display_name: 'West', enabled: true, runtime: { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test', 'query_preview'] }] } }], next_cursor: '' }
        : url.startsWith('/api/v1/providers') ? { items: [] }
          : init?.method === 'POST' && url === '/api/v1/datasources' ? source : { items: [source], next_cursor: '' };
      return new Response(JSON.stringify(body), { status: init?.method === 'POST' ? 201 : 200, headers: { 'Content-Type': 'application/json', ETag: `"datasource:${sourceID}:4"` } });
    }));
    render(<AuthPage client={client} />);
    await user.type(await screen.findByLabelText('Display name'), 'Production metrics');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'https://metrics.example.test');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    expect(await screen.findByRole('heading', { name: 'Create SLI' })).toBeVisible();
    expect(await screen.findByLabelText('Datasource')).toHaveValue(sourceID);
    expect(screen.getByRole('link', { name: 'SLIs' })).toHaveAttribute('aria-current', 'page');
    expect(screen.queryByRole('heading', { name: 'Add datasource' })).not.toBeInTheDocument();
    expect(push).toHaveBeenCalledTimes(1);
  });

  it('opens the SLI screen when leaving datasource setup via Back to SLI', async () => {
    const user = userEvent.setup(); history.replaceState({}, '', '/datasources/new?return=slis');
    render(<AuthPage client={clientFor(signedIn)} />);
    await user.click(await screen.findByRole('button', { name: 'Back to SLI' }));
    expect(await screen.findByRole('heading', { name: 'Create SLI' })).toBeVisible();
    expect(screen.getByRole('link', { name: 'SLIs' })).toHaveAttribute('aria-current', 'page');
  });

  it('opens the agent screen when the datasource form asks the user to view agents', async () => {
    const user = userEvent.setup(); history.replaceState({}, '', '/datasources/new?return=slis');
    render(<AuthPage client={clientFor(signedIn)} />);
    await user.click(await screen.findByRole('button', { name: 'View agents' }));
    expect(await screen.findByRole('heading', { name: 'Agents' })).toBeVisible();
    expect(screen.getByRole('link', { name: 'Agents' })).toHaveAttribute('aria-current', 'page');
  });

  it('shows loading, then the anonymous sign-in form with accessible shell', async () => {
    let resolve!: (value: AuthContext) => void;
    const client = clientFor();
    client.context = vi.fn(() => new Promise<AuthContext>((done) => { resolve = done; }));
    render(<AuthPage client={client} />);
    expect(screen.getByText('Checking your session')).toHaveClass('sr-only');
    expect(screen.getByRole('button', { name: /switch to dark theme/i })).toBeVisible();
    resolve(anonymous);
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    expect(screen.getByRole('main')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Skip to content' })).toHaveAttribute('href', '#main');
    expect(screen.getByRole('banner').querySelector('.wordmark')?.tagName).toBe('P');
    expect(screen.getByRole('banner').querySelector('.wordmark')).not.toHaveAttribute('href');
    expect(screen.getByRole('textbox', { name: 'Login' })).toBeVisible();
    expect(screen.getByLabelText('Password')).toHaveAttribute('autocomplete', 'current-password');
  });

  it('keeps an unavailable initial context explicit and retries only on request', async () => {
    const user = userEvent.setup();
    const client = clientFor();
    client.context = vi.fn().mockRejectedValueOnce(new Error('untrusted detail')).mockResolvedValueOnce(anonymous);
    render(<AuthPage client={client} />);
    expect(await screen.findByRole('heading', { name: 'Unable to check your session' })).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'Sign in' })).not.toBeInTheDocument();
    expect(screen.queryByText('untrusted detail')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Check again' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    expect(client.context).toHaveBeenCalledTimes(2);
  });

  it('reloads into restricted setup without retaining a prior login credential', async () => {
    render(<AuthPage client={clientFor(restricted)} />);
    expect(await screen.findByRole('heading', { name: 'Set your permanent password' })).toBeVisible();
    expect(screen.getByLabelText('Current password')).toHaveAttribute('autocomplete', 'current-password');
    expect(screen.getByLabelText('New password')).toHaveAttribute('autocomplete', 'new-password');
    expect(screen.getByLabelText('Confirm new password')).toHaveAttribute('autocomplete', 'new-password');
    expect(screen.getByText('Choose a different password with 15–128 characters. Spaces are welcome.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeVisible();
  });

  it('validates UTF-8 byte and Unicode code-point limits without trimming passwords', async () => {
    const user = userEvent.setup();
    const client = clientFor();
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Sign in' });
    await user.type(screen.getByLabelText('Login'), 'é'.repeat(129));
    await user.type(screen.getByLabelText('Password'), ' ');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByText('Login is too long. Check it and try again.')).toBeVisible();
    expect(client.login).not.toHaveBeenCalled();
    await user.clear(screen.getByLabelText('Login'));
    await user.type(screen.getByLabelText('Login'), 'owner');
    await user.clear(screen.getByLabelText('Password'));
    await user.type(screen.getByLabelText('Password'), 'päss');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(client.login).toHaveBeenCalledWith({ login: 'owner', password: 'päss' }, anonymous, expect.objectContaining({ signal: expect.any(AbortSignal) }));
  });

  it('submits sign-in on Enter, blocks duplicate submissions and clears credentials', async () => {
    const user = userEvent.setup();
    let resolve!: (value: AuthContext) => void;
    const client = clientFor();
    client.login = vi.fn(() => new Promise<AuthContext>((done) => { resolve = done; }));
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Sign in' });
    await user.type(screen.getByLabelText('Login'), 'owner');
    await user.type(screen.getByLabelText('Password'), 'secret{Enter}');
    expect(client.login).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText('Password')).toHaveValue('');
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled();
    resolve(signedIn);
    expect(await screen.findByRole('heading', { name: 'Agents' })).toBeVisible();
  });

  it('synchronously guards two submit events while the authentication request is pending', async () => {
    const user = userEvent.setup();
    const client = clientFor();
    client.login = vi.fn(() => new Promise<AuthContext>(() => undefined));
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Sign in' });
    await user.type(screen.getByLabelText('Login'), 'owner');
    await user.type(screen.getByLabelText('Password'), 'secret');
    const form = screen.getByRole('button', { name: 'Sign in' }).closest('form');
    fireEvent.submit(form!);
    fireEvent.submit(form!);
    expect(client.login).toHaveBeenCalledOnce();
  });

  it('shows the server identity as text and signs out through a fresh anonymous context', async () => {
    const user = userEvent.setup();
    const client = clientFor(signedIn);
    client.context = vi.fn().mockResolvedValueOnce(signedIn).mockResolvedValueOnce(anonymous);
    render(<AuthPage client={client} />);
    expect(await screen.findByRole('heading', { name: 'Agents' })).toBeVisible();
    expect(screen.getByText('owner')).toBeVisible();
    expect(screen.getByText('administrator')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    expect(client.logout).toHaveBeenCalledOnce();
    expect(client.context).toHaveBeenCalledTimes(2);
  });

  it('shows the signed-in Agents workspace inside the application shell', async () => {
    render(<AuthPage client={clientFor(signedIn)} />);
    expect(await screen.findByRole('heading', { name: 'Agents' })).toBeVisible();
    expect(screen.getByRole('navigation', { name: 'Primary navigation' })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Product' })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Operations' })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Administration' })).toBeVisible();
    expect(screen.getByRole('link', { name: 'Agents' })).toHaveAttribute('aria-current', 'page');
    for (const destination of ['SLOs', 'Alerts']) {
      expect(screen.getByText(destination)).toBeVisible();
      expect(screen.getByText(destination).closest('[aria-disabled="true"]')).toHaveTextContent('Not available yet');
    }
    expect(screen.getByRole('link', { name: 'SLIs' })).toHaveAttribute('href', '/slis');
    expect(screen.getByRole('link', { name: 'Datasources' })).toHaveAttribute('href', '/datasources');
    expect(screen.getByRole('link', { name: 'Datasources' })).not.toHaveAttribute('aria-disabled');
    expect(screen.queryByRole('heading', { name: 'Signed in' })).not.toBeInTheDocument();
    expect(screen.getByText('owner')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeVisible();
  });

  it('switches between signed-in screens without a document or session reload', async () => {
    const user = userEvent.setup();
    const client = clientFor(signedIn);
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Agents' });
    const workspace = document.querySelector('.app-workspace');
    await user.click(within(screen.getByRole('navigation', { name: 'Primary navigation' })).getByRole('link', { name: 'Datasources' }));
    await waitFor(() => expect(location.pathname).toBe('/datasources'));
    expect(await screen.findByRole('heading', { name: 'Datasources' })).toBeVisible();
    expect(within(screen.getByRole('navigation', { name: 'Primary navigation' })).getByRole('link', { name: 'Datasources' })).toHaveAttribute('aria-current', 'page');
    expect(document.querySelector('.app-workspace')).toBe(workspace);
    await user.click(within(screen.getByRole('navigation', { name: 'Primary navigation' })).getByRole('link', { name: 'SLIs' }));
    await waitFor(() => expect(location.pathname).toBe('/slis'));
    expect(await screen.findByRole('heading', { name: 'SLIs' })).toBeVisible();
    expect(within(screen.getByRole('navigation', { name: 'Primary navigation' })).getByRole('link', { name: 'SLIs' })).toHaveAttribute('aria-current', 'page');
    await user.click(within(screen.getByRole('navigation', { name: 'Primary navigation' })).getByRole('link', { name: 'Agents' }));
    await waitFor(() => expect(location.pathname).toBe('/agents'));
    expect(screen.getByRole('heading', { name: 'Agents' })).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Agents' })).toHaveFocus();
    expect(screen.queryByText('Checking your session')).not.toBeInTheDocument();
    expect(client.context).toHaveBeenCalledOnce();
  });

  it('canonicalizes signed-in root links to Agents while preserving their query and fragment', async () => {
    history.replaceState({}, '', '/?agent=agent-1&agents_cursor=agents-next&credentials_cursor=credentials-next#main');
    render(<AuthPage client={clientFor(signedIn)} />);
    expect(await screen.findByRole('heading', { name: 'Agents' })).toBeVisible();
    await waitFor(() => expect(location.href).toContain('/agents?agent=agent-1&agents_cursor=agents-next&credentials_cursor=credentials-next#main'));
  });

  it('opens the mobile navigation as a contained drawer and restores focus on Escape', async () => {
    const user = userEvent.setup();
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: query === '(max-width: 860px)' }) as MediaQueryList);
    render(<AuthPage client={clientFor(signedIn)} />);
    await screen.findByRole('heading', { name: 'Agents' });
    const menu = screen.getByRole('button', { name: 'Menu' });
    await user.click(menu);
    const drawer = screen.getByRole('dialog', { name: 'Navigation menu' });
    expect(screen.getByRole('button', { name: 'Close navigation' })).toHaveFocus();
    await user.keyboard('{Shift>}{Tab}{/Shift}');
    expect(within(drawer).getByRole('button', { name: 'Sign out' })).toHaveFocus();
    await user.keyboard('{Escape}');
    expect(menu).toHaveFocus();
    expect(screen.queryByRole('dialog', { name: 'Navigation menu' })).not.toBeInTheDocument();
  });

  it('closes the mobile drawer and focuses sign-out uncertainty when logout fails', async () => {
    const user = userEvent.setup();
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: query === '(max-width: 860px)' }) as MediaQueryList);
    const client = clientFor(signedIn);
    client.logout = vi.fn().mockRejectedValue(new AuthFailure('unavailable', 'logout_unconfirmed', 'untrusted text'));
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Agents' });
    await user.click(screen.getByRole('button', { name: 'Menu' }));
    const drawer = screen.getByRole('dialog', { name: 'Navigation menu' });
    await user.click(within(drawer).getByRole('button', { name: 'Sign out' }));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(/sign-out could not be confirmed/i);
    expect(alert).toHaveFocus();
    expect(screen.queryByRole('dialog', { name: 'Navigation menu' })).not.toBeInTheDocument();
    expect(document.querySelector('.app-workspace')).not.toHaveAttribute('inert');
    expect(screen.getByRole('heading', { name: 'Agents' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Check session' })).toBeVisible();
    expect(await screen.findByText('No agents have been registered.')).toBeVisible();
    expect(client.logout).toHaveBeenCalledOnce();
  });

  it('validates password rotation and announces success only after an explicit successful response', async () => {
    const user = userEvent.setup();
    const client = clientFor(restricted);
    let complete!: (value: AuthContext) => void;
    client.changePassword = vi.fn(() => new Promise<AuthContext>((resolve) => { complete = resolve; }));
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Set your permanent password' });
    await user.type(screen.getByLabelText('Current password'), 'same password');
    await user.type(screen.getByLabelText('New password'), 'same password');
    await user.type(screen.getByLabelText('Confirm new password'), 'different password');
    await user.click(screen.getByRole('button', { name: 'Set password' }));
    expect(await screen.findByText('Choose a new password that differs from the current password.')).toBeVisible();
    expect(screen.getByText('Enter the same new password again.')).toBeVisible();
    expect(client.changePassword).not.toHaveBeenCalled();
    await user.clear(screen.getByLabelText('New password'));
    await user.type(screen.getByLabelText('New password'), 'a new permanent password');
    await user.clear(screen.getByLabelText('Confirm new password'));
    await user.type(screen.getByLabelText('Confirm new password'), 'a new permanent password');
    await user.click(screen.getByRole('button', { name: 'Set password' }));
    await waitFor(() => expect(client.changePassword).toHaveBeenCalledOnce());
    expect(screen.getByLabelText('Current password')).toHaveValue('');
    expect(screen.getByLabelText('New password')).toHaveValue('');
    expect(screen.getByLabelText('Confirm new password')).toHaveValue('');
    complete(signedIn);
    expect(await screen.findByText('Your permanent password is set.')).toBeVisible();
    expect(client.changePassword).toHaveBeenCalledWith({ current_password: 'same password', new_password: 'a new permanent password' }, restricted, expect.objectContaining({ signal: expect.any(AbortSignal) }));
  });

  it('enforces password limits by UTF-8 bytes and Unicode code points', async () => {
    const user = userEvent.setup();
    const client = clientFor(restricted);
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Set your permanent password' });
    await user.click(screen.getByLabelText('Current password'));
    await user.paste('c'.repeat(513));
    await user.type(screen.getByLabelText('New password'), 'a'.repeat(15));
    await user.type(screen.getByLabelText('Confirm new password'), 'a'.repeat(15));
    await user.click(screen.getByRole('button', { name: 'Set password' }));
    expect(await screen.findByText('Password is too long. Check it and try again.')).toBeVisible();
    expect(client.changePassword).not.toHaveBeenCalled();
    await user.clear(screen.getByLabelText('Current password'));
    await user.type(screen.getByLabelText('Current password'), 'current');
    await user.clear(screen.getByLabelText('New password'));
    await user.click(screen.getByLabelText('New password'));
    await user.paste('a'.repeat(129));
    await user.clear(screen.getByLabelText('Confirm new password'));
    await user.click(screen.getByLabelText('Confirm new password'));
    await user.paste('a'.repeat(129));
    await user.click(screen.getByRole('button', { name: 'Set password' }));
    expect(await screen.findByText('Use 128 characters or fewer.')).toBeVisible();
    expect(client.changePassword).not.toHaveBeenCalled();
    await user.clear(screen.getByLabelText('New password'));
    await user.type(screen.getByLabelText('New password'), 'too short');
    await user.clear(screen.getByLabelText('Confirm new password'));
    await user.type(screen.getByLabelText('Confirm new password'), 'too short');
    await user.click(screen.getByRole('button', { name: 'Set password' }));
    expect(await screen.findByText('Use at least 15 characters.')).toBeVisible();
    expect(client.changePassword).not.toHaveBeenCalled();
  });

  it('keeps uncertain mutation outcomes visible even when recovery returns a normal session', async () => {
    const user = userEvent.setup();
    const client = clientFor(anonymous);
    client.login = vi.fn().mockRejectedValue(new AuthFailure('uncertain', 'outcome_unknown', 'untrusted text', signedIn));
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Sign in' });
    await user.type(screen.getByLabelText('Login'), 'owner');
    await user.type(screen.getByLabelText('Password'), 'secret');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/may have completed/i);
    expect(screen.getByRole('heading', { name: 'Agents' })).toBeVisible();
    expect(screen.getByRole('alert')).toBeVisible();
    expect(screen.queryByText('untrusted text')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Check session' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    expect(screen.getByRole('alert')).toHaveTextContent(/may have completed/i);
    expect(client.login).toHaveBeenCalledOnce();
  });

  it('does not claim sign-out when logout fails and exposes deliberate session recovery', async () => {
    const user = userEvent.setup();
    const client = clientFor(restricted);
    client.logout = vi.fn().mockRejectedValue(new AuthFailure('unavailable', 'logout_unconfirmed', 'untrusted text'));
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Set your permanent password' });
    await user.type(screen.getByLabelText('Current password'), 'draft current');
    await user.type(screen.getByLabelText('New password'), 'draft new password');
    await user.type(screen.getByLabelText('Confirm new password'), 'mismatched draft password');
    await user.click(screen.getByRole('button', { name: 'Set password' }));
    expect(await screen.findByText('Enter the same new password again.')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not be confirmed/i);
    expect(screen.getByRole('heading', { name: 'Set your permanent password' })).toBeVisible();
    expect(screen.getByLabelText('Current password')).toHaveValue('');
    expect(screen.getByLabelText('New password')).toHaveValue('');
    expect(screen.getByLabelText('Confirm new password')).toHaveValue('');
    expect(screen.queryByText('Enter the same new password again.')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Check session' })).toBeVisible();
    expect(client.logout).toHaveBeenCalledOnce();
  });

  it('clears restricted password drafts before logout and keeps them cleared after sign-in', async () => {
    const user = userEvent.setup();
    const client = clientFor(restricted);
    client.context = vi.fn().mockResolvedValueOnce(restricted).mockResolvedValueOnce(anonymous);
    client.login = vi.fn().mockResolvedValue(restricted);
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Set your permanent password' });
    await user.type(screen.getByLabelText('Current password'), 'draft current');
    await user.type(screen.getByLabelText('New password'), 'draft new password');
    await user.type(screen.getByLabelText('Confirm new password'), 'draft new password');
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    await user.type(screen.getByLabelText('Login'), 'owner');
    await user.type(screen.getByLabelText('Password'), 'initial password');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('heading', { name: 'Set your permanent password' })).toBeVisible();
    expect(screen.getByLabelText('Current password')).toHaveValue('');
    expect(screen.getByLabelText('New password')).toHaveValue('');
    expect(screen.getByLabelText('Confirm new password')).toHaveValue('');
    expect(client.changePassword).not.toHaveBeenCalled();
    expect(client.logout).toHaveBeenCalledOnce();
    expect(client.login).toHaveBeenCalledOnce();
  });

  it('describes an unknown password change accurately when recovery finds no session', async () => {
    const user = userEvent.setup();
    const client = clientFor(restricted);
    client.changePassword = vi.fn().mockRejectedValue(new AuthFailure('uncertain', 'outcome_unknown', 'untrusted text', anonymous));
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Set your permanent password' });
    await user.type(screen.getByLabelText('Current password'), 'current password');
    await user.type(screen.getByLabelText('New password'), 'a different permanent password');
    await user.type(screen.getByLabelText('Confirm new password'), 'a different permanent password');
    await user.click(screen.getByRole('button', { name: 'Set password' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/password change may have completed/i);
    expect(screen.getByRole('alert')).toHaveTextContent(/new password may already be active/i);
    expect(screen.getByRole('alert')).toHaveTextContent(/action was not repeated/i);
    expect(screen.getByRole('button', { name: 'Check session' })).toBeVisible();
    expect(client.changePassword).toHaveBeenCalledOnce();
  });

  it('uses locally authored retry guidance and only checks an expired session when requested', async () => {
    const user = userEvent.setup();
    const client = clientFor();
    client.login = vi.fn()
      .mockRejectedValueOnce(new AuthFailure('rejected', 'rate_limited', 'untrusted server detail', null, 15))
      .mockRejectedValueOnce(new AuthFailure('rejected', 'csrf_invalid', 'untrusted server detail'));
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Sign in' });
    await user.type(screen.getByLabelText('Login'), 'owner');
    await user.type(screen.getByLabelText('Password'), 'secret');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Wait at least 15 seconds');
    expect(screen.getByRole('alert')).not.toHaveTextContent('untrusted server detail');
    expect(screen.getByLabelText('Login')).toHaveValue('owner');
    expect(screen.getByLabelText('Password')).toHaveValue('');
    expect(client.login).toHaveBeenCalledOnce();
    await user.type(screen.getByLabelText('Login'), 'owner');
    await user.type(screen.getByLabelText('Password'), 'secret');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('button', { name: 'Check session' })).toBeVisible();
    expect(client.login).toHaveBeenCalledTimes(2);
    await user.click(screen.getByRole('button', { name: 'Check session' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    expect(client.context).toHaveBeenCalledTimes(2);
    expect(client.login).toHaveBeenCalledTimes(2);
  });

  it('cancels initial context on unmount and ignores stale results', async () => {
    let signal!: AbortSignal;
    let resolve!: (value: AuthContext) => void;
    const client = clientFor();
    client.context = vi.fn((options) => { signal = options?.signal as AbortSignal; return new Promise<AuthContext>((done) => { resolve = done; }); });
    const view = render(<AuthPage client={client} />);
    await waitFor(() => expect(client.context).toHaveBeenCalledOnce());
    view.unmount();
    expect(signal.aborted).toBe(true);
    resolve(signedIn);
  });

  it('aborts an in-flight login when the page unmounts', async () => {
    const user = userEvent.setup();
    let signal!: AbortSignal;
    const client = clientFor();
    client.login = vi.fn((_input, _context, options) => {
      signal = options?.signal as AbortSignal;
      return new Promise<AuthContext>(() => undefined);
    });
    const view = render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Sign in' });
    await user.type(screen.getByLabelText('Login'), 'owner');
    await user.type(screen.getByLabelText('Password'), 'secret');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(client.login).toHaveBeenCalledOnce());
    view.unmount();
    expect(signal.aborted).toBe(true);
  });
});
