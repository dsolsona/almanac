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
  it('reads a redacted datasource and validates its canonical version ETag', async () => {
    const fetcher = vi.fn().mockResolvedValue(response(source(), 200, { ETag: `"datasource:${id}:4"` }));
    vi.stubGlobal('fetch', fetcher);
    const result = await createDatasourceClient().get(id);
    expect(result.datasource.authentication.token_configured).toBe(true);
    expect(JSON.stringify(result)).not.toContain('secret-value');
    expect(fetcher.mock.calls[0]?.[1]).toMatchObject({ credentials: 'same-origin', cache: 'no-store', redirect: 'error' });
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
    await expect(createDatasourceClient().connectionTest(input, context)).rejects.toMatchObject({ code: 'configuration_pending', status: 503, uncertain: false, message: 'The agent is applying the latest datasource settings. No diagnostic was started; wait for readiness and retry.' });
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
