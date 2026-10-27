import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DatasourcesPage } from '../src/datasources/DatasourcesPage.tsx';
import type { AuthContext } from '../src/auth/client.ts';

const admin: AuthContext = { csrf_token: 'csrf-admin', session: { user_id: 'user-1', login: 'owner', role: 'administrator', restricted: false } };
const viewer: AuthContext = { csrf_token: 'csrf-viewer', session: { user_id: 'user-2', login: 'reader', role: 'viewer', restricted: false } };
const sourceID = '8ed93d7e-51e3-4a3e-9222-3e8a612de20a';
const agentID = 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c';
function source() { return { id: sourceID, name: 'metrics-prod', display_name: 'Production metrics', version: '4', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test', tls: { insecure_skip_verify: false, ca_pem: null }, authentication: { mode: 'bearer', username: null, password_configured: false, token_configured: true }, polling_interval_seconds: 60, settling_delay_seconds: 60, agent_id: agentID, created_at: '2026-10-20T12:00:00Z', updated_at: '2026-10-21T12:00:00Z' }; }
function response(body: unknown, status = 200, headers: Record<string, string> = {}) { return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } }); }
function installFetch() {
  const calls: { url: string; init?: RequestInit }[] = [];
  const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input); calls.push({ url, init });
    if (url.startsWith('/api/v1/agents')) return response({ items: [{ id: agentID, name: 'west', display_name: 'West collector', enabled: true, runtime: { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test', 'query_preview'] }] } }], next_cursor: '' });
    if (url.startsWith('/api/v1/providers')) return response({ items: [{ provider: 'prometheus', configuration_format: 'prometheus-http-v1', operations: ['connection_test', 'query_preview'], availability: 'available' }] });
    if (url.startsWith('/api/v1/datasources') && init?.method === 'POST') return response(source(), 201, { ETag: `"datasource:${sourceID}:4"` });
    return response({ items: [source()], next_cursor: '' });
  });
  vi.stubGlobal('fetch', fetcher);
  return calls;
}

afterEach(() => { cleanup(); history.replaceState({}, '', '/'); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe('datasource page', () => {
  it('keeps viewer reads redacted and does not request administrator-only agent assignment data', async () => {
    history.replaceState({}, '', '/datasources');
    const calls = installFetch();
    render(<DatasourcesPage context={viewer} />);
    expect(await screen.findByText('Production metrics')).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Add datasource' })).not.toBeInTheDocument();
    expect(calls.some((call) => call.url.startsWith('/api/v1/agents'))).toBe(false);
    expect(screen.getByText('bearer · secret configured')).toBeVisible();
  });

  it('creates with an explicit secret replacement and never leaves the submitted secret in the page', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources/new');
    const calls = installFetch();
    render(<DatasourcesPage context={admin} />);
    await user.type(await screen.findByLabelText('Name'), 'metrics-prod');
    await user.type(screen.getByLabelText('Display name'), 'Production metrics');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'https://metrics.example.test');
    await user.selectOptions(screen.getByLabelText('Authentication'), 'bearer');
    await user.selectOptions(screen.getByLabelText('Bearer token secret'), 'replace');
    await user.type(screen.getByLabelText('Bearer token value'), 'private-token-value');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    expect(await screen.findByText('Production metrics')).toBeVisible();
    const create = calls.find((call) => call.url === '/api/v1/datasources' && call.init?.method === 'POST');
    expect(JSON.parse(String(create?.init?.body)).authentication.token_secret).toEqual({ action: 'replace', value: 'private-token-value' });
    expect(document.body.textContent).not.toContain('private-token-value');
    expect(calls.some((call) => call.url.includes('private-token-value'))).toBe(false);
    for (let index = 0; index < localStorage.length; index += 1) {
      const key = localStorage.key(index);
      expect(key).not.toContain('private-token-value');
      expect(localStorage.getItem(key ?? '')).not.toContain('private-token-value');
    }
  });
});
