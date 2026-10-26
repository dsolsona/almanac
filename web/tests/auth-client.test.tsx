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

function pendingBody(onCancel: () => void, prefix = '{"csrf_token":', cancelResult?: Promise<void> | (() => Promise<void>)) {
  return new Response(new ReadableStream<Uint8Array>({
    start(controller) { controller.enqueue(new TextEncoder().encode(prefix)); },
    cancel() { onCancel(); return typeof cancelResult === 'function' ? cancelResult() : cancelResult; },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } });
}

function responseWithChunks(chunks: Uint8Array[], onCancel: () => void, close = true) {
  return new Response(new ReadableStream<Uint8Array>({
    start(controller) { for (const chunk of chunks) controller.enqueue(chunk); if (close) controller.close(); },
    cancel() { onCancel(); },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } });
}

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
    expect(new Headers(fetcher.mock.calls[0][1]?.headers).has('Idempotency-Key')).toBe(false);
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
    expect(init).toMatchObject({ mode: 'same-origin', cache: 'no-store', redirect: 'error', signal: expect.any(AbortSignal) });
  });

  it('rejects malformed auth shapes and a login response without a session', async () => {
    for (const body of [
      { csrf_token: '', session: null }, { csrf_token: null, session: null },
      { csrf_token: 'csrf', session: { ...signedIn.session, restricted: null } },
      { csrf_token: 'csrf', session: { ...signedIn.session, user_id: '' } },
      { csrf_token: 'csrf', session: { ...signedIn.session, role: 2 } },
      { csrf_token: 'csrf', session: { login: 'owner', role: 'Administrator', restricted: false } },
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

  it('accepts a password change only for the same user in a normal session', async () => {
    const changed = { csrf_token: 'rotated-csrf', session: signedIn.session };
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(changed));
    await expect(createAuthClient(fetcher).changePassword(
      { current_password: 'old', new_password: 'new' }, signedIn,
    )).resolves.toEqual(changed);
    expect(fetcher.mock.calls[0][0]).toBe('/api/v1/auth/password');
    expect(JSON.parse(String(fetcher.mock.calls[0][1]?.body))).toEqual({ current_password: 'old', new_password: 'new' });
    expect(fetcher.mock.calls[0][1]).toMatchObject({ credentials: 'same-origin', mode: 'same-origin', cache: 'no-store', redirect: 'error' });
    expect(new Headers(fetcher.mock.calls[0][1]?.headers).get('X-CSRF-Token')).toBe(signedIn.csrf_token);
    const differentUser = { csrf_token: 'rotated', session: { ...signedIn.session, user_id: 'user-2' } };
    const mismatchFetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(jsonResponse(differentUser)).mockResolvedValueOnce(jsonResponse(context));
    await expect(createAuthClient(mismatchFetcher).changePassword(
      { current_password: 'old', new_password: 'new' }, signedIn,
    )).rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
    expect(mismatchFetcher.mock.calls.map(([url]) => url)).toEqual(['/api/v1/auth/password', '/api/v1/auth/context']);
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

  it('keeps a lost password response uncertain even when context is signed in', async () => {
    const fetcher = vi.fn<typeof fetch>().mockRejectedValueOnce(new TypeError('private transport detail'))
      .mockResolvedValueOnce(jsonResponse(signedIn));
    await expect(createAuthClient(fetcher).changePassword(
      { current_password: 'old-secret', new_password: 'new-secret' }, signedIn,
    )).rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown', recovery: signedIn });
    expect(fetcher.mock.calls.map(([url]) => url)).toEqual(['/api/v1/auth/password', '/api/v1/auth/context']);
    expect(fetcher).toHaveBeenCalledTimes(2);
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

  it('cancels streaming non-success context and server-error response bodies', async () => {
    const contextCancelled = vi.fn();
    const contextFetch = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(new ReadableStream<Uint8Array>({ cancel() { contextCancelled(); } }), { status: 503 }),
    );
    await expect(createAuthClient(contextFetch).context()).rejects.toMatchObject({ kind: 'unavailable' });
    expect(contextCancelled).toHaveBeenCalledOnce();

    const mutationCancelled = vi.fn();
    const serverError = new Response(new ReadableStream<Uint8Array>({
      cancel() { mutationCancelled(); return new Promise<void>(() => undefined); },
    }), { status: 503 });
    const mutationFetch = vi.fn<typeof fetch>().mockResolvedValueOnce(serverError).mockResolvedValueOnce(jsonResponse(context));
    await expect(createAuthClient(mutationFetch).login({ login: 'owner', password: 'secret' }, context))
      .rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
    expect(mutationCancelled).toHaveBeenCalledOnce();
  });

  it('accepts only a valid 501 capability rejection and leaves malformed or mismatched 501 uncertain', async () => {
    const accepted = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(problem('capability_unavailable'), 501));
    await expect(createAuthClient(accepted).login({ login: 'owner', password: 'secret' }, context))
      .rejects.toMatchObject({ kind: 'rejected', code: 'capability_unavailable' });
    expect(accepted).toHaveBeenCalledOnce();
    for (const body of [{ code: 'capability_unavailable' }, problem('invalid_request')]) {
      const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(jsonResponse(body, 501)).mockResolvedValueOnce(jsonResponse(context));
      await expect(createAuthClient(fetcher).login({ login: 'owner', password: 'secret' }, context))
        .rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
      expect(fetcher.mock.calls.map(([url]) => url)).toEqual(['/api/v1/auth/login', '/api/v1/auth/context']);
    }
  });

  it('does not expose invalid Retry-After values', async () => {
    for (const value of ['0', '-1', '1.5', '9007199254740992', 'injected']) {
      const fetcher = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(problem('rate_limited'), 429, { 'Retry-After': value }));
      await expect(createAuthClient(fetcher).login({ login: 'owner', password: 'secret' }, context))
        .rejects.toMatchObject({ kind: 'rejected', retryAfterSeconds: null });
    }
  });

  it('rejects oversized, malformed and truncated bodies without exposing the body', async () => {
    const oversizedCancelled = vi.fn();
    const huge = responseWithChunks([
      new Uint8Array(64 * 1024), new Uint8Array([1]),
    ], oversizedCancelled, false);
    const responses = [huge, new Response('{bad json', { status: 200 }), new Response('{"csrf_token":', { status: 200 })];
    for (const response of responses) {
      const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(response).mockResolvedValueOnce(jsonResponse(context));
      const failure = await createAuthClient(fetcher).login({ login: 'owner', password: 'do-not-echo' }, context).catch((error: unknown) => error);
      expect(failure).toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
      expect((failure as Error).message).not.toMatch(/bad json|do-not-echo|csrf_token/);
      expect(fetcher).toHaveBeenCalledTimes(2);
    }
    expect(oversizedCancelled).toHaveBeenCalledOnce();
  });

  it('treats redirects and unknown status/code pairs conservatively', async () => {
    const cases: Array<{ first: () => Promise<Response> }> = [
      { first: () => Promise.reject(new TypeError('redirect blocked')) },
      { first: async () => jsonResponse(problem('invalid_request'), 409) },
      { first: async () => jsonResponse(problem('future_code'), 418) },
    ];
    for (const entry of cases) {
      const fetcher = vi.fn<typeof fetch>().mockImplementationOnce(entry.first).mockResolvedValueOnce(jsonResponse(context));
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
    expect(init).toMatchObject({ credentials: 'same-origin', mode: 'same-origin', cache: 'no-store', redirect: 'error' });
    expect(new Headers(init?.headers).has('Idempotency-Key')).toBe(false);
    const unexpected = vi.fn<typeof fetch>().mockResolvedValue(new Response('body', { status: 200 }));
    await expect(createAuthClient(unexpected).logout(signedIn)).rejects.toMatchObject({ kind: 'unavailable' });
    const bodylessViolation = {
      status: 204, body: new ReadableStream<Uint8Array>(),
    } as Response;
    await expect(createAuthClient(vi.fn<typeof fetch>().mockResolvedValue(bodylessViolation)).logout(signedIn))
      .rejects.toMatchObject({ kind: 'unavailable' });
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

  it('performs no request when the caller signal is already cancelled', async () => {
    const controller = new AbortController();
    controller.abort();
    const fetcher = vi.fn<typeof fetch>();
    await expect(createAuthClient(fetcher).login({ login: 'owner', password: 'secret' }, context, { signal: controller.signal }))
      .rejects.toMatchObject({ kind: 'unavailable' });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('cancels a pending body reader when the operation deadline expires', async () => {
    vi.useFakeTimers();
    const cancelled = vi.fn();
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(pendingBody(cancelled));
    const pending = createAuthClient(fetcher).context();
    const assertion = expect(pending).rejects.toMatchObject({ kind: 'unavailable' });
    await vi.advanceTimersByTimeAsync(30_000);
    await assertion;
    expect(cancelled).toHaveBeenCalledOnce();
  });

  it('does not wait for body cancellation settlement and absorbs cancellation rejection', async () => {
    vi.useFakeTimers();
    const cancelled = vi.fn();
    const response = pendingBody(cancelled, '{"csrf_token":', () => new Promise<void>(() => undefined));
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response);
    const pending = createAuthClient(fetcher).context();
    const assertion = expect(pending).rejects.toMatchObject({ kind: 'unavailable' });
    await vi.advanceTimersByTimeAsync(30_000);
    await assertion;
    expect(cancelled).toHaveBeenCalledOnce();
    await Promise.resolve();
    expect(response.body?.locked).toBe(false);
  });

  it('handles rejected stream cancellation without an unhandled rejection', async () => {
    vi.useFakeTimers();
    const cancelled = vi.fn();
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(pendingBody(
      cancelled, '{"csrf_token":', () => Promise.reject(new Error('private cancellation failure')),
    ));
    const pending = createAuthClient(fetcher).context();
    const assertion = expect(pending).rejects.toMatchObject({ kind: 'unavailable' });
    await vi.advanceTimersByTimeAsync(30_000);
    await assertion;
    await Promise.resolve();
    expect(cancelled).toHaveBeenCalledOnce();
  });

  it('cleans caller abort listeners and the operation timer after success', async () => {
    vi.useFakeTimers();
    const controller = new AbortController();
    const add = vi.spyOn(controller.signal, 'addEventListener');
    const remove = vi.spyOn(controller.signal, 'removeEventListener');
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(jsonResponse(context));
    await expect(createAuthClient(fetcher).context({ signal: controller.signal })).resolves.toEqual(context);
    const requestSignal = fetcher.mock.calls[0][1]?.signal;
    expect(add).toHaveBeenCalledWith('abort', expect.any(Function), { once: true });
    expect(remove).toHaveBeenCalledWith('abort', expect.any(Function));
    await vi.advanceTimersByTimeAsync(30_000);
    expect(requestSignal?.aborted).toBe(false);
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('cancels a pending body on caller abort without recovery or leaked abort listeners', async () => {
    const controller = new AbortController();
    const add = vi.spyOn(controller.signal, 'addEventListener');
    const remove = vi.spyOn(controller.signal, 'removeEventListener');
    const cancelled = vi.fn();
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(pendingBody(cancelled));
    const pending = createAuthClient(fetcher).login({ login: 'owner', password: 'secret' }, context, { signal: controller.signal });
    await Promise.resolve();
    controller.abort();
    await expect(pending).rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
    expect(cancelled).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledOnce();
    expect(remove).toHaveBeenCalledWith('abort', expect.any(Function));
    expect(add).toHaveBeenCalled();
  });

  it('uses only the original remaining deadline for recovery', async () => {
    vi.useFakeTimers();
    let rejectMutation!: (error: Error) => void;
    let markRecoveryStarted!: () => void;
    const recoveryStarted = new Promise<void>((resolve) => { markRecoveryStarted = resolve; });
    const fetcher = vi.fn<typeof fetch>()
      .mockImplementationOnce(() => new Promise((_, reject) => { rejectMutation = reject; }))
      .mockImplementationOnce(() => { markRecoveryStarted(); return new Promise(() => undefined); });
    const pending = createAuthClient(fetcher).login({ login: 'owner', password: 'secret' }, context);
    const assertion = expect(pending).rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown', recovery: null });
    await vi.advanceTimersByTimeAsync(20_000);
    rejectMutation(new TypeError('lost response'));
    await recoveryStarted;
    await vi.advanceTimersByTimeAsync(10_000);
    await assertion;
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});
