import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
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
function installFetch(agentItems: Record<string, unknown>[] = [{ id: agentID, name: 'west', display_name: 'West collector', enabled: true, runtime: { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test', 'query_preview'] }] } }], providerAvailability: string[] = ['available'], previewStatuses: number[] = [], previewResult: Record<string, unknown> = {
	id: 'e5267bc2-3f9c-4bf2-a6c4-5ea2fa3fc6f4', state: 'succeeded', operation: 'query_preview', created_at: '2026-10-22T12:00:00Z', deadline_at: '2026-10-22T12:00:30Z', result_class: 'complete', result: { diagnostic_id: 'e5267bc2-3f9c-4bf2-a6c4-5ea2fa3fc6f4', state: 'succeeded', result_class: 'complete', series: [] }, error_code: null, message: null,
}, createStatuses: number[] = [], cancelStatus = 200, diagnosticResults: Record<string, unknown>[] = []) {
  const calls: { url: string; init?: RequestInit }[] = [];
  let providerRequest = 0;
  let previewRequest = 0;
  let createRequest = 0;
  let diagnosticRequest = 0;
  const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input); calls.push({ url, init });
    if (url.startsWith('/api/v1/agents')) return response({ items: agentItems, next_cursor: '' });
    if (url === `/api/v1/datasources/${sourceID}/connection-tests` && init?.method === 'POST') return response(previewResult, 202);
    if (url.startsWith('/api/v1/providers')) {
      const availability = providerAvailability[Math.min(providerRequest++, providerAvailability.length - 1)];
      return response({ items: [{ provider: 'prometheus', configuration_format: 'prometheus-http-v1', operations: ['connection_test', 'query_preview'], availability,
        desired_configuration_revision: '3', applied_configuration_revision: availability === 'applying' ? '2' : '3', configuration_failure_code: null, configuration_failure_revision: null }] });
    }
    if (url === `/api/v1/datasources/${sourceID}` && init?.method === 'PUT') return response({ ...source(), version: '5' }, 200, { ETag: `"datasource:${sourceID}:5"` });
    if (url === `/api/v1/datasources/${sourceID}`) return response(source(), 200, { ETag: `"datasource:${sourceID}:4"` });
    if (url === `/api/v1/datasources/${sourceID}/query-previews` && init?.method === 'POST') {
      const status = previewStatuses[Math.min(previewRequest++, previewStatuses.length - 1)] ?? 500;
      return status === 503 ? response({ code: 'configuration_pending', message: 'safe contract response' }, status) : response(previewResult, status);
    }
    if (url.endsWith('/cancel') && init?.method === 'POST') return cancelStatus === 200 ? response({ ...previewResult, state: 'canceled', result: null }) : response({ code: 'management_unavailable', message: 'safe failure' }, cancelStatus);
    if (url.startsWith('/api/v1/diagnostics/')) return response(diagnosticResults[Math.min(diagnosticRequest++, diagnosticResults.length - 1)] ?? previewResult);
    if (url.startsWith('/api/v1/datasources') && init?.method === 'POST') { const status = createStatuses[Math.min(createRequest++, createStatuses.length - 1)] ?? 201; return response(status === 201 ? source() : { code: 'management_unavailable', message: 'safe failure' }, status, { ETag: `"datasource:${sourceID}:4"` }); }
    return response({ items: [source()], next_cursor: '' });
  });
  vi.stubGlobal('fetch', fetcher);
  return calls;
}

