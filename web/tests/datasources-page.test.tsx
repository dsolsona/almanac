import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DatasourcesPage } from '../src/datasources/DatasourcesPage.tsx';
import type { AuthContext } from '../src/auth/client.ts';

const admin: AuthContext = { csrf_token: 'csrf-admin', session: { user_id: 'user-1', login: 'owner', role: 'administrator', restricted: false } };
const editor: AuthContext = { csrf_token: 'csrf-editor', session: { user_id: 'user-3', login: 'editor', role: 'editor', restricted: false } };
const viewer: AuthContext = { csrf_token: 'csrf-viewer', session: { user_id: 'user-2', login: 'reader', role: 'viewer', restricted: false } };
const sourceID = '8ed93d7e-51e3-4a3e-9222-3e8a612de20a';
const agentID = 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c';
function source() { return { id: sourceID, name: 'metrics-prod', display_name: 'Production metrics', version: '4', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test', tls: { insecure_skip_verify: false, ca_pem: null }, authentication: { mode: 'bearer', username: null, password_configured: false, token_configured: true }, polling_interval_seconds: 60, settling_delay_seconds: 60, agent_id: agentID, created_at: '2026-10-20T12:00:00Z', updated_at: '2026-10-21T12:00:00Z' }; }
function response(body: unknown, status = 200, headers: Record<string, string> = {}) { return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } }); }
function installFetch(agentItems: Record<string, unknown>[] = [{ id: agentID, name: 'west', display_name: 'West collector', enabled: true, runtime: { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test', 'query_preview'] }] } }], providerAvailability: string[] = ['available'], previewStatuses: number[] = []) {
  const calls: { url: string; init?: RequestInit }[] = [];
  let providerRequest = 0;
  let previewRequest = 0;
  const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input); calls.push({ url, init });
    if (url.startsWith('/api/v1/agents')) return response({ items: agentItems, next_cursor: '' });
    if (url.startsWith('/api/v1/providers')) {
      const availability = providerAvailability[Math.min(providerRequest++, providerAvailability.length - 1)];
      return response({ items: [{ provider: 'prometheus', configuration_format: 'prometheus-http-v1', operations: ['connection_test', 'query_preview'], availability,
        desired_configuration_revision: '3', applied_configuration_revision: availability === 'applying' ? '2' : '3', configuration_failure_code: null, configuration_failure_revision: null }] });
    }
    if (url === `/api/v1/datasources/${sourceID}` && init?.method === 'PUT') return response({ ...source(), version: '5' }, 200, { ETag: `"datasource:${sourceID}:5"` });
    if (url === `/api/v1/datasources/${sourceID}`) return response(source(), 200, { ETag: `"datasource:${sourceID}:4"` });
    if (url === `/api/v1/datasources/${sourceID}/query-previews` && init?.method === 'POST') {
      const status = previewStatuses[Math.min(previewRequest++, previewStatuses.length - 1)] ?? 500;
      return status === 503 ? response({ code: 'configuration_pending', message: 'safe contract response' }, status) : response({
        id: 'e5267bc2-3f9c-4bf2-a6c4-5ea2fa3fc6f4', state: 'succeeded', operation: 'query_preview', created_at: '2026-10-22T12:00:00Z', deadline_at: '2026-10-22T12:00:30Z', result_class: 'complete', result: { series: [] }, error_code: null, message: null,
      }, status);
    }
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

  it('distinguishes unreported, unsupported, disabled, and offline agents without enabling unsupported assignments', async () => {
    const offlineID = '2e8fa70c-94dc-4cff-b15b-0953521e8ea6';
    history.replaceState({}, '', '/datasources/new');
    installFetch([
      { id: '63b8d999-b8e1-462b-8b79-6e95fb86ae2d', name: 'new', display_name: 'Never connected', enabled: true },
      { id: '413c216e-e907-4d2d-856f-b4c8dd391e4e', name: 'unsupported', display_name: 'Unsupported', enabled: true, runtime: { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test'] }] } },
      { id: '20d48b8e-74b8-460f-9a10-99787de85c49', name: 'disabled', display_name: 'Disabled', enabled: false, runtime: { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test', 'query_preview'] }] } },
      { id: offlineID, name: 'offline', display_name: 'Offline capable', enabled: true, runtime: { reachability: 'unreachable', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test', 'query_preview'] }] } },
    ]);
    render(<DatasourcesPage context={admin} />);

    const assignment = await screen.findByLabelText('Assigned agent');
    expect(screen.getByRole('option', { name: 'Never connected · capabilities not reported' })).toBeDisabled();
    expect(screen.getByRole('option', { name: 'Unsupported · diagnostics unsupported' })).toBeDisabled();
    expect(screen.getByRole('option', { name: 'Disabled · disabled' })).toBeDisabled();
    expect(screen.getByRole('option', { name: 'Offline capable · offline' })).not.toBeDisabled();
    expect(assignment).toHaveValue(offlineID);
    expect(screen.getByText('The assigned agent is offline. Diagnostics require a connected agent.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Test connection' })).toBeDisabled();
  });

  it('keeps editor preview disabled while the saved datasource configuration is applying, then enables it when ready', async () => {
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const calls = installFetch([], ['applying', 'available']);
    render(<DatasourcesPage context={editor} />);

    const preview = await screen.findByRole('button', { name: 'Run preview' });
    expect(screen.getByText('The assigned agent is applying datasource settings. Preview will be available when it finishes.')).toBeVisible();
    expect(preview).toBeDisabled();
    await waitFor(() => expect(preview).not.toBeDisabled(), { timeout: 5000 });
    expect(screen.queryByText('The assigned agent is applying datasource settings. Preview will be available when it finishes.')).not.toBeInTheDocument();
    expect(calls.some((call) => call.url.startsWith('/api/v1/agents'))).toBe(false);
  });

  it('invalidates readiness after an administrator saves and waits before enabling preview', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const calls = installFetch(undefined, ['available', 'applying', 'available']);
    render(<DatasourcesPage context={admin} />);

    await user.clear(await screen.findByLabelText('Display name'));
    await user.type(screen.getByLabelText('Display name'), 'Updated metrics');
    await user.click(screen.getByRole('button', { name: 'Save settings' }));
    expect(await screen.findByText('Datasource settings saved.')).toBeVisible();
    const preview = screen.getByRole('button', { name: 'Run preview' });
    expect(await screen.findByText('The assigned agent is applying datasource settings. Preview will be available when it finishes.')).toBeVisible();
    expect(preview).toBeDisabled();
    await waitFor(() => expect(preview).not.toBeDisabled(), { timeout: 5000 });
    expect(calls.some((call) => call.url === `/api/v1/datasources/${sourceID}` && call.init?.method === 'PUT')).toBe(true);
  });

  it('waits for readiness and retries a preview when admission confirms no job was created', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const calls = installFetch([], ['available', 'applying', 'available'], [503, 202]);
    render(<DatasourcesPage context={editor} />);
    const query = await screen.findByLabelText('PromQL query');
    await user.clear(query);
    await user.type(query, 'up');
    const preview = await screen.findByRole('button', { name: 'Run preview' });
    await waitFor(() => expect(preview).not.toBeDisabled());
    await user.click(preview);
    expect(await screen.findByText('No preview was started. The agent is applying datasource settings; waiting briefly before retrying this query.')).toBeVisible();
    await waitFor(() => expect(calls.filter((call) => call.url === `/api/v1/datasources/${sourceID}/query-previews` && call.init?.method === 'POST')).toHaveLength(2), { timeout: 5000 });
    expect(await screen.findByText('The diagnostic completed.')).toBeVisible();
    const previews = calls.filter((call) => call.url === `/api/v1/datasources/${sourceID}/query-previews` && call.init?.method === 'POST');
    expect(previews.map((call) => JSON.parse(String(call.init?.body)).query)).toEqual(['up', 'up']);
    expect(screen.queryByText(/outcome is unknown/i)).not.toBeInTheDocument();
  });
});
