import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SlisPage } from '../src/slis/SlisPage.tsx';
import type { AuthContext } from '../src/auth/client.ts';

const admin: AuthContext = { csrf_token: 'csrf-admin', session: { user_id: 'admin-1', login: 'owner', role: 'administrator', restricted: false } };
const editor: AuthContext = { csrf_token: 'csrf-editor', session: { user_id: 'user-1', login: 'editor', role: 'editor', restricted: false } };
const viewer: AuthContext = { csrf_token: 'csrf-viewer', session: { user_id: 'user-2', login: 'reader', role: 'viewer', restricted: false } };
const sourceID = '8ed93d7e-51e3-4a3e-9222-3e8a612de20a';
const sliID = 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c';
function source() { return { id: sourceID, name: 'metrics-prod', display_name: 'Production metrics', version: '4', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test', tls: { insecure_skip_verify: false, ca_pem: null }, authentication: { mode: 'bearer', username: null, password_configured: false, token_configured: true }, polling_interval_seconds: 15, settling_delay_seconds: 60, agent_id: '4b13e073-3c86-4a7f-b7e5-c15e9626e71c', created_at: '2026-10-20T12:00:00Z', updated_at: '2026-10-21T12:00:00Z' }; }
function sli() { return { id: sliID, name: 'request-success', display_name: 'Request success', version: '2', measurement_revision: '1', datasource_id: sourceID, datasource_version: '4', agent_id: '4b13e073-3c86-4a7f-b7e5-c15e9626e71c', agent_reachability: 'connected', collection_progress: 'idle', measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: 'sum(rate(requests_total{status=~"2.."}[1m]))', total_query: 'sum(rate(requests_total[1m]))', polling_interval_seconds: 15, settling_delay_seconds: 60, effective_from: '2026-10-27T12:00:15Z', activation_state: 'scheduled', freshness: 'fresh', latest_observation: { observation_id: 'f313e073-3c86-4a7f-b7e5-c15e9626e71c', measurement_revision: '1', interval_start: '2026-10-27T11:59:45Z', interval_end: '2026-10-27T12:00:00Z', outcome: 'observed', good_count: '99', total_count: '100', error_code: null, missing_components: [], recorded_at: '2026-10-27T12:00:01Z' }, last_attempt_at: '2026-10-27T12:00:01Z', last_observation_at: '2026-10-27T12:00:01Z', next_expected_execution_at: '2026-10-27T12:00:15Z', created_at: '2026-10-27T11:59:00Z', updated_at: '2026-10-27T12:00:00Z' }; }
function response(body: unknown, status = 200, headers: Record<string, string> = {}) { return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } }); }
function historyPage(items: unknown[]) { return { items, next_cursor: '', earliest_queryable_interval_start: '2026-10-27T11:00:00Z', inspected_start: '2026-10-27T11:59:45Z', inspected_end: '2026-10-27T12:00:00Z', missing_intervals: [] }; }
function expectObservedHistory() {
  const table = screen.getByRole('table');
  expect(within(table).getByRole('cell', { name: '99' })).toBeVisible();
  expect(within(table).getByRole('cell', { name: '100' })).toBeVisible();
}
function deferredResponse() {
  let resolve!: (value: Response) => void;
  const promise = new Promise<Response>((done) => { resolve = done; });
  return { promise, resolve };
}

afterEach(() => { cleanup(); history.replaceState({}, '', '/'); vi.unstubAllGlobals(); });