afterEach(() => { cleanup(); history.replaceState({}, '', '/'); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe('datasource page', () => {
  it.each([false, true])('clears Updating and enables diagnostics after acknowledgement (initially offline: %s)', async (initiallyOffline) => {
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const runtime = { reachability: initiallyOffline ? 'unreachable' : 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test', 'query_preview'] }] };
    const agent = { id: agentID, name: 'west', display_name: 'West collector', enabled: true, runtime };
    const calls = installFetch([agent]);
    const delegate = globalThis.fetch;
    let reads = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input) === `/api/v1/datasources/${sourceID}` && init?.method === 'GET') {
        reads += 1;
        return response({ ...source(), ...(reads === 1 ? { reassignment: { source_agent_id: 'a6ad8995-d902-41b8-886c-bb7d5897104a', target_agent_id: agentID, requested_at: '2026-10-22T12:00:00Z', authority_expires_at: '2026-10-22T12:00:00Z', state: 'waiting_for_configuration' } } : {}) }, 200, { ETag: `"datasource:${sourceID}:4"` });
      }
      if (String(input).startsWith('/api/v1/agents') && reads > 1) return response({ items: [{ ...agent, runtime: { ...runtime, reachability: 'connected' } }], next_cursor: '' });
      return delegate(input, init);
    }));
    render(<DatasourcesPage context={admin} />);
    expect(await screen.findByRole('heading', { name: 'Updating datasource' })).toBeVisible();
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Updating datasource' })).not.toBeInTheDocument(), { timeout: 2500 });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Run preview' })).toBeEnabled());
    expect(screen.getByRole('button', { name: 'Test connection' })).toBeEnabled();
    expect(document.querySelector('.ds-head__status')).toHaveTextContent('Active');
    expect(reads).toBe(2);
    expect(calls.some((call) => call.url.startsWith('/api/v1/providers'))).toBe(true);
  });

  it('keeps a newer agent choice when a pending status request finishes late', async () => {
    const user = userEvent.setup(); history.replaceState({}, '', `/datasources/${sourceID}`);
    const target = 'a6ad8995-d902-41b8-886c-bb7d5897104a';
    const runtime = { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test', 'query_preview'] }] };
    installFetch([{ id: agentID, display_name: 'West collector', enabled: true, runtime }, { id: target, display_name: 'Replacement', enabled: true, runtime }]);
    const delegate = globalThis.fetch;
    let reads = 0;
    let finish!: (value: Response) => void;
    let statusSignal: AbortSignal | undefined;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input) === `/api/v1/datasources/${sourceID}` && init?.method === 'GET') {
        if (++reads > 1) { statusSignal = init.signal ?? undefined; return new Promise<Response>((resolve) => { finish = resolve; }); }
        return response({ ...source(), reassignment: { source_agent_id: target, target_agent_id: agentID, requested_at: '2026-10-22T12:00:00Z', authority_expires_at: '2026-10-22T12:00:00Z', state: 'waiting_for_configuration' } }, 200, { ETag: `"datasource:${sourceID}:4"` });
      }
      return delegate(input, init);
    }));
    render(<DatasourcesPage context={admin} />);
    await screen.findByRole('heading', { name: 'Updating datasource' });
    await waitFor(() => expect(reads).toBe(2), { timeout: 2500 });
    await user.click(within(screen.getByRole('region', { name: 'Agent' })).getByRole('button', { name: 'Change' }));
    await user.click(screen.getByLabelText('Assigned agent'));
    await user.click(screen.getByRole('option', { name: /Replacement/ }));
    expect(statusSignal?.aborted).toBe(true);
    await act(async () => { finish(response(source(), 200, { ETag: `"datasource:${sourceID}:4"` })); });
    expect(screen.getByRole('heading', { name: 'Updating datasource' })).toBeVisible();
    expect(screen.getByRole('region', { name: 'Agent' })).toHaveTextContent('Replacement');
    expect(screen.getByRole('button', { name: 'Save settings' })).toBeEnabled();
  });

  it('refreshes a completed assignment when cancellation loses the race to acknowledgement', async () => {
    const user = userEvent.setup(); history.replaceState({}, '', `/datasources/${sourceID}`);
    const target = 'a6ad8995-d902-41b8-886c-bb7d5897104a';
    installFetch(); const delegate = globalThis.fetch;
    let canceled = false;
    let cancelBody: Record<string, unknown> = {};
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input) === `/api/v1/datasources/${sourceID}`) {
        if (init?.method === 'PUT') {
          cancelBody = JSON.parse(String(init.body)); canceled = true;
          return response({ code: 'conflict', message: 'completed before cancellation' }, 409);
        }
        return response({ ...source(), agent_id: target, ...(!canceled ? { reassignment: { source_agent_id: agentID, target_agent_id: target, requested_at: '2026-10-22T12:00:00Z', authority_expires_at: '2026-10-22T12:00:00Z', state: 'waiting_for_configuration' } } : {}) }, 200, { ETag: `"datasource:${sourceID}:4"` });
      }
      return delegate(input, init);
    }));
    render(<DatasourcesPage context={admin} />);
    await user.click(await screen.findByRole('button', { name: 'Move back to previous agent' }));
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Updating datasource' })).not.toBeInTheDocument());
    expect(cancelBody.cancel_reassignment).toBe(true);
    expect(screen.queryByRole('button', { name: 'Move back to previous agent' })).not.toBeInTheDocument();
    expect(screen.getByText('The move completed before cancellation. Showing the current assignment.')).toBeVisible();
  });

  it.each(['waiting_for_configuration', 'waiting_for_collection', 'replacement_unavailable'])('lets an editor choose another agent during %s without editing connection settings', async (state) => {
    const user = userEvent.setup(); history.replaceState({}, '', `/datasources/${sourceID}`);
    const target = 'a6ad8995-d902-41b8-886c-bb7d5897104a';
    const runtime = { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['collect', 'connection_test', 'query_preview'] }] };
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.startsWith('/api/v1/agents')) return response({ items: [{ id: agentID, display_name: 'West collector', enabled: true, runtime }, { id: target, display_name: 'Replacement', enabled: true, runtime }], next_cursor: '' });
      if (path.startsWith('/api/v1/providers') || path.startsWith('/api/v1/slis')) return response({ items: [], next_cursor: '' });
      if (init?.method === 'PUT') return response({ ...source(), version: '5' }, 200, { ETag: `"datasource:${sourceID}:5"` });
      return response({ ...source(), agent_id: target, reassignment: { source_agent_id: agentID, target_agent_id: target, requested_at: '2026-10-22T12:00:00Z', authority_expires_at: '2026-10-22T12:00:00Z', state } }, 200, { ETag: `"datasource:${sourceID}:4"` });
    });
    vi.stubGlobal('fetch', fetcher); render(<DatasourcesPage context={editor} />);
    expect(await screen.findByRole('heading', { name: state === 'replacement_unavailable' ? 'Agent unavailable' : 'Updating datasource' })).toBeVisible();
    if (state === 'replacement_unavailable') {
      expect(document.querySelector('.ds-head__status')).toHaveTextContent('Not collecting');
      expect(screen.queryByRole('heading', { name: 'Updating datasource' })).not.toBeInTheDocument();
    } else expect(screen.getByText('Waiting for Replacement to apply its configuration.')).toBeVisible();
    expect(screen.queryByText(/waiting until/i)).not.toBeInTheDocument();
    expect(screen.getByLabelText('Prometheus endpoint')).toBeDisabled();
    await user.click(within(screen.getByRole('region', { name: 'Agent' })).getByRole('button', { name: 'Change' }));
    await user.click(screen.getByLabelText('Assigned agent'));
    await user.click(screen.getByRole('option', { name: /West collector/ }));
    await user.click(screen.getByRole('button', { name: 'Save settings' }));
    await waitFor(() => expect(fetcher.mock.calls.some(([, init]) => init?.method === 'PUT')).toBe(true));
    const request = fetcher.mock.calls.find(([, init]) => init?.method === 'PUT')?.[1];
    expect(JSON.parse(String(request?.body))).toMatchObject({ agent_id: agentID, endpoint: source().endpoint, authentication: { token_secret: { action: 'retain' } } });
  });

  it.each([true, false])('reconciles an uncertain cancellation when pending is %s', async (stillPending) => {
    const user = userEvent.setup(); history.replaceState({}, '', `/datasources/${sourceID}`);
    const target = 'a6ad8995-d902-41b8-886c-bb7d5897104a';
    const reassignment = { source_agent_id: agentID, target_agent_id: target, requested_at: '2026-10-22T12:00:00Z', authority_expires_at: '2026-10-22T13:00:00Z', state: 'waiting_for_previous_agent' };
    let reads = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.startsWith('/api/v1/agents')) return response({ items: [{ id: agentID, display_name: 'West', enabled: true }, { id: target, display_name: 'Replacement', enabled: true }], next_cursor: '' });
      if (path.startsWith('/api/v1/providers')) return response({ items: [] });
      if (path.startsWith('/api/v1/slis')) return response({ items: [], next_cursor: '' });
      if (init?.method === 'PUT') throw new Error('lost response');
      const first = reads++ === 0; const version = first ? '4' : '5';
      return response({ ...source(), version, ...(first || stillPending ? { reassignment } : {}) }, 200, { ETag: `"datasource:${sourceID}:${version}"` });
    }));
    render(<DatasourcesPage context={admin} />);
    await user.click(await screen.findByRole('button', { name: 'Cancel reassignment' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('outcome is unknown');
    expect(screen.getByRole('button', { name: 'Cancel reassignment' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Reconcile save' }));
    expect(await screen.findByText(stillPending ? /reassignment is still pending/ : /Review its current assignment/)).toHaveAttribute('role', 'status');
    expect(screen.queryByText('Reassignment canceled.')).not.toBeInTheDocument();
    expect(Boolean(screen.queryByRole('heading', { name: 'Updating datasource' }))).toBe(stillPending);
  });
  it('keeps a stale cancellation blocked and tells the user to refresh', async () => {
    const user = userEvent.setup(); history.replaceState({}, '', `/datasources/${sourceID}`);
    const target = 'a6ad8995-d902-41b8-886c-bb7d5897104a';
    const reassignment = { source_agent_id: agentID, target_agent_id: target, requested_at: '2026-10-22T12:00:00Z', authority_expires_at: '2026-10-22T13:00:00Z', state: 'waiting_for_previous_agent' };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.startsWith('/api/v1/agents')) return response({ items: [{ id: agentID, display_name: 'West', enabled: true }, { id: target, display_name: 'Replacement', enabled: true }], next_cursor: '' });
      if (path.startsWith('/api/v1/providers')) return response({ items: [] });
      if (init?.method === 'PUT') return response({ code: 'conflict' }, 409);
      return response({ ...source(), reassignment }, 200, { ETag: `"datasource:${sourceID}:4"` });
    }));
    render(<DatasourcesPage context={admin} />);
    await user.click(await screen.findByRole('button', { name: 'Cancel reassignment' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Refresh it');
    expect(screen.getByRole('heading', { name: 'Updating datasource' })).toBeVisible();
    expect(screen.getByRole('button', { name: 'Cancel reassignment' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Refresh reassignment' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Cancel reassignment' })).toBeEnabled());
    expect(screen.queryByText('Reassignment canceled.')).not.toBeInTheDocument();
  });
  it('refreshes a pending handoff before presenting the replacement as active', async () => {
    const user = userEvent.setup(); history.replaceState({}, '', `/datasources/${sourceID}`);
    const target = 'a6ad8995-d902-41b8-886c-bb7d5897104a';
    let reads = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const path = String(input);
      if (path.startsWith('/api/v1/agents')) return response({ items: [{ id: agentID, display_name: 'West', enabled: true }, { id: target, display_name: 'Replacement', enabled: true }], next_cursor: '' });
      if (path.startsWith('/api/v1/providers')) return response({ items: [] });
      if (path.startsWith('/api/v1/slis')) return response({ items: [], next_cursor: '' });
      if (++reads === 1) return response({ ...source(), reassignment: { source_agent_id: agentID, target_agent_id: target, requested_at: '2026-10-22T12:00:00Z', authority_expires_at: '2026-10-22T13:00:00Z', state: 'waiting_for_previous_agent' } }, 200, { ETag: `"datasource:${sourceID}:4"` });
      return response({ ...source(), agent_id: target, version: '5' }, 200, { ETag: `"datasource:${sourceID}:5"` });
    }));
    render(<DatasourcesPage context={admin} />);
    expect(await screen.findByText(/Waiting for Replacement to apply its configuration/)).toBeVisible();
    expect(screen.getByText(/replacement can start without waiting for the previous agent/)).toBeVisible();
    expect(within(screen.getByRole('region', { name: 'Agent' })).getByText('West')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Refresh reassignment' }));
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Updating datasource' })).not.toBeInTheDocument());
    expect(within(screen.getByRole('region', { name: 'Agent' })).getByText('Replacement')).toBeVisible();
  });
  it('shows pending reassignment and cancels with the source and current version', async () => {
    const user = userEvent.setup(); history.replaceState({}, '', `/datasources/${sourceID}`);
    const target = 'a6ad8995-d902-41b8-886c-bb7d5897104a';
    const reassignment = { source_agent_id: agentID, target_agent_id: target, requested_at: '2026-10-22T12:00:00Z', authority_expires_at: '2026-10-22T13:00:00Z', state: 'replacement_unavailable' };
    let current: Record<string, unknown> = { ...source(), reassignment };
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.startsWith('/api/v1/agents')) return response({ items: [{ id: agentID, display_name: 'West collector', enabled: true }, { id: target, display_name: 'Replacement', enabled: true }], next_cursor: '' });
      if (path.startsWith('/api/v1/providers')) return response({ items: [] });
      if (init?.method === 'PUT') { current = { ...source(), version: '5' }; return response(current, 200, { ETag: `"datasource:${sourceID}:5"` }); }
      return response(current, 200, { ETag: `"datasource:${sourceID}:4"` });
    });
    vi.stubGlobal('fetch', fetcher); render(<DatasourcesPage context={admin} />);
    expect(await screen.findByRole('heading', { name: 'Agent unavailable' })).toBeVisible();
    expect(screen.getByText('Replacement is unavailable. Choose another agent below or refresh after it recovers.')).toBeVisible();
    expect(screen.getByLabelText('Prometheus endpoint')).toBeDisabled();
    expect(within(screen.getByRole('region', { name: 'Agent' })).getByText('West collector')).toBeVisible();
    expect(screen.queryByText('Datasource settings saved.')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Cancel reassignment' }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith(`/api/v1/datasources/${sourceID}`, expect.objectContaining({ method: 'PUT' })));
    const request = fetcher.mock.calls.find(([, init]) => init?.method === 'PUT')?.[1];
    expect(new Headers(request?.headers).get('If-Match')).toBe(`"datasource:${sourceID}:4"`);
    expect(JSON.parse(String(request?.body)).agent_id).toBe(agentID);
    expect(await screen.findByText('Reassignment canceled.')).toBeVisible();
  });
  it('returns to SLI creation with the created datasource when invoked from SLI', async () => {
    const user = userEvent.setup(); history.replaceState({}, '', '/datasources/new?return=slis');
    installFetch(); render(<DatasourcesPage context={admin} />);
    await user.type(await screen.findByLabelText('Display name'), 'Production metrics');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'http://localhost');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    await waitFor(() => expect(location.pathname + location.search).toBe(`/slis/new?datasource=${sourceID}`));
  });

  it('retains the SLI return path only after an uncertain create is reconciled', async () => {
    const user = userEvent.setup(); history.replaceState({}, '', '/datasources/new?return=slis');
    const calls = installFetch(undefined, ['available'], [], undefined, [503, 201]);
    render(<DatasourcesPage context={admin} />);
    await user.type(await screen.findByLabelText('Display name'), 'Production metrics');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'http://localhost');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    await screen.findByRole('button', { name: 'Retry same create' });
    expect(location.pathname).toBe('/datasources/new');
    await user.click(screen.getByRole('button', { name: 'Retry same create' }));
    await waitFor(() => expect(location.pathname + location.search).toBe(`/slis/new?datasource=${sourceID}`));
    const creates = calls.filter((call) => call.url === '/api/v1/datasources' && call.init?.method === 'POST');
    expect(new Headers(creates[0]?.init?.headers).get('Idempotency-Key')).toBe(new Headers(creates[1]?.init?.headers).get('Idempotency-Key'));
  });

  it('explains how to make an unreported agent eligible', async () => {
    history.replaceState({}, '', '/datasources/new?return=slis');
    installFetch([{ id: agentID, name: 'local', display_name: 'Local', enabled: true, runtime: null }]);
    render(<DatasourcesPage context={admin} />);
    expect(await screen.findByText(/Start your registered agent before assigning this datasource/)).toBeVisible();
    expect(screen.getByRole('button', { name: 'View agents' })).toBeVisible();
    expect(screen.getByLabelText('Assigned agent')).toHaveValue('');
  });

  it('explains that an agent must be registered when none exists', async () => {
    history.replaceState({}, '', '/datasources/new'); installFetch([]);
    render(<DatasourcesPage context={admin} />);
    expect(await screen.findByText(/Register and start an Almanac agent before adding a datasource/)).toBeVisible();
    expect(screen.getByRole('button', { name: 'View agents' })).toBeVisible();
  });

  it('keeps the current list during paging and marks it stale if the next page fails', async () => {
    history.replaceState({}, '', '/datasources');
    let finish!: (response: Response) => void;
    const page = new Promise<Response>((resolve) => { finish = resolve; });
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).includes('cursor=')
      ? page : response({ items: [source()], next_cursor: 'next' })));
    const user = userEvent.setup(); render(<DatasourcesPage context={viewer} />);
    await user.click(await screen.findByRole('button', { name: 'Next ›' }));
    expect(screen.getByRole('button', { name: 'Next ›' })).toBeDisabled();
    expect(screen.getByText('Refreshing datasources')).toHaveClass('sr-only');
    await act(async () => finish(response({}, 503)));
    expect(await screen.findByRole('alert')).toHaveTextContent('Showing previous results.');
    expect(screen.getByRole('link', { name: 'Production metrics' })).toBeVisible();
  });

  it('does not show an empty datasource state after an initial read fails', async () => {
    history.replaceState({}, '', '/datasources');
    vi.stubGlobal('fetch', vi.fn(async () => response({}, 503)));
    render(<DatasourcesPage context={viewer} />);
    expect(await screen.findByRole('alert')).toBeVisible();
    expect(screen.queryByRole('heading', { name: 'No datasources yet' })).not.toBeInTheDocument();
  });

  it('keeps refresh available when the first read fails or the list is empty', async () => {
    history.replaceState({}, '', '/datasources');
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({}, 503))
      .mockResolvedValueOnce(response({ items: [], next_cursor: '' }))
      .mockImplementation(async () => response({ items: [source()], next_cursor: '' }));
    vi.stubGlobal('fetch', fetcher);
    const user = userEvent.setup(); render(<DatasourcesPage context={viewer} />);
    expect(await screen.findByRole('alert')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Refresh datasources' }));
    expect(await screen.findByRole('heading', { name: 'No datasources yet' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Refresh datasources' }));
    expect(await screen.findByRole('link', { name: 'Production metrics' })).toBeVisible();
  });

  it('keeps viewer reads redacted and does not request administrator-only agent assignment data', async () => {
    history.replaceState({}, '', '/datasources');
    const calls = installFetch();
    render(<DatasourcesPage context={viewer} />);
    expect(await screen.findByText('Production metrics')).toBeVisible();
    expect(screen.queryByRole('button', { name: 'New datasource' })).not.toBeInTheDocument();
    expect(calls.some((call) => call.url.startsWith('/api/v1/agents'))).toBe(false);
    // Viewers see the record without administrator-only health or agent columns; auth details stay on the detail page (ADR-0031).
    expect(screen.getAllByRole('columnheader').map((cell) => cell.textContent?.replace(/[↕↑↓]/g, ''))).toEqual(['Datasource', 'Cadence', 'SLIs']);
  });

  it('keeps First page available while viewing the last page', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources');
    const fetcher = vi.fn(async (input: RequestInfo | URL) => String(input).includes('cursor=')
      ? response({ items: [source()], next_cursor: '' })
      : response({ items: [source()], next_cursor: 'after-first' }));
    vi.stubGlobal('fetch', fetcher);
    render(<DatasourcesPage context={viewer} />);
    await user.click(await screen.findByRole('button', { name: 'Next ›' }));
    expect(await screen.findByRole('button', { name: '‹ Previous' })).toBeVisible();
    await user.click(screen.getByRole('button', { name: '‹ Previous' }));
    expect(fetcher.mock.calls.at(-1)?.[0]).not.toContain('cursor=');
  });

  it('creates with an explicit secret replacement and never leaves the submitted secret in the page', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources/new');
    const calls = installFetch();
    render(<DatasourcesPage context={admin} />);
    await user.type(await screen.findByLabelText('Display name'), 'Production metrics');
    expect(screen.getByLabelText('Identifier')).toHaveValue('production-metrics');
    await user.clear(screen.getByLabelText('Identifier'));
    await user.type(screen.getByLabelText('Identifier'), 'metrics-prod');
    await user.clear(screen.getByLabelText('Display name'));
    await user.type(screen.getByLabelText('Display name'), 'Updated production metrics');
    expect(screen.getByLabelText('Identifier')).toHaveValue('metrics-prod');
    await user.clear(screen.getByLabelText('Display name'));
    await user.type(screen.getByLabelText('Display name'), 'Production metrics');
    const endpoint = screen.getByLabelText('Prometheus endpoint');
    await user.type(endpoint, 'http://localhost');
    expect(endpoint).toHaveValue('http://localhost');
    await user.click(screen.getByRole('radio', { name: 'Bearer token' }));
    await user.type(screen.getByLabelText('Token'), 'private-token-value');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    expect(await screen.findByRole('heading', { name: 'Production metrics' })).toBeVisible();
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

  it('shows an actionable error for an invalid custom datasource identifier and does not submit it', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources/new');
    const calls = installFetch();
    render(<DatasourcesPage context={admin} />);
    await user.type(await screen.findByLabelText('Display name'), 'Local metrics');
    const name = screen.getByLabelText('Identifier');
    expect(name).toHaveValue('local-metrics');
    await user.clear(name);
    await user.type(name, 'Local Metrics');
    expect(screen.getByText('Use 1–63 characters: start with a lowercase letter, then use lowercase letters, numbers, or hyphens.')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    expect(calls.some((call) => call.url === '/api/v1/datasources' && call.init?.method === 'POST')).toBe(false);
  });

  it('shows validation for an empty suggestion, then creates after the display name is corrected', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources/new');
    const calls = installFetch();
    render(<DatasourcesPage context={admin} />);
    const displayName = await screen.findByLabelText('Display name');
    await user.type(displayName, '你好');
    const name = screen.getByLabelText('Identifier');
    expect(name).toHaveValue('');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'http://localhost');
    expect(within(screen.getByRole('region', { name: 'Agent' })).getByText('West collector')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    expect(screen.getByText('Use 1–63 characters: start with a lowercase letter, then use lowercase letters, numbers, or hyphens.')).toBeVisible();
    expect(name).toHaveFocus();
    expect(calls.some((call) => call.url === '/api/v1/datasources' && call.init?.method === 'POST')).toBe(false);

    await user.clear(displayName);
    await user.type(displayName, 'Local metrics');
    expect(name).toHaveValue('local-metrics');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    await screen.findByRole('heading', { name: 'Production metrics' });
    const create = calls.find((call) => call.url === '/api/v1/datasources' && call.init?.method === 'POST');
    expect(JSON.parse(String(create?.init?.body)).name).toBe('local-metrics');
  });

  it('starts a new create intent after navigating away from an uncertain create', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources/new');
    const calls = installFetch(undefined, ['available'], [], undefined, [503, 201]);
    render(<DatasourcesPage context={admin} />);
    await user.type(await screen.findByLabelText('Identifier'), 'first-source');
    await user.type(screen.getByLabelText('Display name'), 'First source');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'https://first.example.test');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    await screen.findByRole('button', { name: 'Retry same create' });
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.click(await screen.findByRole('button', { name: 'New datasource' }));
    await user.type(await screen.findByLabelText('Identifier'), 'second-source');
    await user.type(screen.getByLabelText('Display name'), 'Second source');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'https://second.example.test');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    const creates = calls.filter((call) => call.url === '/api/v1/datasources' && call.init?.method === 'POST');
    expect(creates).toHaveLength(2);
    expect(new Headers(creates[0]?.init?.headers).get('Idempotency-Key')).not.toBe(new Headers(creates[1]?.init?.headers).get('Idempotency-Key'));
    expect(JSON.parse(String(creates[1]?.init?.body)).name).toBe('second-source');
  });

  it('ignores a late successful save after the user opens a fresh form', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources/new');
    installFetch();
    const delegate = globalThis.fetch.bind(globalThis);
    let resolveFirst!: (value: Response) => void;
    const firstResponse = new Promise<Response>((resolve) => { resolveFirst = resolve; });
    const fetcher = vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input) === '/api/v1/datasources' && init?.method === 'POST' ? firstResponse : delegate(input, init));
    vi.stubGlobal('fetch', fetcher);
    render(<DatasourcesPage context={admin} />);
    await user.type(await screen.findByLabelText('Identifier'), 'old-source');
    await user.type(screen.getByLabelText('Display name'), 'Old source');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'https://old.example.test');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith('/api/v1/datasources', expect.objectContaining({ method: 'POST' })));
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.click(await screen.findByRole('button', { name: 'New datasource' }));
    await user.type(await screen.findByLabelText('Identifier'), 'fresh-source');
    await user.type(screen.getByLabelText('Display name'), 'Fresh source');
    resolveFirst(response(source(), 201, { ETag: `"datasource:${sourceID}:4"` }));
    await waitFor(() => expect(location.pathname).toBe('/datasources/new'));
    expect(screen.getByLabelText('Identifier')).toHaveValue('fresh-source');
    expect(screen.getByLabelText('Display name')).toHaveValue('Fresh source');
    expect(screen.getByRole('button', { name: 'Create datasource' })).toBeEnabled();
  });

  it('ignores a late uncertain save error after the user opens a fresh form', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources/new');
    installFetch();
    const delegate = globalThis.fetch.bind(globalThis);
    let resolveFirst!: (value: Response) => void;
    const firstResponse = new Promise<Response>((resolve) => { resolveFirst = resolve; });
    const fetcher = vi.fn((input: RequestInfo | URL, init?: RequestInit) => String(input) === '/api/v1/datasources' && init?.method === 'POST' ? firstResponse : delegate(input, init));
    vi.stubGlobal('fetch', fetcher);
    render(<DatasourcesPage context={admin} />);
    await user.type(await screen.findByLabelText('Identifier'), 'old-source');
    await user.type(screen.getByLabelText('Display name'), 'Old source');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'https://old.example.test');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith('/api/v1/datasources', expect.objectContaining({ method: 'POST' })));
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    await user.click(await screen.findByRole('button', { name: 'New datasource' }));
    await user.type(await screen.findByLabelText('Identifier'), 'fresh-source');
    resolveFirst(response({ code: 'management_unavailable', message: 'safe failure' }, 503));
    expect(await screen.findByLabelText('Identifier')).toHaveValue('fresh-source');
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Retry same create' })).not.toBeInTheDocument();
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

    const picker = await screen.findByRole('region', { name: 'Agent' });
    expect(await within(picker).findByText('Offline capable')).toBeVisible();
    expect(within(picker).getByText('Unreachable')).toBeVisible();
    await userEvent.click(within(picker).getByRole('button', { name: 'Change' }));
    await userEvent.click(screen.getByLabelText('Assigned agent'));
    expect(screen.getByRole('option', { name: /Never connected/ })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByRole('option', { name: /Never connected/ })).toHaveTextContent('Never connected');
    expect(screen.getByRole('option', { name: /Unsupported/ })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByRole('option', { name: /Unsupported/ })).toHaveTextContent('Diagnostics unsupported');
    expect(screen.getByRole('option', { name: /Disabled/ })).toHaveAttribute('aria-disabled', 'true');
    expect(screen.getByRole('option', { name: /Offline capable/ })).not.toHaveAttribute('aria-disabled');
    await userEvent.click(screen.getByRole('option', { name: /Offline capable/ }));
    expect(within(picker).getByText('Offline capable')).toBeVisible();
    expect(offlineID).toBeTruthy();
    expect(screen.getByText('The assigned agent is offline. Diagnostics require a connected agent.')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Test connection' })).toBeDisabled();
  });

  it('keeps editor preview disabled while the saved datasource configuration is applying, then enables it when ready', async () => {
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const calls = installFetch([], ['applying', 'available']);
    render(<DatasourcesPage context={editor} />);

    const preview = await screen.findByRole('button', { name: 'Run preview' });
    expect(await screen.findByText(/^The assigned agent is applying datasource settings(?: \(revision \d+ of \d+\))?\. Preview will be available when it finishes\.$/)).toBeVisible();
    expect(preview).toBeDisabled();
    await waitFor(() => expect(preview).not.toBeDisabled(), { timeout: 5000 });
    expect(screen.queryByText(/^The assigned agent is applying datasource settings(?: \(revision \d+ of \d+\))?\. Preview will be available when it finishes\.$/)).not.toBeInTheDocument();
    expect(calls.some((call) => call.url.startsWith('/api/v1/agents'))).toBe(true);
  });

  it('offers readiness retry after an offline status and recovers when the agent reconnects', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const calls = installFetch([], ['offline', 'available']);
    render(<DatasourcesPage context={editor} />);
    expect(await screen.findByText('The assigned agent is offline. Preview is unavailable until it reconnects.')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Check readiness' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Run preview' })).not.toBeDisabled());
    expect(calls.filter((call) => call.url.startsWith('/api/v1/providers'))).toHaveLength(2);
  });

  it('keeps cancellation available while a diagnostic is pending', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const pending = { id: 'e5267bc2-3f9c-4bf2-a6c4-5ea2fa3fc6f4', state: 'pending', operation: 'query_preview', created_at: new Date().toISOString(), deadline_at: new Date(Date.now() + 30_000).toISOString(), result_class: null, result: null, error_code: null, message: null };
    const calls = installFetch([], ['available'], [202], pending);
    render(<DatasourcesPage context={editor} />);
    await user.click(await screen.findByRole('button', { name: 'Run preview' }));
    const cancel = await screen.findByRole('button', { name: 'Cancel diagnostic' });
    expect(cancel).toBeEnabled();
    await user.click(cancel);
    await waitFor(() => expect(calls.some((call) => call.url === `/api/v1/diagnostics/${pending.id}/cancel`)).toBe(true));
  });

  it('continues polling after the cancel request fails and shows the diagnostic final state', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const pending = { id: 'e5267bc2-3f9c-4bf2-a6c4-5ea2fa3fc6f4', state: 'pending', operation: 'query_preview', created_at: new Date().toISOString(), deadline_at: new Date(Date.now() + 30_000).toISOString(), result_class: null, result: null, error_code: null, message: null };
    const completed = { ...pending, state: 'succeeded', result_class: 'complete', result: { diagnostic_id: pending.id, state: 'succeeded', result_class: 'complete', series: [] } };
    const calls = installFetch([], ['available'], [202], pending, [], 503, [completed]);
    render(<DatasourcesPage context={editor} />);
    await user.click(await screen.findByRole('button', { name: 'Run preview' }));
    await user.click(await screen.findByRole('button', { name: 'Cancel diagnostic' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Datasource management is temporarily unavailable.');
    await waitFor(() => expect(calls.some((call) => call.url === `/api/v1/diagnostics/${pending.id}` && call.init?.method === 'GET')).toBe(true));
    expect(await screen.findByText('Succeeded')).toBeVisible();
  });

  it('labels diagnostic failure as Failed with a safe provider cause, never as an SLO breach', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    installFetch([], ['available'], [202], {
      id: 'e5267bc2-3f9c-4bf2-a6c4-5ea2fa3fc6f4', state: 'failed', operation: 'query_preview',
      created_at: '2026-10-22T12:00:00Z', deadline_at: '2026-10-22T12:00:30Z', result_class: null,
      result: null, error_code: 'query_failed', message: 'private provider detail',
    });
    render(<DatasourcesPage context={editor} />);
    await user.click(await screen.findByRole('button', { name: 'Run preview' }));
    expect(await screen.findByText('Failed')).toBeVisible();
    expect(screen.getAllByText('The datasource rejected the query.').length).toBeGreaterThan(0);
    expect(screen.getAllByText('query_failed').length).toBeGreaterThan(0);
    expect(screen.queryByText('Breached')).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain('private provider detail');
    expect(document.body.textContent).not.toContain('"error_code": "query"');
  });

  it('accepts a polled failed diagnostic with an empty result payload', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const pending = { id: 'e5267bc2-3f9c-4bf2-a6c4-5ea2fa3fc6f4', state: 'pending', operation: 'query_preview', created_at: new Date().toISOString(), deadline_at: new Date(Date.now() + 30_000).toISOString(), result_class: null, result: null, error_code: null, message: null };
    const failed = { ...pending, state: 'failed', error_code: 'query_failed', message: 'private provider detail' };
    const calls = installFetch([], ['available'], [202], pending, [], 200, [failed]);
    render(<DatasourcesPage context={editor} />);
    await user.click(await screen.findByRole('button', { name: 'Run preview' }));
    expect(await screen.findByText('Failed', {}, { timeout: 3000 })).toBeVisible();
    expect(screen.getAllByText('The datasource rejected the query.').length).toBeGreaterThan(0);
    expect(document.body.textContent).not.toContain('private provider detail');
    expect(calls.some((call) => call.url === `/api/v1/diagnostics/${pending.id}` && call.init?.method === 'GET')).toBe(true);
  });

  it('tests unchanged saved settings without resending retained basic or bearer credentials', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const calls = installFetch();
    render(<DatasourcesPage context={admin} />);
    await user.click(await screen.findByRole('button', { name: 'Test connection' }));
    await waitFor(() => expect(calls.some((call) => call.url === `/api/v1/datasources/${sourceID}/connection-tests` && call.init?.method === 'POST')).toBe(true));
    const call = calls.find((item) => item.url === `/api/v1/datasources/${sourceID}/connection-tests` && item.init?.method === 'POST');
    expect(call?.init?.body).toBe('{}');
    expect(JSON.stringify(call?.init?.headers)).not.toContain('private');
    expect(calls.some((item) => item.url === '/api/v1/datasources/connection-tests')).toBe(false);
  });

  it('requires save or explicit credential replacement before testing edited settings with a retained secret', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const calls = installFetch();
    render(<DatasourcesPage context={admin} />);
    await user.clear(await screen.findByLabelText('Prometheus endpoint'));
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'https://changed.example.test');
    await waitFor(() => expect(screen.getByLabelText('Prometheus endpoint')).toHaveValue('https://changed.example.test'));
    await user.click(screen.getByRole('button', { name: 'Test connection' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Save the connection settings before testing with the saved credential, or replace the credential to test unsaved settings.');
    expect(calls.some((item) => item.url.endsWith('/connection-tests') && item.init?.method === 'POST')).toBe(false);
  });

  it('invalidates readiness after an administrator saves and waits before enabling preview', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    const calls = installFetch(undefined, ['available', 'applying', 'available']);
    render(<DatasourcesPage context={admin} />);

    expect(await screen.findByLabelText('Identifier')).toHaveValue('metrics-prod');
    expect(screen.getByLabelText('Identifier')).toBeDisabled();
    await user.clear(await screen.findByLabelText('Display name'));
    await user.type(screen.getByLabelText('Display name'), 'Updated metrics');
    await user.click(screen.getByRole('button', { name: 'Save settings' }));
    expect(await screen.findByText('Datasource settings saved.')).toBeVisible();
    const preview = screen.getByRole('button', { name: 'Run preview' });
    expect(await screen.findByText(/^The assigned agent is applying datasource settings(?: \(revision \d+ of \d+\))?\. Preview will be available when it finishes\.$/)).toBeVisible();
    expect(preview).toBeDisabled();
    await waitFor(() => expect(preview).not.toBeDisabled(), { timeout: 5000 });
    const update = calls.find((call) => call.url === `/api/v1/datasources/${sourceID}` && call.init?.method === 'PUT');
    expect(update).toBeDefined();
    expect(JSON.parse(String(update?.init?.body)).name).toBe('metrics-prod');
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

  it('lists health, agent and SLI usage for administrators and filters the loaded page', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources');
    const offlineID = '2e8fa70c-94dc-4cff-b15b-0953521e8ea6';
    const second = { ...source(), id: 'c2e0739a-3c86-4a7f-b7e5-c15e9626e71c', name: 'staging', display_name: 'Staging metrics', agent_id: offlineID, tls: { insecure_skip_verify: true, ca_pem: null }, authentication: { mode: 'none', username: null, password_configured: false, token_configured: false } };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith('/api/v1/agents')) return response({ items: [
        { id: agentID, name: 'west', display_name: 'West collector', enabled: true, runtime: { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['collect', 'connection_test', 'query_preview'] }] } },
        { id: offlineID, name: 'east', display_name: 'East collector', enabled: true, runtime: { reachability: 'unreachable', last_heartbeat_at: new Date(Date.now() - 7200_000).toISOString(), capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['collect', 'connection_test', 'query_preview'] }] } },
      ], next_cursor: '' });
      if (url.startsWith('/api/v1/slis')) return response({ items: [{ id: 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c', name: 'request-success', display_name: 'Request success', version: '2', measurement_revision: '1', datasource_id: sourceID, datasource_version: '4', agent_id: agentID, agent_reachability: 'connected', collection_progress: 'idle', measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: 'good', total_query: 'total', polling_interval_seconds: 60, settling_delay_seconds: 60, effective_from: '2026-10-27T12:00:00Z', activated_at: '2026-10-27T12:00:00Z', activation_state: 'active', freshness: 'fresh', latest_observation: null, last_attempt_at: null, last_observation_at: null, next_expected_execution_at: null, created_at: '2026-10-27T12:00:00Z', updated_at: '2026-10-27T12:00:00Z', datasource_display_name: 'Production metrics', recent_measurements: [], linked_slos: { items: [], total_count: 0 } }], next_cursor: '' });
      return response({ items: [source(), second], next_cursor: '' });
    }));
    render(<DatasourcesPage context={admin} />);
    const table = await screen.findByRole('table', { name: 'Datasources' });
    // Name-first rows, health as a chip, no count subline (ADR-0031).
    expect(screen.queryByText(/all times UTC/)).not.toBeInTheDocument();
    expect(within(table).getAllByRole('columnheader').map((cell) => cell.textContent?.replace(/[↕↑↓]/g, ''))).toEqual(['Datasource', 'Health', 'Agent', 'Cadence', 'SLIs']);
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows[0]).toHaveTextContent('Active');
    expect(rows[0]).not.toHaveTextContent('agent connected');
    expect(rows[1]).toHaveTextContent('Not collecting');
    expect(rows[1]).toHaveTextContent('agent unreachable for 2 h');
    expect(rows[1]).toHaveTextContent('TLS unverified');
    await waitFor(() => expect(rows[0]).toHaveTextContent('1'));
    expect(screen.getByRole('radio', { name: 'Unused 1' })).toBeInTheDocument();
    await user.click(screen.getByRole('radio', { name: 'Needs attention 1' }));
    expect(within(table).getAllByRole('row')).toHaveLength(2);
    expect(within(table).getByRole('link', { name: 'Staging metrics' })).toBeVisible();
    await user.click(screen.getByRole('radio', { name: 'All 2' }));
    await user.type(screen.getByLabelText('Filter datasources'), 'West');
    expect(within(table).getAllByRole('row')).toHaveLength(2);
    expect(within(table).getByRole('link', { name: 'Production metrics' })).toBeVisible();
    expect(screen.queryByLabelText('Agent')).not.toBeInTheDocument();
    await user.clear(screen.getByLabelText('Filter datasources'));
    await user.type(screen.getByLabelText('Filter datasources'), 'East collector');
    expect(within(table).getAllByRole('row')).toHaveLength(2);
    expect(within(table).getByRole('link', { name: 'Staging metrics' })).toBeVisible();
    await user.type(screen.getByLabelText('Filter datasources'), ' nothing');
    expect(screen.getByText('No datasources match these filters on this page.')).toBeVisible();
  });

  it('rejects an out-of-range custom cadence and a malformed endpoint before any request', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources/new');
    const calls = installFetch();
    render(<DatasourcesPage context={admin} />);
    await user.type(await screen.findByLabelText('Display name'), 'Production metrics');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'not a url');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('HTTP or HTTPS URL');
    expect(screen.getByRole('alert')).toHaveFocus();
    await user.clear(screen.getByLabelText('Prometheus endpoint'));
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'https://metrics.example.test');
    await user.click(within(screen.getByRole('radiogroup', { name: 'Query every' })).getByRole('radio', { name: 'Custom…' }));
    await user.type(screen.getByLabelText('Custom query every'), '4');
    await user.click(screen.getByRole('button', { name: 'Create datasource' }));
    await waitFor(() => expect(screen.getAllByRole('alert').some((alert) => /from 15 to 86400/.test(alert.textContent ?? ''))).toBe(true));
    expect(calls.some((call) => call.url === '/api/v1/datasources' && call.init?.method === 'POST')).toBe(false);
  });

  it('switches TLS modes with the segmented control and continues into SLI creation', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/datasources/new');
    const calls = installFetch();
    render(<DatasourcesPage context={admin} />);
    await user.type(await screen.findByLabelText('Display name'), 'Production metrics');
    await user.type(screen.getByLabelText('Prometheus endpoint'), 'https://metrics.example.test');
    expect(screen.queryByLabelText('Custom CA PEM')).not.toBeInTheDocument();
    await user.click(screen.getByRole('radio', { name: 'Custom CA' }));
    await user.type(screen.getByLabelText('Custom CA PEM'), '-----BEGIN CERTIFICATE-----');
    await user.click(screen.getByRole('radio', { name: 'Skip verification' }));
    expect(screen.getByText(/Insecure: the certificate is not checked/)).toBeVisible();
    await user.click(screen.getByRole('radio', { name: 'Verify certificate' }));
    expect(screen.getByRole('radio', { name: '60 s' })).toHaveAttribute('aria-checked', 'true');
    await user.click(screen.getByRole('radio', { name: '5 min' }));
    await user.click(screen.getByRole('button', { name: 'Create and add an SLI' }));
    await waitFor(() => expect(location.pathname + location.search).toBe(`/slis/new?datasource=${sourceID}`));
    const create = calls.find((call) => call.url === '/api/v1/datasources' && call.init?.method === 'POST');
    const body = JSON.parse(String(create?.init?.body));
    expect(body.tls).toEqual({ insecure_skip_verify: false, ca_pem: null });
    expect(body.polling_interval_seconds).toBe(300);
  });

  it('shows the detail tiles, the unreachable-agent banner and discards edits', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', `/datasources/${sourceID}`);
    installFetch([{ id: agentID, name: 'west', display_name: 'West collector', enabled: true, runtime: { reachability: 'unreachable', last_heartbeat_at: '2026-10-22T08:00:00Z', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['connection_test', 'query_preview'] }] } }]);
    render(<DatasourcesPage context={admin} />);
    expect(await screen.findByRole('heading', { name: 'Production metrics' })).toBeVisible();
    expect(screen.getByText(/Not collecting · agent unreachable/)).toBeVisible();
    expect(screen.getByRole('region', { name: 'Agent unreachable' })).toHaveTextContent('has not sent a heartbeat since 08:00:00 UTC');
    expect(screen.getByRole('link', { name: 'Open the agent' })).toHaveAttribute('href', `/agents?agent=${agentID}`);
    expect(screen.getByText('Used by')).toBeVisible();
    expect(screen.getByText('every 60 s')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Discard changes' })).toBeDisabled();
    await user.clear(screen.getByLabelText('Display name'));
    await user.type(screen.getByLabelText('Display name'), 'Renamed');
    expect(screen.getByRole('button', { name: 'Discard changes' })).toBeEnabled();
    await user.click(screen.getByRole('button', { name: 'Discard changes' }));
    expect(screen.getByLabelText('Display name')).toHaveValue('Production metrics');
    expect(screen.getByText('Set')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Replace' }));
    expect(screen.getByLabelText('Token')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Keep saved token' }));
    expect(screen.getByText('Set')).toBeVisible();
  });
});

