import { afterEach, describe, expect, it, vi } from 'vitest';
import { createDatasourceClient } from '../src/datasources/client.ts';
import type { AuthContext } from '../src/auth/client.ts';

const context: AuthContext = { csrf_token: 'csrf-token', session: { user_id: 'user-1', login: 'owner', role: 'administrator', restricted: false } };
const id = '8ed93d7e-51e3-4a3e-9222-3e8a612de20a';
const agent = 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c';
function source() { return { id, name: 'metrics-prod', display_name: 'Production metrics', version: '4', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test', tls: { insecure_skip_verify: false, ca_pem: null }, authentication: { mode: 'bearer', username: null, password_configured: false, token_configured: true }, polling_interval_seconds: 60, settling_delay_seconds: 60, agent_id: agent, created_at: '2026-10-20T12:00:00Z', updated_at: '2026-10-21T12:00:00Z' }; }
function response(body: unknown, status = 200, headers: Record<string, string> = {}) { return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } }); }

afterEach(() => vi.unstubAllGlobals());

describe('datasource client', () => {
  it('treats configuration pending as a definite rejected move', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ code: 'configuration_pending' }, 503)));
    const input = { name: 'metrics-prod', display_name: 'Production metrics', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test', tls: { insecure_skip_verify: false, ca_pem: null }, authentication: { mode: 'none', username: null, password_secret: { action: 'clear' }, token_secret: { action: 'clear' } }, polling_interval_seconds: 60, agent_id: agent } as const;
    await expect(createDatasourceClient().save(input, context, 'key', `"datasource:${id}:4"`)).rejects.toMatchObject({ code: 'configuration_pending', status: 503, uncertain: false, message: 'The agent is applying configuration. Wait until it is ready and try again.' });
  });
  it.each([['reassignment_pending', 409, 'This agent is completing a datasource move. Wait for it to finish or cancel the move before changing collection settings.'], ['reassignment_settings_conflict', 422, 'Save connection changes before changing the assigned agent.']])('explains %s', async (code, status, message) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ code }, status)));
    await expect(createDatasourceClient().get(id)).rejects.toMatchObject({ code, status, message });
  });
  it('accepts a bounded pending reassignment and rejects mismatched owners', async () => {
    const reassignment = { source_agent_id: agent, target_agent_id: id, requested_at: '2026-10-22T12:00:00Z', authority_expires_at: '2026-10-22T13:00:00Z', state: 'waiting_for_previous_agent' };
    const fetcher = vi.fn().mockResolvedValue(response({ ...source(), reassignment }, 200, { ETag: `"datasource:${id}:4"` })); vi.stubGlobal('fetch', fetcher);
    await expect(createDatasourceClient().get(id)).resolves.toMatchObject({ datasource: { agent_id: agent, reassignment } });
    fetcher.mockResolvedValue(response({ ...source(), reassignment: { ...reassignment, source_agent_id: id } }, 200, { ETag: `"datasource:${id}:4"` }));
    await expect(createDatasourceClient().get(id)).rejects.toMatchObject({ code: 'invalid_response' });
    fetcher.mockResolvedValue(response({ ...source(), reassignment: { ...reassignment, state: ['waiting_for_previous_agent'] } }, 200, { ETag: `"datasource:${id}:4"` }));
    await expect(createDatasourceClient().get(id)).rejects.toMatchObject({ code: 'invalid_response' });
  });
  it('reads a redacted datasource and validates its canonical version ETag', async () => {
    const fetcher = vi.fn().mockResolvedValue(response(source(), 200, { ETag: `"datasource:${id}:4"` }));
    vi.stubGlobal('fetch', fetcher);
    const result = await createDatasourceClient().get(id);
    expect(result.datasource.authentication.token_configured).toBe(true);
    expect(JSON.stringify(result)).not.toContain('secret-value');
    expect(fetcher.mock.calls[0]?.[1]).toMatchObject({ credentials: 'same-origin', cache: 'no-store', redirect: 'error' });
  });

  it('accepts ordinary datasource names and metric labels containing token or password', async () => {
    const named = { ...source(), name: 'token' };
    const diagnostic = { id, state: 'succeeded', operation: 'query_preview', created_at: '2026-10-20T12:00:00Z', deadline_at: '2026-10-20T12:00:30Z', result_class: 'complete', result: { diagnostic_id: id, state: 'succeeded', result_class: 'complete', series: [{ labels: { token: 'ordinary label', password: 'ordinary label' }, time: '1700000000.000', value: '1' }] }, error_code: null, message: null };
    const fetcher = vi.fn().mockResolvedValueOnce(response(named, 200, { ETag: `"datasource:${id}:4"` })).mockResolvedValueOnce(response(diagnostic));
    vi.stubGlobal('fetch', fetcher);
    await expect(createDatasourceClient().get(id)).resolves.toMatchObject({ datasource: { name: 'token' } });
    await expect(createDatasourceClient().diagnostic(id)).resolves.toMatchObject({ result: { series: [{ labels: { token: 'ordinary label', password: 'ordinary label' } }] } });
  });

  it('rejects secret fields outside the declared redacted response shape', async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ ...source(), token: 'private-token' }, 200, { ETag: `"datasource:${id}:4"` }));
    vi.stubGlobal('fetch', fetcher);
    await expect(createDatasourceClient().get(id)).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('sends explicit secret actions with CSRF and idempotency while returning no submitted secret', async () => {
    const fetcher = vi.fn().mockResolvedValue(response(source(), 201, { ETag: `"datasource:${id}:4"` }));
    vi.stubGlobal('fetch', fetcher);
    const input = { name: 'metrics-prod', display_name: 'Production metrics', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test', tls: { insecure_skip_verify: false, ca_pem: null }, authentication: { mode: 'bearer', username: null, password_secret: { action: 'clear' }, token_secret: { action: 'replace', value: 'private-token' } }, polling_interval_seconds: 60, agent_id: agent } as const;
    const result = await createDatasourceClient().save(input, context, 'request-1');
    expect(result.authentication.token_configured).toBe(true);
    const init = fetcher.mock.calls[0]?.[1];
    expect(new Headers(init?.headers).get('X-CSRF-Token')).toBe('csrf-token');
    expect(new Headers(init?.headers).get('Idempotency-Key')).toBe('request-1');
    expect(JSON.parse(String(init?.body)).authentication.token_secret).toEqual({ action: 'replace', value: 'private-token' });
  });

  it('replaces backend detail with a safe message and never reads secret error text', async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ code: 'management_unavailable', message: 'bearer token is private', request_id: 'abc' }, 503));
    vi.stubGlobal('fetch', fetcher);
    await expect(createDatasourceClient().list()).rejects.toThrow('Datasource management is temporarily unavailable.');
  });

  it('validates readiness revisions while allowing an unapplied zero revision', async () => {
    const descriptor = { provider: 'prometheus', configuration_format: 'prometheus-http-v1', operations: ['connection_test', 'query_preview'], availability: 'unreported', desired_configuration_revision: null, applied_configuration_revision: '0', configuration_failure_code: null, configuration_failure_revision: null };
    const fetcher = vi.fn().mockResolvedValue(response({ items: [descriptor] }));
    vi.stubGlobal('fetch', fetcher);
    await expect(createDatasourceClient().providers(agent)).resolves.toEqual([descriptor]);

    fetcher.mockResolvedValue(response({ items: [{ ...descriptor, desired_configuration_revision: '03' }] }));
    await expect(createDatasourceClient().providers(agent)).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('preserves a definite configuration-pending response instead of reporting an unknown diagnostic outcome', async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ code: 'configuration_pending', message: 'untrusted backend detail' }, 503));
    vi.stubGlobal('fetch', fetcher);
    const input = { provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test', tls: { insecure_skip_verify: false, ca_pem: null }, authentication: { mode: 'none', username: null, password_secret: { action: 'clear' }, token_secret: { action: 'clear' } }, agent_id: agent } as const;
    await expect(createDatasourceClient().connectionTest(input, context)).rejects.toMatchObject({ code: 'configuration_pending', status: 503, uncertain: false, message: 'The agent is applying configuration. Wait until it is ready and try again.' });
  });

  it('tests stored datasource credentials by ID and exact version without sending settings or secrets', async () => {
    const diagnostic = { id, state: 'pending', operation: 'connection_test', created_at: '2026-10-27T12:00:00Z', deadline_at: '2026-10-27T12:00:30Z', result_class: null, result: null, error_code: null, message: null };
    const fetcher = vi.fn().mockResolvedValue(response(diagnostic, 202));
    vi.stubGlobal('fetch', fetcher);
    await expect(createDatasourceClient().savedConnectionTest(id, `"datasource:${id}:4"`, context, 'test-key')).resolves.toMatchObject({ id, state: 'pending' });
    expect(fetcher.mock.calls[0]?.[0]).toBe(`/api/v1/datasources/${id}/connection-tests`);
    const init = fetcher.mock.calls[0]?.[1];
    expect(new Headers(init?.headers).get('If-Match')).toBe(`"datasource:${id}:4"`);
    expect(new Headers(init?.headers).get('Idempotency-Key')).toBe('test-key');
    expect(new Headers(init?.headers).get('X-CSRF-Token')).toBe('csrf-token');
    expect(init?.body).toBe('{}');
  });

  it('keeps a guaranteed no-job saved diagnostic failure definite', async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ code: 'configuration_pending', message: 'private detail' }, 503));
    vi.stubGlobal('fetch', fetcher);
    await expect(createDatasourceClient().savedConnectionTest(id, `"datasource:${id}:4"`, context, 'test-key')).rejects.toMatchObject({ code: 'configuration_pending', uncertain: false });
  });

  it('allows a bounded large diagnostic receipt while keeping the ordinary response budget separate', async () => {
    const labels = Object.fromEntries(Array.from({ length: 32 }, (_, index) => [`label_${String(index).padStart(2, '0')}`, 'x'.repeat(256)]));
    const series = Array.from({ length: 20 }, () => ({ labels, time: '1700000000.000', value: '1' }));
    const diagnostic = { id, state: 'succeeded', operation: 'query_preview', created_at: '2026-10-20T12:00:00Z', deadline_at: '2026-10-20T12:00:30Z', result_class: 'complete', result: { diagnostic_id: id, state: 'succeeded', result_class: 'complete', series }, error_code: null, message: null };
    const fetcher = vi.fn().mockResolvedValue(response(diagnostic));
    vi.stubGlobal('fetch', fetcher);
    await expect(createDatasourceClient().diagnostic(id)).resolves.toMatchObject({ id, state: 'succeeded' });
    fetcher.mockResolvedValue(response({ ...diagnostic, result: { payload: 'x'.repeat(2 * 1024 * 1024) } }));
    await expect(createDatasourceClient().diagnostic(id)).rejects.toMatchObject({ code: 'invalid_response' });
  });
});
