import { afterEach, describe, expect, it, vi } from 'vitest';
import { AgentFailure, createAgentClient, type Agent, type AuthContext } from '../src/agents/client.ts';

const auth: AuthContext = { csrf_token: 'session-csrf', session: { user_id: 'u', login: 'admin', role: 'Administrator', restricted: false } };
const id = '9c5b4ef2-75e8-4e76-a4de-baa6a4d77277';
const credentialId = '499c01d1-bbbd-4b75-8e86-8f802bd9ca10';
const replacementCredentialId = '2b3c7c0a-9d85-4e82-bd38-4b567e8c27f0';
const agent: Agent = { id, name: 'edge-a', display_name: 'Edge A', enabled: true, version: '9223372036854775807', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' };
const credential = { id: credentialId, agent_id: id, issued_at: '2026-01-01T00:00:00Z', expires_at: '2026-12-31T00:00:00Z', revoked_at: null, retire_at: null };
const revokedCredential = { ...credential, revoked_at: '2026-02-01T00:00:00Z' };
const disabledAgent = { ...agent, enabled: false };
const json = (value: unknown, status = 200, headers?: HeadersInit) => new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json', ...headers } });
const issuance = { agent, credential, replayed: false, secret_state: 'issued', token: 'one-time-secret' };
afterEach(() => vi.useRealTimers());

describe('browser agent management client', () => {
  it('sends a bounded same-origin list request without CSRF or Origin and preserves decimal versions', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json({ items: [agent], next_cursor: '' }));
    const result = await createAgentClient(fetcher).list({ limit: 200, cursor: 'opaque.cursor' });
    expect(result.items[0].version).toBe('9223372036854775807');
    expect(fetcher).toHaveBeenCalledWith('/api/v1/agents?limit=200&cursor=opaque.cursor', expect.objectContaining({
      method: 'GET', credentials: 'same-origin', mode: 'same-origin', cache: 'no-store', redirect: 'error', signal: expect.any(AbortSignal),
    }));
    const headers = new Headers(fetcher.mock.calls[0][1]?.headers);
    expect(headers.has('X-CSRF-Token')).toBe(false);
    expect(headers.has('Origin')).toBe(false);
  });

  it('validates strong ETag and its agent/version agreement', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json(agent, 200, { ETag: `"agent:${id}:${agent.version}"` }));
    await expect(createAgentClient(fetcher).get(id)).resolves.toEqual({ agent, etag: `"agent:${id}:${agent.version}"` });
    const mismatch = vi.fn<typeof fetch>().mockResolvedValue(json(agent, 200, { ETag: `"agent:${id}:1"` }));
    await expect(createAgentClient(mismatch).get(id)).rejects.toMatchObject({ kind: 'unavailable', code: 'invalid_response' });
  });

  it('sends create with explicit caller key and CSRF, exposing only validated fresh issuance', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json(issuance, 201));
    const input = { name: 'edge-a', display_name: 'Edge A' };
    const result = await createAgentClient(fetcher).create(input, auth, 'caller-key');
    expect(result.token).toBe('one-time-secret');
    expect(fetcher).toHaveBeenCalledOnce();
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('/api/v1/agents');
    expect(init).toMatchObject({ method: 'POST', body: JSON.stringify(input), credentials: 'same-origin', mode: 'same-origin', cache: 'no-store', redirect: 'error' });
    expect(new Headers(init?.headers).get('X-CSRF-Token')).toBe(auth.csrf_token);
    expect(new Headers(init?.headers).get('Idempotency-Key')).toBe('caller-key');
    expect(JSON.parse(String(init?.body))).not.toHaveProperty('current_password');
  });

  it('rejects replay issuance with any token member and never leaks response secrets', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json({ ...issuance, replayed: true, secret_state: 'secret_unavailable', token: null }, 200));
    let failure: unknown;
    try { await createAgentClient(fetcher).create({ name: 'edge-a', display_name: 'Edge A' }, auth, 'key'); }
    catch (error) { failure = error; }
    expect(failure).toBeInstanceOf(AgentFailure);
    expect(failure).toMatchObject({ kind: 'uncertain', code: 'invalid_response' });
    expect(String(failure)).not.toMatch(/password-secret|one-time-secret/);
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('accepts replay issuance only when the token member is absent', async () => {
    const replay = { agent, credential, replayed: true, secret_state: 'secret_unavailable' };
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json(replay, 200));
    await expect(createAgentClient(fetcher).create({ name: 'edge-a', display_name: 'Edge A' }, auth, 'key'))
      .resolves.toEqual(replay);
  });

  it('preserves a validated management outage cause and safe request ID without replaying', async () => {
    const requestID = '0123456789abcdef0123456789abcdef';
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json({
      type: 'about:blank', title: 'Unavailable', status: 503, code: 'management_unavailable',
      message: 'private backend detail', request_id: requestID, field_errors: [],
    }, 503));
    let caught: unknown;
    try { await createAgentClient(fetcher).create({ name: 'edge-a', display_name: 'Edge A' }, auth, 'same-key'); }
    catch (error) { caught = error; }
    expect(caught).toMatchObject({ kind: 'uncertain', code: 'management_unavailable', requestID });
    expect(String(caught)).not.toContain('private backend detail');
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('reports a timed-out error body as an uncertain timeout without replaying', async () => {
    vi.useFakeTimers();
    let cancelled = false;
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new TextEncoder().encode('{"code":')); },
      cancel() { cancelled = true; },
    }), { status: 503, headers: { 'Content-Type': 'application/json' } }));
    const pending = createAgentClient(fetcher).create({ name: 'edge-a', display_name: 'Edge A' }, auth, 'same-key');
    const result = pending.catch((error: unknown) => error);
    await Promise.resolve();
    await Promise.resolve();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(await result).toMatchObject({ kind: 'uncertain', code: 'request_timeout' });
    expect(cancelled).toBe(true);
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('reports an aborted error body as an uncertain cancellation without replaying', async () => {
    let cancelled = false;
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new TextEncoder().encode('{"code":')); },
      cancel() { cancelled = true; },
    }), { status: 503, headers: { 'Content-Type': 'application/json' } }));
    const controller = new AbortController();
    const pending = createAgentClient(fetcher).create({ name: 'edge-a', display_name: 'Edge A' }, auth, 'same-key', { signal: controller.signal });
    const result = pending.catch((error: unknown) => error);
    await Promise.resolve();
    await Promise.resolve();
    controller.abort();
    expect(await result).toMatchObject({ kind: 'uncertain', code: 'request_cancelled' });
    expect(cancelled).toBe(true);
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('reports a post-dispatch connection failure without exposing transport detail', async () => {
    const fetcher = vi.fn<typeof fetch>().mockRejectedValue(new Error('private certificate and URL detail'));
    let caught: unknown;
    try { await createAgentClient(fetcher).create({ name: 'edge-a', display_name: 'Edge A' }, auth, 'same-key'); }
    catch (error) { caught = error; }
    expect(caught).toMatchObject({ kind: 'uncertain', code: 'connection_failed' });
    expect(String(caught)).not.toMatch(/certificate|URL detail/);
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('checks create and rotate issuance statuses and all token-state combinations', async () => {
    const createFresh = vi.fn<typeof fetch>().mockResolvedValue(json(issuance, 201));
    await expect(createAgentClient(createFresh).create({ name: 'n', display_name: 'd' }, auth, 'key'))
      .resolves.toMatchObject({ replayed: false, token: 'one-time-secret' });
    for (const malformed of [
      { ...issuance, token: '' }, { ...issuance, token: null },
      { ...issuance, replayed: true, secret_state: 'secret_unavailable', token: '' },
      { ...issuance, replayed: true, secret_state: 'secret_unavailable', token: null },
    ]) {
      const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json(malformed, malformed.replayed ? 200 : 201));
      await expect(createAgentClient(fetcher).create({ name: 'n', display_name: 'd' }, auth, 'key'))
        .rejects.toMatchObject({ kind: 'uncertain' });
      expect(fetcher).toHaveBeenCalledOnce();
    }
    const wrongFreshStatus = vi.fn<typeof fetch>().mockResolvedValue(json(issuance, 200));
    await expect(createAgentClient(wrongFreshStatus).create({ name: 'n', display_name: 'd' }, auth, 'key'))
      .rejects.toMatchObject({ kind: 'uncertain' });
    const rotationFresh = { ...issuance, credential: { ...credential, id: replacementCredentialId }, previous_credential: credential };
    const rotate = vi.fn<typeof fetch>().mockResolvedValue(json(rotationFresh));
    await expect(createAgentClient(rotate).rotate(id, { current_password: 'p' }, auth, `"agent:${id}:9"`, 'rotate-key'))
      .resolves.toMatchObject({ replayed: false, secret_state: 'issued' });
    const replay = { agent, credential, previous_credential: null, replayed: true, secret_state: 'secret_unavailable' };
    const rotateReplay = vi.fn<typeof fetch>().mockResolvedValue(json(replay));
    const replayResult = await createAgentClient(rotateReplay).rotate(id, { current_password: 'p' }, auth, `"agent:${id}:9"`, 'rotate-key');
    expect(replayResult).toMatchObject({ replayed: true });
    expect(Object.hasOwn(replayResult, 'token')).toBe(false);
    for (const malformed of [
      { ...rotationFresh, previous_credential: { ...credential, id: replacementCredentialId } },
      { ...rotationFresh, previous_credential: { ...credential, agent_id: credentialId } },
      { ...rotationFresh, token: null },
      { ...replay, token: null }, { ...replay, token: '' },
      { ...replay, agent: { ...agent, id: credentialId } },
    ]) {
      const invalid = vi.fn<typeof fetch>().mockResolvedValue(json(malformed));
      await expect(createAgentClient(invalid).rotate(id, { current_password: 'p' }, auth, `"agent:${id}:9"`, 'same-key'))
        .rejects.toMatchObject({ kind: 'uncertain' });
      expect(invalid).toHaveBeenCalledOnce();
    }
  });

  it('keeps malformed and truncated mutation successes uncertain without exposing secrets', async () => {
    for (const result of [new Response('{"token":"private"', { status: 201 }), json({ ...issuance, agent: { ...agent, id: credentialId } }, 201)]) {
      const fetcher = vi.fn<typeof fetch>().mockResolvedValue(result);
      let failure: unknown;
      try { await createAgentClient(fetcher).create({ name: 'n', display_name: 'd' }, auth, 'same-key'); }
      catch (error) { failure = error; }
      expect(failure).toMatchObject({ kind: 'uncertain', code: 'invalid_response' });
      expect(String(failure)).not.toContain('private');
      expect(fetcher).toHaveBeenCalledOnce();
    }
  });

  it('sends rotate and revoke with the caller ETag, key, proof and exact paths', async () => {
    const rotateResult = { ...issuance, previous_credential: null };
    const rotate = vi.fn<typeof fetch>().mockResolvedValue(json(rotateResult, 200));
    const client = createAgentClient(rotate);
    await client.rotate(id, { current_password: 'proof', lifetime_seconds: 0, overlap_seconds: 0 }, auth, `"agent:${id}:7"`, 'rotate-key');
    const [rotateURL, rotateInit] = rotate.mock.calls[0];
    expect(rotateURL).toBe(`/api/v1/agents/${id}/credentials/rotate`);
    expect(new Headers(rotateInit?.headers).get('If-Match')).toBe(`"agent:${id}:7"`);
    expect(new Headers(rotateInit?.headers).get('Idempotency-Key')).toBe('rotate-key');
    expect(JSON.parse(String(rotateInit?.body))).toEqual({ current_password: 'proof', lifetime_seconds: 0, overlap_seconds: 0 });

    const lifecycle = { agent, credential: revokedCredential, changed: true, revoked_credential_count: '1', replayed: false };
    const revoke = vi.fn<typeof fetch>().mockResolvedValue(json(lifecycle));
    await createAgentClient(revoke).revoke(id, credentialId, { current_password: 'proof' }, auth, `"agent:${id}:7"`, 'revoke-key');
    expect(revoke.mock.calls[0][0]).toBe(`/api/v1/agents/${id}/credentials/${credentialId}/revoke`);
    const disabled = { ...lifecycle, agent: disabledAgent, credential: null, revoked_credential_count: '9223372036854775807' };
    expect((await createAgentClient(vi.fn<typeof fetch>().mockResolvedValue(json(disabled))).disable(
      id, { current_password: 'proof' }, auth, `"agent:${id}:7"`, 'disable-key',
    )).revoked_credential_count).toBe('9223372036854775807');
  });

  it('requires revoke and disable results to match their endpoint contracts', async () => {
    const base = { agent, credential: revokedCredential, changed: true, revoked_credential_count: '1', replayed: false };
    const valid = vi.fn<typeof fetch>().mockResolvedValue(json(base));
    await expect(createAgentClient(valid).revoke(id, credentialId, { current_password: 'p' }, auth, `"agent:${id}:3"`, 'k'))
      .resolves.toMatchObject({ credential: { id: credentialId, revoked_at: expect.any(String) } });
    for (const result of [
      { ...base, credential: null }, { ...base, credential: { ...revokedCredential, id: replacementCredentialId } },
      { ...base, credential: credential }, { ...base, revoked_credential_count: '0' },
    ]) {
      const invalid = vi.fn<typeof fetch>().mockResolvedValue(json(result));
      await expect(createAgentClient(invalid).revoke(id, credentialId, { current_password: 'p' }, auth, `"agent:${id}:3"`, 'k'))
        .rejects.toMatchObject({ kind: 'uncertain' });
      expect(invalid).toHaveBeenCalledOnce();
    }
    const noOpRevoke = vi.fn<typeof fetch>().mockResolvedValue(json({ ...base, changed: false, revoked_credential_count: '0' }));
    await expect(createAgentClient(noOpRevoke).revoke(id, credentialId, { current_password: 'p' }, auth, `"agent:${id}:3"`, 'k'))
      .resolves.toMatchObject({ changed: false });
    const disabled = { ...base, agent: disabledAgent, credential: null };
    const disableFetcher = vi.fn<typeof fetch>().mockResolvedValue(json(disabled));
    await expect(createAgentClient(disableFetcher).disable(id, { current_password: 'p' }, auth, `"agent:${id}:3"`, 'k'))
      .resolves.toMatchObject({ agent: { enabled: false }, credential: null });
    const disabledNoOp = { ...disabled, changed: false, revoked_credential_count: '0' };
    const noOpDisable = vi.fn<typeof fetch>().mockResolvedValue(json(disabledNoOp));
    await expect(createAgentClient(noOpDisable).disable(id, { current_password: 'p' }, auth, `"agent:${id}:3"`, 'k'))
      .resolves.toMatchObject({ changed: false, revoked_credential_count: '0' });
    for (const result of [base, { ...disabled, agent, credential: null }, { ...disabled, changed: false, revoked_credential_count: '1' }]) {
      const invalid = vi.fn<typeof fetch>().mockResolvedValue(json(result));
      await expect(createAgentClient(invalid).disable(id, { current_password: 'p' }, auth, `"agent:${id}:3"`, 'k'))
        .rejects.toMatchObject({ kind: 'uncertain' });
      expect(invalid).toHaveBeenCalledOnce();
    }
    const overflow = vi.fn<typeof fetch>().mockResolvedValue(json({ ...disabled, revoked_credential_count: '9223372036854775808' }));
    await expect(createAgentClient(overflow).disable(id, { current_password: 'p' }, auth, `"agent:${id}:3"`, 'k'))
      .rejects.toMatchObject({ kind: 'uncertain' });
  });

  it('validates nested IDs, nullable history fields and page bounds', async () => {
    const bad = vi.fn<typeof fetch>().mockResolvedValue(json({ items: [{ ...credential, agent_id: 'not-a-uuid' }], next_cursor: '' }));
    await expect(createAgentClient(bad).credentials(id)).rejects.toMatchObject({ code: 'invalid_response' });
    const tooMany = vi.fn<typeof fetch>().mockResolvedValue(json({ items: Array(201).fill(agent), next_cursor: '' }));
    await expect(createAgentClient(tooMany).list()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('accepts empty and maximum escaped display-name pages within the response cap', async () => {
    const empty = vi.fn<typeof fetch>().mockResolvedValue(json({ items: [], next_cursor: '' }));
    await expect(createAgentClient(empty).list()).resolves.toEqual({ items: [], next_cursor: '' });
    const escapedAgent = { ...agent, display_name: '\\'.repeat(256) };
    expect(escapedAgent.display_name.length).toBe(256);
    const maximum = vi.fn<typeof fetch>().mockResolvedValue(json({ items: Array(200).fill(escapedAgent), next_cursor: 'opaque' }));
    await expect(createAgentClient(maximum).list({ limit: 200 })).resolves.toMatchObject({ items: { length: 200 } });
    expect(maximum).toHaveBeenCalledWith('/api/v1/agents?limit=200', expect.anything());
  });

  it('validates credential history and requires explicit nullable members', async () => {
    const page = vi.fn<typeof fetch>().mockResolvedValue(json({ items: [credential], next_cursor: '' }));
    await expect(createAgentClient(page).credentials(id, { limit: 3, cursor: 'abc_DEF-123' })).resolves.toEqual({ items: [credential], next_cursor: '' });
    expect(page).toHaveBeenCalledWith(`/api/v1/agents/${id}/credentials?limit=3&cursor=abc_DEF-123`, expect.anything());
    for (const malformed of [{ ...credential, revoked_at: undefined }, { ...credential, retire_at: undefined }]) {
      const missing = vi.fn<typeof fetch>().mockResolvedValue(json({ items: [malformed], next_cursor: '' }));
      await expect(createAgentClient(missing).credentials(id)).rejects.toMatchObject({ code: 'invalid_response' });
    }
  });

  it('preserves exact version strings near 2^53 and int64 max while rejecting overflow', async () => {
    for (const version of ['9007199254740991', '9007199254740992', '9007199254740993', '9223372036854775807']) {
      const row = { ...agent, version };
      const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json(row, 200, { ETag: `"agent:${id}:${version}"` }));
      await expect(createAgentClient(fetcher).get(id)).resolves.toMatchObject({ agent: { version } });
    }
    for (const version of ['9223372036854775808', '01', '0']) {
      const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json({ ...agent, version }, 200, { ETag: `"agent:${id}:${version}"` }));
      await expect(createAgentClient(fetcher).get(id)).rejects.toMatchObject({ code: 'invalid_response' });
    }
  });

  it('uses Gregorian month lengths for years below 100 and rejects invalid dates', async () => {
    const early = { ...agent, created_at: '0000-02-29T00:00:00Z', updated_at: '0000-02-29T00:00:00Z' };
    const valid = vi.fn<typeof fetch>().mockResolvedValue(json({ items: [early], next_cursor: '' }));
    await expect(createAgentClient(valid).list()).resolves.toMatchObject({ items: [{ created_at: '0000-02-29T00:00:00Z' }] });
    const invalid = vi.fn<typeof fetch>().mockResolvedValue(json({ items: [{ ...early, created_at: '0001-02-29T00:00:00Z' }], next_cursor: '' }));
    await expect(createAgentClient(invalid).list()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('bounds opaque cursors to printable ASCII and returns invalid arguments as Promise rejections', async () => {
    const fetcher = vi.fn<typeof fetch>();
    await expect(createAgentClient(fetcher).list({ cursor: 'x'.repeat(513) })).rejects.toMatchObject({ kind: 'rejected' });
    await expect(createAgentClient(fetcher).credentials(id, { cursor: 'é' })).rejects.toMatchObject({ kind: 'rejected' });
    await expect(createAgentClient(fetcher).create({ name: 'n', display_name: 'd' }, auth, 'bad key'))
      .rejects.toMatchObject({ kind: 'rejected' });
    await expect(createAgentClient(fetcher).disable(id, { current_password: 'p' }, auth, 'W/"bad"', 'key'))
      .rejects.toMatchObject({ kind: 'rejected' });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('rejects invalid IDs and pre-aborted operations without dispatch', async () => {
    const fetcher = vi.fn<typeof fetch>();
    await expect(createAgentClient(fetcher).get('../bad')).rejects.toMatchObject({ kind: 'rejected', code: 'invalid_request' });
    const controller = new AbortController(); controller.abort();
    await expect(createAgentClient(fetcher).list({ signal: controller.signal })).rejects.toMatchObject({ kind: 'unavailable', code: 'cancelled' });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('cancels a slow response body when the caller aborts', async () => {
    let cancelled = false;
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new TextEncoder().encode('{"items":')); },
      cancel() { cancelled = true; },
    })));
    const controller = new AbortController();
    const pending = createAgentClient(fetcher).list({ signal: controller.signal });
    await Promise.resolve();
    controller.abort();
    await expect(pending).rejects.toMatchObject({ kind: 'unavailable' });
    expect(cancelled).toBe(true);
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('enforces the 30-second deadline while the fetch request is pending', async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn<typeof fetch>().mockImplementation(() => new Promise<Response>(() => undefined));
    const pending = createAgentClient(fetcher).list();
    await Promise.resolve();
    const signal = fetcher.mock.calls[0][1]?.signal as AbortSignal;
    const rejection = expect(pending).rejects.toMatchObject({ kind: 'unavailable', code: 'cancelled' });
    await vi.advanceTimersByTimeAsync(30_000);
    await rejection;
    expect(signal.aborted).toBe(true);
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('enforces the 30-second deadline while reading a response body', async () => {
    vi.useFakeTimers();
    let cancelled = false;
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(new ReadableStream<Uint8Array>({ cancel() { cancelled = true; } })));
    const pending = createAgentClient(fetcher).list();
    await Promise.resolve();
    const rejection = expect(pending).rejects.toMatchObject({ kind: 'unavailable', code: 'cancelled' });
    await vi.advanceTimersByTimeAsync(30_000);
    await rejection;
    expect(cancelled).toBe(true);
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('keeps mutation aborts uncertain and pre-aborted mutations undispatched', async () => {
    const preAborted = new AbortController(); preAborted.abort();
    const never = vi.fn<typeof fetch>();
    await expect(createAgentClient(never).disable(id, { current_password: 'p' }, auth, `"agent:${id}:1"`, 'k', { signal: preAborted.signal }))
      .rejects.toMatchObject({ kind: 'unavailable', code: 'cancelled' });
    expect(never).not.toHaveBeenCalled();

    let resolveFetch!: (response: Response) => void;
    const fetcher = vi.fn<typeof fetch>().mockImplementation(() => new Promise<Response>((resolve) => { resolveFetch = resolve; }));
    const controller = new AbortController();
    const pending = createAgentClient(fetcher).disable(id, { current_password: 'p' }, auth, `"agent:${id}:1"`, 'caller-key', { signal: controller.signal });
    await Promise.resolve();
    controller.abort();
    await expect(pending).rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
    expect((fetcher.mock.calls[0][1]?.signal as AbortSignal).aborted).toBe(true);
    expect(fetcher).toHaveBeenCalledOnce();
    resolveFetch(json({ private: 'secret' }));
  });

  it('disposes a late response after a dispatched mutation is aborted', async () => {
    let resolveFetch!: (response: Response) => void;
    let cancelled = false;
    const fetcher = vi.fn<typeof fetch>().mockImplementation(() => new Promise<Response>((resolve) => { resolveFetch = resolve; }));
    const controller = new AbortController();
    const pending = createAgentClient(fetcher).disable(id, { current_password: 'p' }, auth, `"agent:${id}:1"`, 'key', { signal: controller.signal });
    await Promise.resolve();
    controller.abort();
    await expect(pending).rejects.toMatchObject({ kind: 'uncertain' });
    resolveFetch(new Response(new ReadableStream<Uint8Array>({ cancel() { cancelled = true; } }), { status: 200 }));
    await Promise.resolve(); await Promise.resolve();
    expect(cancelled).toBe(true);
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it('treats oversized response bodies as invalid without exposing their contents', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(json({ items: [], next_cursor: '', padding: 'x'.repeat(512 * 1024) }));
    await expect(createAgentClient(fetcher).list()).rejects.toMatchObject({ kind: 'unavailable', code: 'invalid_response' });
  });

  it('trusts only documented conflict responses and validates retry guidance', async () => {
    const conflict = vi.fn<typeof fetch>().mockResolvedValue(json({ code: 'conflict', message: 'secret details', request_id: 'r', field_errors: [] }, 409));
    await expect(createAgentClient(conflict).disable(id, { current_password: 'secret' }, auth, `"agent:${id}:1"`, 'key'))
      .rejects.toMatchObject({ kind: 'rejected', code: 'conflict' });
    const malformed = vi.fn<typeof fetch>().mockResolvedValue(json({ code: 'conflict', message: 'secret details' }, 409));
    await expect(createAgentClient(malformed).disable(id, { current_password: 'secret' }, auth, `"agent:${id}:1"`, 'key'))
      .rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
    const throttled = vi.fn<typeof fetch>().mockResolvedValue(json({ code: 'rate_limited', message: 'secret details', request_id: 'r', field_errors: [] }, 429, { 'Retry-After': '42' }));
    let failure: unknown;
    try { await createAgentClient(throttled).disable(id, { current_password: 'secret' }, auth, `"agent:${id}:1"`, 'key'); }
    catch (error) { failure = error; }
    expect(failure).toMatchObject({ kind: 'rejected', code: 'rate_limited', retryAfterSeconds: 42 });
    expect(String(failure)).not.toContain('secret details');
  });

  it('treats all post-dispatch 503 responses and transport failures as uncertain exactly once', async () => {
    for (const response of [
      json({ code: 'management_unavailable', message: 'private detail', request_id: 'r', field_errors: [] }, 503),
      json({ code: 'management_unavailable', message: 'private detail' }, 503),
    ]) {
      const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response);
      const input = Object.freeze({ current_password: 'input-secret' });
      const promise = createAgentClient(fetcher).disable(id, input, auth, `"agent:${id}:11"`, 'original-key');
      await expect(promise).rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
      expect(fetcher).toHaveBeenCalledOnce();
      expect(input).toEqual({ current_password: 'input-secret' });
      expect(fetcher.mock.calls[0][1]).toMatchObject({ headers: expect.objectContaining({ 'If-Match': `"agent:${id}:11"`, 'Idempotency-Key': 'original-key' }) });
    }
    const transport = vi.fn<typeof fetch>().mockRejectedValue(new Error('transport input-secret private detail'));
    let failure: unknown;
    try { await createAgentClient(transport).disable(id, { current_password: 'input-secret' }, auth, `"agent:${id}:11"`, 'original-key'); }
    catch (error) { failure = error; }
    expect(failure).toMatchObject({ kind: 'uncertain' });
    expect(String(failure)).not.toMatch(/input-secret|private detail/);
    expect(transport).toHaveBeenCalledOnce();
  });

  it('keeps post-dispatch transport failures uncertain without retries', async () => {
    const fetcher = vi.fn<typeof fetch>().mockRejectedValue(new Error('token-secret transport detail'));
    await expect(createAgentClient(fetcher).disable(id, { current_password: 'password-secret' }, auth, `"agent:${id}:1"`, 'key'))
      .rejects.toMatchObject({ kind: 'uncertain', code: 'outcome_unknown' });
    expect(fetcher).toHaveBeenCalledOnce();
  });
});