it.each(['active', 'unavailable', 'empty'])('handles collection-only replacements when SLI usage is %s', async (usage) => {
 const targetID = 'a6ad8995-d902-41b8-886c-bb7d5897104a';
 const user = userEvent.setup(); history.replaceState({}, '', `/datasources/${sourceID}`);
 vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
 const url = String(input);
 if (url.startsWith('/api/v1/agents')) return response({ items: [
 { id: agentID, display_name: 'Original collector', enabled: true, runtime: { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['collect'] }] } },
 { id: targetID, display_name: 'Collection only', enabled: true, runtime: { reachability: 'connected', capabilities: [{ provider: 'prometheus', configuration_formats: ['prometheus-http-v1'], operations: ['collect'] }] } }
 ], next_cursor: '' });
 if (url.startsWith('/api/v1/providers')) return response({ items: [] });
 if (url === `/api/v1/datasources/${sourceID}`) return response(source(), 200, { ETag: `"datasource:${sourceID}:4"` });
if (url.startsWith('/api/v1/slis') && usage === 'unavailable') return response({ code: 'unavailable' }, 503);
 if (url.startsWith('/api/v1/slis') && usage === 'empty') return response({ items: [], next_cursor: '' });
 if (url.startsWith('/api/v1/slis')) return response({ items: [{ id: 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c', name: 'request-success', display_name: 'Request success', version: '2', measurement_revision: '1', datasource_id: sourceID, datasource_version: '4', agent_id: agentID, agent_reachability: 'connected', collection_progress: 'idle', measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: 'good', total_query: 'total', polling_interval_seconds: 60, settling_delay_seconds: 60, effective_from: '2026-10-27T12:00:00Z', activated_at: '2026-10-27T12:00:00Z', activation_state: 'active', freshness: 'fresh', latest_observation: null, last_attempt_at: null, last_observation_at: null, next_expected_execution_at: null, created_at: '2026-10-27T12:00:00Z', updated_at: '2026-10-27T12:00:00Z', datasource_display_name: 'Production metrics', recent_measurements: [], linked_slos: { items: [], total_count: 0 } }], next_cursor: '' });
 return response({ items: [], next_cursor: '' });
 }));
 render(<DatasourcesPage context={editor} />);
 await screen.findByRole('heading', { name: 'Production metrics' });
 await user.click(within(screen.getByRole('region', { name: 'Agent' })).getByRole('button', { name: 'Change' }));
 await user.click(screen.getByLabelText('Assigned agent'));
 await waitFor(() => {
  const option = screen.getByRole('option', { name: /Collection only/ });
  if (usage === 'empty') expect(option).toHaveAttribute('aria-disabled', 'true');
  else expect(option).not.toHaveAttribute('aria-disabled', 'true');
 });
 expect(document.querySelector('.ds-head__status')).toHaveTextContent(usage === 'empty' ? 'Needs attention' : 'Active');
});

