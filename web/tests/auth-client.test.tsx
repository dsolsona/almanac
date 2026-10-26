import { afterEach, describe, expect, it, vi } from 'vitest';
import { AuthFailure, createAuthClient, type AuthContext } from '../src/auth/client.ts';

const context: AuthContext = {
  csrf_token: 'tab-csrf',
  session: null,
};

const signedIn: AuthContext = {
  csrf_token: 'session-csrf',
  session: { user_id: 'user-1', login: 'owner', role: 'Administrator', restricted: false },
};

const jsonResponse = (body: unknown, status = 200, headers?: HeadersInit): Response =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });

const problem = (code: string) => ({
  code,
  message: 'untrusted server text',
  request_id: 'request-1',
  field_errors: [],
});

afterEach(() => vi.useRealTimers());

describe('browser authentication client', () => {
  it('uses fixed same-origin context request settings and validates its response', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(context));
    await expect(createAuthClient(fetcher).context()).resolves.toEqual(context);
    expect(fetcher).toHaveBeenCalledWith('/api/v1/auth/context', {
      method: 'POST', body: '{}', credentials: 'same-origin', mode: 'same-origin',
      cache: 'no-store', redirect: 'error', headers: { 'Content-Type': 'application/json' },
      signal: expect.any(AbortSignal),
    });
  });

  it('sends generated login data and CSRF without replay headers', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(signedIn));
    const input = { login: 'owner', password: 'correct horse' };
    await expect(createAuthClient(fetcher).login(input, context)).resolves.toEqual(signedIn);
    expect(fetcher).toHaveBeenCalledOnce();
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/api/v1/auth/login');
    expect(init).toMatchObject({ method: 'POST', body: JSON.stringify(input), credentials: 'same-origin' });
    expect(new Headers(init?.headers).get('X-CSRF-Token')).toBe(context.csrf_token);
    expect(new Headers(init?.headers).has('Idempotency-Key')).toBe(false);
  });

  it('rejects malformed auth shapes and a login response without a session', async () => {
    for (const body of [
      { csrf_token: '', session: null },
      { csrf_token: 'csrf', session: { ...signedIn.session, restricted: null } },
      { csrf_token: 'csrf' },
    ]) {
      const fetcher = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(body));
      await expect(createAuthClient(fetcher).context()).rejects.toMatchObject({ kind: 'unavailable' });
    }
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(context));
    await expect(createAuthClient(fetcher).login({ login: 'owner', password: 'secret' }, context))
      .rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
  });

  it('requires the same normal session after password change and rejects missing session before I/O', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse({
      csrf_token: 'next-csrf', session: { ...signedIn.session, restricted: true },
    }));
    await expect(createAuthClient(fetcher).changePassword(
      { current_password: 'old', new_password: 'new' }, signedIn,
    )).rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
    const neverFetch = vi.fn<typeof fetch>();
    await expect(createAuthClient(neverFetch).changePassword(
      { current_password: 'old', new_password: 'new' }, context,
    )).rejects.toMatchObject({ kind: 'rejected' });
    expect(neverFetch).not.toHaveBeenCalled();
  });

  it('keeps a lost mutation uncertain and makes at most one recovery context read', async () => {
    const fetcher = vi.fn<typeof fetch>()
      .mockRejectedValueOnce(new TypeError('secret transport detail'))
      .mockResolvedValueOnce(jsonResponse(signedIn));
    let failure: unknown;
    try {
      await createAuthClient(fetcher).login({ login: 'owner', password: 'sensitive password' }, context);
    } catch (error) { failure = error; }
    expect(failure).toBeInstanceOf(AuthFailure);
    expect(failure).toMatchObject({ kind: 'uncertain', code: 'outcome_unknown', recovery: signedIn });
    expect((failure as Error).message).not.toMatch(/sensitive password|secret transport detail/);
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls.map(([url]) => url)).toEqual([
      '/api/v1/auth/login', '/api/v1/auth/context',
    ]);
  });

  it('treats a valid documented rate limit as definitive and exposes only safe retry seconds', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(
      jsonResponse(problem('rate_limited'), 429, { 'Retry-After': '17' }),
    );
    await expect(createAuthClient(fetcher).login({ login: 'owner', password: 'secret' }, context))
      .rejects.toMatchObject({ kind: 'rejected', code: 'rate_limited', retryAfterSeconds: 17 });
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('treats malformed problems and unexpected status/code pairs as uncertain', async () => {
    for (const response of [jsonResponse(problem('rate_limited'), 503), jsonResponse({ code: 'invalid_request' }, 400)]) {
      const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(response).mockResolvedValueOnce(jsonResponse(context));
      await expect(createAuthClient(fetcher).login({ login: 'owner', password: 'secret' }, context))
        .rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
      expect(fetcher).toHaveBeenCalledTimes(2);
    }
  });

  it('requires an empty 204 logout response and sends its CSRF token', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, { status: 204 }));
    await expect(createAuthClient(fetcher).logout(signedIn)).resolves.toBeUndefined();
    const [, init] = fetcher.mock.calls[0];
    expect(init?.body).toBe('{}');
    expect(new Headers(init?.headers).get('X-CSRF-Token')).toBe(signedIn.csrf_token);
  });

  it('bounds pending fetches by the operation deadline', async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn<typeof fetch>(() => new Promise(() => undefined));
    const pending = createAuthClient(fetcher).context();
    const assertion = expect(pending).rejects.toMatchObject({ kind: 'unavailable' });
    await vi.advanceTimersByTimeAsync(30_000);
    await assertion;
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('keeps a submitted login uncertain when the caller cancels and does not recover after cancellation', async () => {
    const controller = new AbortController();
    let submitted!: () => void;
    const started = new Promise<void>((resolve) => { submitted = resolve; });
    const fetcher = vi.fn<typeof fetch>(() => {
      submitted();
      return new Promise(() => undefined);
    });
    const pending = createAuthClient(fetcher).login(
      { login: 'owner', password: 'secret' }, context, { signal: controller.signal },
    );
    await started;
    controller.abort();
    await expect(pending).rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
    expect(fetcher).toHaveBeenCalledOnce();
  });
});
