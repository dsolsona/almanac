import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { AuthFailure, type AuthContext, type createAuthClient } from '../src/auth/client.ts';
import { AuthPage } from '../src/auth/AuthPage.tsx';

type Client = ReturnType<typeof createAuthClient>;
const anonymous: AuthContext = { csrf_token: 'csrf-anonymous', session: null };
const restricted: AuthContext = { csrf_token: 'csrf-restricted', session: { user_id: 'u1', login: 'owner', role: 'Administrator', restricted: true } };
const signedIn: AuthContext = { csrf_token: 'csrf-normal', session: { user_id: 'u1', login: 'owner', role: 'Administrator', restricted: false } };

function clientFor(context: AuthContext = anonymous): Client {
  return {
    context: vi.fn().mockResolvedValue(context),
    login: vi.fn().mockResolvedValue(signedIn),
    changePassword: vi.fn().mockResolvedValue(signedIn),
    logout: vi.fn().mockResolvedValue(undefined),
  };
}

afterEach(() => {
  cleanup();
  document.documentElement.classList.remove('dark');
  document.documentElement.style.colorScheme = '';
  localStorage.clear();
  vi.restoreAllMocks();
});

describe('authentication page', () => {
  it('shows loading, then the anonymous sign-in form with accessible shell', async () => {
    let resolve!: (value: AuthContext) => void;
    const client = clientFor();
    client.context = vi.fn(() => new Promise<AuthContext>((done) => { resolve = done; }));
    render(<AuthPage client={client} />);
    expect(screen.getByRole('status', { name: 'Checking your session' })).toBeVisible();
    expect(screen.getByRole('button', { name: /switch to dark theme/i })).toBeVisible();
    resolve(anonymous);
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    expect(screen.getByRole('main')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Skip to content' })).toHaveAttribute('href', '#main');
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
    expect(await screen.findByText('Login must be at most 256 UTF-8 bytes.')).toBeVisible();
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
    expect(await screen.findByRole('heading', { name: 'Signed in' })).toBeVisible();
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
    expect(await screen.findByRole('heading', { name: 'Signed in' })).toBeVisible();
    expect(screen.getByText('owner')).toBeVisible();
    expect(screen.getByText('Administrator')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    expect(client.logout).toHaveBeenCalledOnce();
    expect(client.context).toHaveBeenCalledTimes(2);
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
    await user.type(screen.getByLabelText('Current password'), 'c'.repeat(513));
    await user.type(screen.getByLabelText('New password'), 'a'.repeat(15));
    await user.type(screen.getByLabelText('Confirm new password'), 'a'.repeat(15));
    await user.click(screen.getByRole('button', { name: 'Set password' }));
    expect(await screen.findByText('Password must be at most 512 UTF-8 bytes.')).toBeVisible();
    expect(client.changePassword).not.toHaveBeenCalled();
    await user.clear(screen.getByLabelText('Current password'));
    await user.type(screen.getByLabelText('Current password'), 'current');
    await user.clear(screen.getByLabelText('New password'));
    await user.type(screen.getByLabelText('New password'), 'a'.repeat(129));
    await user.clear(screen.getByLabelText('Confirm new password'));
    await user.type(screen.getByLabelText('Confirm new password'), 'a'.repeat(129));
    await user.click(screen.getByRole('button', { name: 'Set password' }));
    expect(await screen.findByText('New password must be 15–128 Unicode code points and at most 512 UTF-8 bytes.')).toBeVisible();
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
    expect(screen.getByRole('heading', { name: 'Signed in' })).toBeVisible();
    expect(screen.getByRole('alert')).toBeVisible();
    expect(screen.queryByText('untrusted text')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Check session' }));
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeVisible();
    expect(screen.getByRole('alert')).toHaveTextContent(/may have completed/i);
    expect(client.login).toHaveBeenCalledOnce();
  });

  it('does not claim sign-out when logout fails and exposes deliberate session recovery', async () => {
    const user = userEvent.setup();
    const client = clientFor(signedIn);
    client.logout = vi.fn().mockRejectedValue(new AuthFailure('unavailable', 'logout_unconfirmed', 'untrusted text'));
    render(<AuthPage client={client} />);
    await screen.findByRole('heading', { name: 'Signed in' });
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not be confirmed/i);
    expect(screen.getByRole('heading', { name: 'Signed in' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Check session' })).toBeVisible();
    expect(client.logout).toHaveBeenCalledOnce();
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