it('loads assigned agent names and health beyond the first 200 agents before sorting', async () => {
  history.replaceState({}, '', '/datasources');
  const earlyID = '00000000-0000-4000-8000-000000000001';
  const firstPage = Array.from({length:200}, (_,index) => ({id:`00000000-0000-4000-8000-${String(index+1).padStart(12,'0')}`,display_name:index===0?'Alpha collector':`Unused ${index}`,enabled:true}));
  const lateAgent = {id:agentID,display_name:'Zeta collector',enabled:true,runtime:{reachability:'connected',capabilities:[{provider:'prometheus',configuration_formats:['prometheus-http-v1'],operations:['collect','connection_test','query_preview']}]}};
  const sources = [source(), {...source(),id:'00000000-0000-4000-8000-000000000999',name:'other',display_name:'Other metrics',agent_id:earlyID}];
  const fetcher = vi.fn(async (input:RequestInfo|URL) => {
    const url=String(input);
    if(url.startsWith('/api/v1/agents')) return response(url.includes('cursor=agents-next')?{items:[lateAgent],next_cursor:''}:{items:firstPage,next_cursor:'agents-next'});
    if(url.startsWith('/api/v1/slis')) return response({items:[],next_cursor:''});
    return response({items:sources,next_cursor:''});
  });
  vi.stubGlobal('fetch',fetcher);
  render(<DatasourcesPage context={admin}/>);
  await screen.findByText('Zeta collector');
  expect(fetcher).toHaveBeenCalledWith('/api/v1/agents?limit=200&cursor=agents-next',expect.objectContaining({signal:expect.any(AbortSignal)}));
  const user=userEvent.setup();
  await user.click(screen.getByRole('button',{name:'Agent'}));
  await waitFor(()=>expect(document.querySelector('.ds-table tbody tr')).toHaveTextContent('Other metrics'));
  await user.click(screen.getByRole('button',{name:'Health'}));
  await waitFor(()=>expect(document.querySelector('.ds-table tbody tr')).toHaveTextContent('Production metrics'));
  expect(screen.queryByText('Unknown agent')).not.toBeInTheDocument();
});