describe('SLI page', () => {
  it.each([['/slis', admin], ['/slis/new', admin], ['/slis', editor], ['/slis/new', editor]] as const)('shows the datasource prerequisite at %s for %s', async (path, context) => {
    history.replaceState({}, '', path);
    vi.stubGlobal('fetch', vi.fn(async () => response({ items: [], next_cursor: '' })));
    render(<SlisPage context={context} />);
    if (context === admin) {
      expect(await screen.findByText(/Add a Prometheus datasource to create/)).toBeVisible();
      const action = screen.getByRole('button', { name: 'Add datasource' });
      expect(screen.queryByRole('button', { name: 'Create SLI' })).not.toBeInTheDocument();
      await userEvent.click(action);
      expect(location.pathname + location.search).toBe('/datasources/new?return=slis');
    } else {
      expect(await screen.findByText(/Ask an administrator to add a Prometheus datasource/)).toBeVisible();
      expect(screen.queryByRole('button', { name: 'Add datasource' })).not.toBeInTheDocument();
    }
  });

  it('does not mistake a pending or failed datasource read for an empty prerequisite and retries', async () => {
    history.replaceState({}, '', '/slis');
    const deferred = deferredResponse(); let reads = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).startsWith('/api/v1/datasources')
      ? (++reads === 1 ? deferred.promise : response({ items: [source()], next_cursor: '' }))
      : response({ items: [], next_cursor: '' })));
    render(<SlisPage context={admin} />);
    expect(screen.queryByText(/Add a Prometheus datasource to create/)).not.toBeInTheDocument();
    await act(async () => deferred.resolve(response({}, 503)));
    expect(await screen.findByRole('alert')).toHaveTextContent('Datasources could not be loaded');
    expect(screen.queryByRole('button', { name: 'Add datasource' })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('button', { name: 'Create SLI' })).toBeVisible();
  });

  it('shows existing SLIs while datasource names are still being fetched', async () => {
    history.replaceState({}, '', '/slis');
    const deferred = deferredResponse();
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).startsWith('/api/v1/datasources')
      ? deferred.promise : response({ items: [sli()], next_cursor: '' })));
    render(<SlisPage context={admin} />);
    expect(await screen.findByRole('button', { name: /Request success/ })).toBeVisible();
    expect(screen.getByText('Checking datasources')).toHaveClass('sr-only');
    await act(async () => deferred.resolve(response({ items: [source()], next_cursor: '' })));
    expect(await screen.findByText(/Production metrics/)).toBeVisible();
  });

  it('selects the returned datasource and suggests an editable identifier', async () => {
    history.replaceState({}, '', `/slis/new?datasource=${sourceID}`);
    vi.stubGlobal('fetch', vi.fn(async () => response({ items: [source()], next_cursor: '' })));
    const user = userEvent.setup(); render(<SlisPage context={admin} />);
    expect(await screen.findByLabelText('Datasource')).toHaveValue(sourceID);
    await user.type(screen.getByLabelText('Display name'), 'Request success');
    expect(screen.getByLabelText('SLI name')).toHaveValue('request-success');
    await user.clear(screen.getByLabelText('SLI name'));
    await user.type(screen.getByLabelText('SLI name'), 'custom-name');
    await user.type(screen.getByLabelText('Display name'), ' updated');
    expect(screen.getByLabelText('SLI name')).toHaveValue('custom-name');
  });

  it.each(['empty', 'observed', 'error'] as const)('shows loading until initial history settles: %s', async (outcome) => {
    history.replaceState({}, '', `/slis/${sliID}`);
    const deferred = deferredResponse();
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return deferred.promise;
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
    }));
    render(<SlisPage context={editor} />);
    expect(await screen.findByRole('heading', { name: 'Request success' })).toBeVisible();
    expect(screen.getByText('Refreshing observation history')).toHaveClass('sr-only');
    expect(screen.queryByText('No observations have been recorded in this range.')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Display name')).toBeEnabled();
    await act(async () => deferred.resolve(outcome === 'error' ? response({ code: 'sli_unavailable' }, 503)
      : response(historyPage(outcome === 'observed' ? [sli().latest_observation] : []))));
    expect(screen.queryByText('Refreshing observation history')).not.toBeInTheDocument();
    if (outcome === 'empty') expect(screen.getByText('No observations have been recorded in this range.')).toBeVisible();
    if (outcome === 'observed') expectObservedHistory();
    if (outcome === 'error') {
      expect(screen.getByRole('alert')).toHaveTextContent('SLI management is temporarily unavailable.');
      expect(screen.queryByText('No observations have been recorded in this range.')).not.toBeInTheDocument();
    }
  });

  it.each(['initial-first', 'refresh-first'] as const)('keeps the newer refresh history with overlapping requests: %s', async (order) => {
    history.replaceState({}, '', `/slis/${sliID}`);
    const initial = deferredResponse();
    const refresh = deferredResponse();
    let historyReads = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return ++historyReads === 1 ? initial.promise : refresh.promise;
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
    }));
    const user = userEvent.setup();
    render(<SlisPage context={editor} />);
    await screen.findByRole('heading', { name: 'Request success' });
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(historyReads).toBe(2));
    if (order === 'initial-first') {
      await act(async () => initial.resolve(response(historyPage([]))));
      expect(screen.getByText('Refreshing observation history')).toHaveClass('sr-only');
      expect(screen.queryByText('No observations have been recorded in this range.')).not.toBeInTheDocument();
    }
    await act(async () => refresh.resolve(response(historyPage([sli().latest_observation]))));
    if (order === 'refresh-first') await act(async () => initial.resolve(response(historyPage([]))));
    expectObservedHistory();
    expect(screen.queryByText('Refreshing observation history')).not.toBeInTheDocument();
  });

  it('retains loaded history while a refresh is pending', async () => {
    history.replaceState({}, '', `/slis/${sliID}`);
    const refresh = deferredResponse();
    let historyReads = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return ++historyReads === 1 ? response(historyPage([sli().latest_observation])) : refresh.promise;
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
    }));
    const user = userEvent.setup();
    render(<SlisPage context={editor} />);
    await screen.findByRole('table');
    expectObservedHistory();
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(historyReads).toBe(2));
    expect(screen.getByText('Refreshing observation history')).toHaveClass('sr-only');
    expectObservedHistory();
    await act(async () => refresh.resolve(response(historyPage([]))));
    expect(screen.getByText('No observations have been recorded in this range.')).toBeVisible();
  });

  it('retains loaded rows while fetching the next history page', async () => {
    history.replaceState({}, '', `/slis/${sliID}`);
    const next = deferredResponse();
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations') && url.includes('cursor=')) return next.promise;
      if (url.includes('/observations')) return response({ ...historyPage([sli().latest_observation]), next_cursor: 'next' });
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
    }));
    const user = userEvent.setup();
    render(<SlisPage context={editor} />);
    await user.click(await screen.findByRole('button', { name: 'Load more intervals' }));
    expect(screen.getByText('Refreshing observation history')).toHaveClass('sr-only');
    expectObservedHistory();
    await act(async () => next.resolve(response(historyPage([]))));
    expectObservedHistory();
    expect(screen.queryByText('Refreshing observation history')).not.toBeInTheDocument();
  });
  it('creates an SLI through the API and reads its scheduled activation and exact history', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/slis/new');
    const calls: { url: string; init?: RequestInit }[] = [];
    const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input); calls.push({ url, init });
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      if (url.startsWith('/api/v1/slis/') && url.endsWith('/observations?limit=50')) return response(historyPage([sli().latest_observation]));
      if (url === `/api/v1/slis/${sliID}`) return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
      if (url === '/api/v1/slis' && init?.method === 'POST') return response(sli(), 201, { ETag: `"sli:${sliID}:2"` });
      if (url.startsWith('/api/v1/slis?')) return response({ items: [sli()], next_cursor: '' });
      return response({ code: 'not_found' }, 404);
    });
    vi.stubGlobal('fetch', fetcher);
    render(<SlisPage context={editor} />);
    await user.type(await screen.findByLabelText('SLI name'), 'request-success');
    await user.type(screen.getByLabelText('Display name'), 'Request success');
    await user.selectOptions(screen.getByLabelText('Datasource'), sourceID);
    fireEvent.change(screen.getByLabelText('Good count query'), { target: { value: 'sum(rate(requests_total{status=~"2.."}[1m]))' } });
    fireEvent.change(screen.getByLabelText('Total count query'), { target: { value: 'sum(rate(requests_total[1m]))' } });
    await user.click(screen.getByRole('button', { name: 'Create SLI' }));

    expect(await screen.findByRole('heading', { name: 'Request success' })).toBeVisible();
    expect(screen.getByText('scheduled')).toBeVisible();
    expect(screen.getAllByText('99').length).toBeGreaterThan(0);
    expect(screen.getAllByText('100').length).toBeGreaterThan(0);
    const create = calls.find((call) => call.url === '/api/v1/slis' && call.init?.method === 'POST');
    expect(JSON.parse(String(create?.init?.body))).toMatchObject({ name: 'request-success', datasource_id: sourceID, measurement_kind: 'event_counts', component_mode: 'interval_count' });
    expect(new Headers(create?.init?.headers).get('X-CSRF-Token')).toBe('csrf-editor');
    await waitFor(() => expect(calls.some((call) => call.url === `/api/v1/slis/${sliID}/observations?limit=50`)).toBe(true));
  });

  it('shows SLI list and exact counts to a viewer without mutation controls', async () => {
    history.replaceState({}, '', '/slis');
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).startsWith('/api/v1/slis') ? response({ items: [sli()], next_cursor: '' }) : response({ items: [source()], next_cursor: '' })));
    render(<SlisPage context={viewer} />);
    expect(await screen.findByText('Request success')).toBeVisible();
    expect(screen.getByText(/Latest 99 good \/ 100 total/)).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Add SLI' })).not.toBeInTheDocument();
  });

  it('loads the next bounded SLI list page from its cursor', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/slis');
    const next = { ...sli(), id: 'c2e0739a-3c86-4a7f-b7e5-c15e9626e71c', name: 'checkout-success', display_name: 'Checkout success' };
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      if (url === '/api/v1/slis?limit=50') return response({ items: [sli()], next_cursor: 'next-cursor' });
      if (url === '/api/v1/slis?limit=50&cursor=next-cursor') return response({ items: [next], next_cursor: '' });
      return response({ code: 'not_found' }, 404);
    });
    vi.stubGlobal('fetch', fetcher);
    render(<SlisPage context={viewer} />);
    expect(await screen.findByText('Request success')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Load more SLIs' }));
    expect(await screen.findByText('Checkout success')).toBeVisible();
    expect(fetcher).toHaveBeenCalledWith('/api/v1/slis?limit=50&cursor=next-cursor', expect.anything());
    expect(screen.queryByRole('button', { name: 'Load more SLIs' })).not.toBeInTheDocument();
  });

  it('loads later datasource pages for creation', async () => {
    history.replaceState({}, '', '/slis/new');
    const second = { ...source(), id: 'c2e0739a-3c86-4a7f-b7e5-c15e9626e71c', name: 'second-source', display_name: 'Second source' };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).includes('cursor=next')
      ? response({ items: [second], next_cursor: '' }) : response({ items: [source()], next_cursor: 'next' })));
    render(<SlisPage context={editor} />);
    expect(await screen.findByRole('option', { name: /Second source/ })).toBeVisible();
  });

  it('keeps detail controls available when history fails and preserves a dirty draft on refresh', async () => {
    history.replaceState({}, '', `/slis/${sliID}`);
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return response({ code: 'sli_unavailable' }, 503);
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
    }));
    const user = userEvent.setup();
    render(<SlisPage context={editor} />);
    const field = await screen.findByLabelText('Display name');
    await user.clear(field);
    await user.type(field, 'Draft name');
    expect(await screen.findByText('SLI management is temporarily unavailable.')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(field).toHaveValue('Draft name'));
  });

  it('ignores a late detail refresh after navigating to another SLI', async () => {
    history.replaceState({}, '', `/slis/${sliID}`);
    const otherID = 'c2e0739a-3c86-4a7f-b7e5-c15e9626e71c';
    const other = { ...sli(), id: otherID, name: 'checkout', display_name: 'Checkout' };
    let resolveOld!: (value: Response) => void;
    let reads = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return response(historyPage([]));
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      if (url === `/api/v1/slis/${otherID}`) return response(other, 200, { ETag: `"sli:${otherID}:2"` });
      if (url === `/api/v1/slis/${sliID}` && ++reads > 1) return new Promise<Response>((resolve) => { resolveOld = resolve; });
      return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
    }));
    const user = userEvent.setup();
    render(<SlisPage context={editor} />);
    await screen.findByRole('heading', { name: 'Request success' });
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(reads).toBe(2));
    act(() => { history.pushState({}, '', `/slis/${otherID}`); window.dispatchEvent(new PopStateEvent('popstate')); });
    expect(await screen.findByRole('heading', { name: 'Checkout' })).toBeVisible();
    await act(async () => { resolveOld(response(sli(), 200, { ETag: `"sli:${sliID}:2"` })); });
    expect(screen.getByRole('heading', { name: 'Checkout' })).toBeVisible();
  });

  it('shows inferred missing ranges separately from retained observations', async () => {
    history.replaceState({}, '', `/slis/${sliID}`);
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return response({ ...historyPage([]), missing_intervals: [{ interval_start: '2026-10-27T11:59:45Z', interval_end: '2026-10-27T12:00:00Z' }] });
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
    }));
    render(<SlisPage context={viewer} />);
    expect(await screen.findByText('Missing intervals')).toBeVisible();
    expect(screen.getByText(/History before/)).toBeVisible();
    expect(screen.getByText('No observations have been recorded in this range.')).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Measurement queries' })).toBeVisible();
  });
});