it('keeps a rejected sorted read visible after clearing sorting and recovers through refresh', async () => {
  history.replaceState({}, '', '/datasources');
  let denied=false;
  installFetch();
  const originalFetch=fetch;
  vi.stubGlobal('fetch',vi.fn(async (input:RequestInfo|URL,init?:RequestInit)=>{
    if(denied && String(input).startsWith('/api/v1/datasources?')) return response({code:'forbidden'},403);
    return originalFetch(input,init);
  }));
  render(<DatasourcesPage context={admin}/>);
  await screen.findByText('Production metrics');
  const user=userEvent.setup();
  await user.selectOptions(screen.getByRole('combobox',{name:'Sort by'}),'name');
  await waitFor(()=>expect(screen.getByRole('columnheader',{name:'Datasource'})).toHaveAttribute('aria-sort','ascending'));
  denied=true;
  await user.click(screen.getByRole('button',{name:'Refresh datasources'}));
  await waitFor(()=>expect(screen.queryByText('Production metrics')).not.toBeInTheDocument());
  await user.selectOptions(screen.getByRole('combobox',{name:'Sort by'}),'');
  expect(screen.queryByRole('heading',{name:'No datasources yet'})).not.toBeInTheDocument();
  expect(screen.getAllByRole('alert').length).toBeGreaterThan(0);
  denied=false;
  await user.click(screen.getByRole('button',{name:'Try again'}));
  await screen.findByText('Production metrics');
});
