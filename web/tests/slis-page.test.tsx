import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterAll, afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { SlisPage } from '../src/slis/SlisPage.tsx';
import type { AuthContext } from '../src/auth/client.ts';
import * as telemetry from '../src/telemetry/client.ts';

const admin: AuthContext = { csrf_token: 'csrf-admin', session: { user_id: 'admin-1', login: 'owner', role: 'administrator', restricted: false } };
const editor: AuthContext = { csrf_token: 'csrf-editor', session: { user_id: 'user-1', login: 'editor', role: 'editor', restricted: false } };
const viewer: AuthContext = { csrf_token: 'csrf-viewer', session: { user_id: 'user-2', login: 'reader', role: 'viewer', restricted: false } };
const sourceID = '8ed93d7e-51e3-4a3e-9222-3e8a612de20a';
const sliID = 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c';
function source() { return { id: sourceID, name: 'metrics-prod', display_name: 'Production metrics', version: '4', provider: 'prometheus', configuration_format: 'prometheus-http-v1', endpoint: 'https://metrics.example.test', tls: { insecure_skip_verify: false, ca_pem: null }, authentication: { mode: 'bearer', username: null, password_configured: false, token_configured: true }, polling_interval_seconds: 15, settling_delay_seconds: 60, agent_id: '4b13e073-3c86-4a7f-b7e5-c15e9626e71c', created_at: '2026-10-20T12:00:00Z', updated_at: '2026-10-21T12:00:00Z' }; }
function sli() { return { id: sliID, name: 'request-success', display_name: 'Request success', version: '2', measurement_revision: '1', datasource_id: sourceID, datasource_version: '4', agent_id: '4b13e073-3c86-4a7f-b7e5-c15e9626e71c', agent_reachability: 'connected', collection_progress: 'idle', measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: 'sum(rate(requests_total{status=~"2.."}[1m]))', total_query: 'sum(rate(requests_total[1m]))', polling_interval_seconds: 15, settling_delay_seconds: 60, effective_from: '2026-10-27T12:00:15Z', activated_at: null, activation_state: 'scheduled', freshness: 'fresh', latest_observation: { observation_id: 'f313e073-3c86-4a7f-b7e5-c15e9626e71c', measurement_revision: '1', interval_start: '2026-10-27T11:59:45Z', interval_end: '2026-10-27T12:00:00Z', outcome: 'observed', good_count: '99', total_count: '100', error_code: null, missing_components: [], recorded_at: '2026-10-27T12:00:01Z' }, last_attempt_at: '2026-10-27T12:00:01Z', last_observation_at: '2026-10-27T12:00:01Z', next_expected_execution_at: '2026-10-27T12:00:15Z', datasource_display_name: 'Production metrics', recent_measurements: [], linked_slos: { items: [], total_count: 0 }, created_at: '2026-10-27T11:59:00Z', updated_at: '2026-10-27T12:00:00Z' }; }
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

const dialogDescriptors = { showModal: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'showModal'), close: Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'close') };
Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value(this: HTMLDialogElement) { this.setAttribute('open', ''); } });
Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value(this: HTMLDialogElement) { this.removeAttribute('open'); } });
afterAll(() => { for (const [name, descriptor] of Object.entries(dialogDescriptors)) { if (descriptor) Object.defineProperty(HTMLDialogElement.prototype, name, descriptor); else Reflect.deleteProperty(HTMLDialogElement.prototype, name); } });
beforeEach(() => { vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} }); });
afterEach(() => { cleanup(); history.replaceState({}, '', '/'); vi.unstubAllGlobals(); });

describe('SLI page', () => {
  it('signals detail readiness after history renders and reports a history failure explicitly', async () => {
    const ready = vi.spyOn(telemetry, 'journeyContentReady');
    try {
      history.replaceState({}, '', `/slis/${sliID}`);
      const pending = deferredResponse();
      vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
        if (String(input).includes('/observations')) return pending.promise;
        if (String(input).includes('/datasources')) return response({ items: [source()], next_cursor: '' });
        return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
      }));
      render(<SlisPage context={editor} />);
      await screen.findByRole('heading', { name: 'Request success' });
      expect(ready).not.toHaveBeenCalledWith('sli.detail', false);
      await act(async () => pending.resolve(response(historyPage([]))));
      await waitFor(() => expect(ready).toHaveBeenCalledWith('sli.detail', false));
      cleanup(); ready.mockClear();
      vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).includes('/observations')
        ? response({}, 503) : String(input).includes('/datasources') ? response({ items: [source()], next_cursor: '' }) : response(sli(), 200, { ETag: `"sli:${sliID}:2"` })));
      render(<SlisPage context={editor} />);
      await waitFor(() => expect(ready).toHaveBeenCalledWith('sli.detail', true));
      expect(ready).not.toHaveBeenCalledWith('sli.detail', false);
    } finally { ready.mockRestore(); }
  });
  it.each([['/slis', admin], ['/slis/new', admin], ['/slis', editor], ['/slis/new', editor]] as const)('shows the datasource prerequisite at %s for %s', async (path, context) => {
    history.replaceState({}, '', path);
    vi.stubGlobal('fetch', vi.fn(async () => response({ items: [], next_cursor: '' })));
    render(<SlisPage context={context} />);
    if (context === admin && path === '/slis') {
      // The list has one action, the header's New SLI; the prerequisite is explained, not duplicated as buttons.
      expect(await screen.findByText(/Add a Prometheus datasource to create/)).toBeVisible();
      expect(screen.getAllByRole('button').filter((button) => /SLI|datasource/i.test(button.textContent ?? '')).map((button) => button.textContent)).toEqual(['New SLI']);
    } else if (context === admin) {
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
    expect(await screen.findByText(/Create your first SLI/)).toBeVisible();
    expect(screen.getByRole('button', { name: 'New SLI' })).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Create SLI' })).not.toBeInTheDocument();
  });

  it('recovers a failed datasource read with existing SLIs', async () => {
    history.replaceState({}, '', '/slis');
    let reads = 0;
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).startsWith('/api/v1/datasources')
      ? (++reads === 1 ? response({}, 503) : response({ items: [source()], next_cursor: '' }))
      : response({ items: [sli()], next_cursor: '' })));
    render(<SlisPage context={admin} />);
    expect(await screen.findByRole('alert')).toHaveTextContent('Datasources could not be loaded');
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('button', { name: 'New SLI' })).toBeVisible();
  });

  it('focuses route headings and ignores fragment history', async () => {
    history.replaceState({}, '', '/slis');
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      if (url.includes('/observations')) return response(historyPage([]));
      if (url.includes(sliID)) return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
      return response({ items: [sli()], next_cursor: '' });
    }));
    render(<SlisPage context={editor} />);
    const row = await screen.findByRole('link', { name: 'Request success' });
    await waitFor(() => expect(screen.getByRole('heading', { name: 'SLIs' })).toHaveFocus());
    expect(row).toHaveAttribute('href', `/slis/${sliID}`);
    act(() => { history.pushState({}, '', row.getAttribute('href')!); window.dispatchEvent(new PopStateEvent('popstate')); });
    const heading = await screen.findByRole('heading', { name: 'Request success' });
    await waitFor(() => expect(heading).toHaveFocus());
    const refresh = screen.getByRole('button', { name: 'Refresh SLI' });
    await userEvent.click(refresh);
    expect(refresh).toHaveFocus();
    act(() => { history.pushState({}, '', '#main'); window.dispatchEvent(new PopStateEvent('popstate')); });
    expect(screen.getByRole('button', { name: 'Refresh SLI' })).toBeVisible();
    expect(refresh).toHaveFocus();
    expect(within(screen.getByRole('navigation', { name: 'Breadcrumb' })).getByRole('link', { name: 'SLIs' })).toHaveAttribute('href', '/slis');
    act(() => { history.pushState({}, '', '/slis'); window.dispatchEvent(new PopStateEvent('popstate')); });
    await waitFor(() => expect(screen.getByRole('heading', { name: 'SLIs' })).toHaveFocus());
    await userEvent.click(await screen.findByRole('button', { name: 'New SLI' }));
    await waitFor(() => expect(screen.getByRole('heading', { name: 'New SLI' })).toHaveFocus());
  });

  it('shows existing SLIs while datasource names are still being fetched', async () => {
    history.replaceState({}, '', '/slis');
    const deferred = deferredResponse();
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).startsWith('/api/v1/datasources')
      ? deferred.promise : response({ items: [sli()], next_cursor: '' })));
    render(<SlisPage context={admin} />);
    expect(await screen.findByRole('link', { name: 'Request success' })).toBeVisible();
    expect(screen.getByText('Checking datasources')).toHaveClass('sr-only');
    await act(async () => deferred.resolve(response({ items: [source()], next_cursor: '' })));
    expect(await screen.findByRole('link', { name: 'Production metrics' })).toHaveAttribute('href', `/datasources/${sourceID}`);
  });

  it('selects the returned datasource and suggests an editable identifier', async () => {
    history.replaceState({}, '', `/slis/new?datasource=${sourceID}`);
    vi.stubGlobal('fetch', vi.fn(async () => response({ items: [source()], next_cursor: '' })));
    const user = userEvent.setup(); render(<SlisPage context={admin} />);
    expect(await screen.findByRole('button', { name: 'Change' })).toBeVisible();
    expect(screen.getByText('Production metrics')).toBeVisible();
    expect(screen.getByText(/first interval can precede creation/i)).toBeVisible();
    await user.type(screen.getByLabelText('Display name'), 'Request success');
    expect(screen.getByLabelText('Identifier')).toHaveValue('request-success');
    await user.clear(screen.getByLabelText('Identifier'));
    await user.type(screen.getByLabelText('Identifier'), 'custom-name');
    await user.type(screen.getByLabelText('Display name'), ' updated');
    expect(screen.getByLabelText('Identifier')).toHaveValue('custom-name');
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
    await userEvent.click(screen.getByRole('button', { name: 'Edit SLI' }));
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
    await user.click(screen.getByRole('button', { name: 'Refresh SLI' }));
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
    await user.click(screen.getByRole('button', { name: 'Refresh SLI' }));
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
    await user.type(await screen.findByLabelText('Identifier'), 'request-success');
    await user.type(screen.getByLabelText('Display name'), 'Request success');
    expect(await screen.findByRole('button', { name: 'Change' })).toBeVisible();
    fireEvent.change(screen.getByLabelText('Good events'), { target: { value: 'sum(increase(requests_total{status=~"2.."}[1m]))' } });
    fireEvent.change(screen.getByLabelText('Total events'), { target: { value: 'sum(increase(requests_total[1m]))' } });
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

  it.each([
    ['connected', 'preparing', /after the assigned agent receives its configuration/],
    ['unreachable', 'preparing', /assigned agent is unavailable/],
    ['connected', 'blocked', /collection is blocked/i],
  ])('explains the first result with agent %s and activation %s', async (reachability, activation, explanation) => {
    history.replaceState({}, '', `/slis/${sliID}`);
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return response(historyPage([]));
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      return response({ ...sli(), agent_reachability: reachability, activation_state: activation, latest_observation: null, last_attempt_at: null, last_observation_at: null }, 200, { ETag: `"sli:${sliID}:2"` });
    }));
    render(<SlisPage context={viewer} />);
    expect(await screen.findByText(explanation)).toBeVisible();
    expect(screen.getByText(/most recent complete interval/)).toHaveTextContent('60 seconds');
    expect(screen.queryByText(/first query is overdue|First interval:/)).not.toBeInTheDocument();
    expect(screen.getByText(/does not mean the counts are zero/)).toBeVisible();
  });

  it('shows a saved interval before creation without calling it the collection start', async () => {
    history.replaceState({}, '', `/slis/${sliID}`);
    const saved = { ...sli(), created_at: '2026-10-27T12:02:00Z', updated_at: '2026-10-27T12:02:06Z', activated_at: '2026-10-27T12:02:05Z', activation_state: 'active', effective_from: '2026-10-27T12:00:45Z', last_attempt_at: '2026-10-27T12:02:06Z', last_observation_at: '2026-10-27T12:02:06Z', next_expected_execution_at: '2026-10-27T12:02:15Z', latest_observation: { ...sli().latest_observation, interval_start: '2026-10-27T12:00:45Z', interval_end: '2026-10-27T12:01:00Z', recorded_at: '2026-10-27T12:02:06Z' } };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return response(historyPage([saved.latest_observation]));
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      return response(saved, 200, { ETag: `"sli:${sliID}:2"` });
    }));
    render(<SlisPage context={viewer} />);
    expect(await screen.findByText(/12:00:45/, { selector: '.sli-interval' })).toBeVisible();
    expect(screen.queryByText('Collection starts')).not.toBeInTheDocument();
    expect(screen.getByText('Collection activated')).toBeVisible();
    expect(screen.getByText('Oct 27, 2026, 12:02:05 PM UTC')).toBeVisible();
  });

  it.each([
    [null, 'Not activated yet'],
    ['2026-10-27T12:02:05Z', 'Oct 27, 2026, 12:02:05 PM UTC'],
  ])('shows the actual activation time %s', async (activatedAt, expected) => {
    history.replaceState({}, '', `/slis/${sliID}`);
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return response(historyPage([]));
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      return response({ ...sli(), activated_at: activatedAt }, 200, { ETag: `"sli:${sliID}:2"` });
    }));
    render(<SlisPage context={viewer} />);
    const label = await screen.findByText('Collection activated');
    expect(label.nextElementSibling).toHaveTextContent(expected);
  });

  it('keeps the list breadcrumb when an SLI is named SLIs', async () => {
    history.replaceState({}, '', `/slis/${sliID}`);
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return response(historyPage([]));
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      return response({ ...sli(), display_name: 'SLIs' }, 200, { ETag: `"sli:${sliID}:2"` });
    }));
    render(<SlisPage context={viewer} />);
    expect(await screen.findByRole('link', { name: 'SLIs' })).toHaveAttribute('href', '/slis');
    expect(screen.getByRole('heading', { name: 'SLIs' })).toBeVisible();
  });

  it('offers related routes while names resolve and reports failed name reads', async () => {
    history.replaceState({}, '', `/slis/${sliID}`);
    const sourceRead = deferredResponse(); const agentRead = deferredResponse();
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return response(historyPage([]));
      if (url.startsWith('/api/v1/datasources')) return sourceRead.promise;
      if (url.startsWith('/api/v1/agents/')) return agentRead.promise;
      return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
    }));
    render(<SlisPage context={admin} />);
    expect(await screen.findByRole('link', { name: 'View datasource' })).toHaveAttribute('href', `/datasources/${sourceID}`);
    expect(screen.getByRole('link', { name: 'View assigned agent' })).toHaveAttribute('href', `/agents?agent=${sli().agent_id}`);
    await act(async () => { sourceRead.resolve(response({}, 503)); agentRead.resolve(response({}, 503)); });
    expect(await screen.findByText('Datasource name unavailable. Refresh to try again.')).toBeVisible();
    expect(await screen.findByText('Agent name unavailable. Refresh to try again.')).toBeVisible();
    expect(screen.getByRole('heading', { name: 'Latest result' })).toBeVisible();
  });

  it('links named records for administrators and explains agent access to other roles', async () => {
    history.replaceState({}, '', `/slis/${sliID}`);
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return response(historyPage([]));
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      if (url.startsWith('/api/v1/agents/')) return response({ id: sli().agent_id, name: 'local', display_name: 'Local agent', enabled: true, version: '3', created_at: '2026-10-20T12:00:00Z', updated_at: '2026-10-21T12:00:00Z' }, 200, { ETag: `"agent:${sli().agent_id}:3"` });
      return response(sli(), 200, { ETag: `"sli:${sliID}:2"` });
    });
    vi.stubGlobal('fetch', fetcher);
    const view = render(<SlisPage context={admin} />);
    expect(await screen.findByRole('link', { name: 'Production metrics' })).toHaveAttribute('href', `/datasources/${sourceID}`);
    expect(await screen.findByRole('link', { name: 'Local agent' })).toHaveAttribute('href', `/agents?agent=${sli().agent_id}`);
    expect(screen.getByRole('link', { name: 'SLIs' })).toHaveAttribute('href', '/slis');
    expect(screen.getByText('Request success', { selector: '[aria-current="page"]' })).toBeVisible();
    view.unmount(); fetcher.mockClear();
    render(<SlisPage context={viewer} />);
    expect(await screen.findByText('Agent details are available to administrators.')).toBeVisible();
    expect(fetcher.mock.calls.some(([url]) => String(url).startsWith('/api/v1/agents/'))).toBe(false);
  });

  it('shows SLI list and exact counts to a viewer without mutation controls', async () => {
    history.replaceState({}, '', '/slis');
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).startsWith('/api/v1/slis') ? response({ items: [sli()], next_cursor: '' }) : response({ items: [source()], next_cursor: '' })));
    render(<SlisPage context={viewer} />);
    expect(await screen.findByText('Request success')).toBeVisible();
    expect(screen.getByText(/99 \/ 100/)).toBeVisible();
    expect(screen.queryByRole('button', { name: 'New SLI' })).not.toBeInTheDocument();
  });

  it('searches on the server and renders exact bounded samples and linked objectives', async () => {
    history.replaceState({}, '', '/slis');
    const objectives = Array.from({ length: 5 }, (_, index) => ({ id: `c2e0739${index}-3c86-4a7f-b7e5-c15e9626e71c`, name: `objective-${index}`, display_name: `Objective ${index}` }));
    const matched = {
      ...sli(), display_name: 'Search target',
      recent_measurements: [
        { interval_start: '2026-10-27T11:58:00Z', interval_end: '2026-10-27T11:58:15Z', state: 'observed', good_count: '98', total_count: '100' },
        { interval_start: '2026-10-27T11:58:15Z', interval_end: '2026-10-27T11:58:30Z', state: 'zero_traffic', good_count: '0.000000000000', total_count: '0.000000000000' },
        { interval_start: '2026-10-27T11:58:30Z', interval_end: '2026-10-27T11:58:45Z', state: 'known_empty', good_count: null, total_count: null },
        { interval_start: '2026-10-27T11:58:45Z', interval_end: '2026-10-27T11:59:00Z', state: 'missing', good_count: null, total_count: null },
        { interval_start: '2026-10-27T11:59:00Z', interval_end: '2026-10-27T11:59:15Z', state: 'partial', good_count: null, total_count: null },
        { interval_start: '2026-10-27T11:59:15Z', interval_end: '2026-10-27T11:59:30Z', state: 'failed', good_count: null, total_count: null },
      ],
      linked_slos: { items: objectives, total_count: 6 },
    };
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      if (url.startsWith('/api/v1/slis?')) return response({ items: url.includes('q=Search+target') ? [matched] : [sli()], next_cursor: '' });
      return response({ code: 'not_found' }, 404);
    });
    vi.stubGlobal('fetch', fetcher);
    const user = userEvent.setup();
    render(<SlisPage context={viewer} />);
    const search = await screen.findByLabelText('Search SLIs');
    await user.type(search, 'Search target');
    expect(await screen.findByRole('link', { name: 'Search target' })).toHaveAttribute('href', `/slis/${sliID}`);
    expect(fetcher).toHaveBeenCalledWith('/api/v1/slis?limit=20&q=Search+target', expect.anything());
    expect(screen.getByRole('img', { name: /Recent good-to-total measurements/ })).toBeVisible();
    // ADR-0032: the list shows the strip alone (exact counts are on the SLI page) and the linked SLOs as a count
    // that opens the SLI's objectives, with the names in its tooltip.
    expect(screen.queryByText(/^Exact values/)).toBeNull();
    const linkedCount = screen.getByRole('link', { name: /^6 SLOs: Objective 0/ });
    expect(linkedCount).toHaveTextContent(/^6 SLOs$/);
    expect(linkedCount).toHaveAttribute('href', `/slis/${sliID}`);
    expect(linkedCount.getAttribute('title')).toMatch(/^Objective 0, .*, and 1 more$/);
    expect(screen.getByRole('link', { name: 'Production metrics' })).toHaveAttribute('href', `/datasources/${sourceID}`);
  });

  it('loads the next bounded SLI list page from its cursor', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/slis');
    const next = { ...sli(), id: 'c2e0739a-3c86-4a7f-b7e5-c15e9626e71c', name: 'checkout-success', display_name: 'Checkout success' };
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      if (url === '/api/v1/slis?limit=20') return response({ items: [sli()], next_cursor: 'next-cursor' });
      if (url === '/api/v1/slis?limit=20&cursor=next-cursor') return response({ items: [next], next_cursor: '' });
      return response({ code: 'not_found' }, 404);
    });
    vi.stubGlobal('fetch', fetcher);
    render(<SlisPage context={viewer} />);
    expect(await screen.findByText('Request success')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Next ›' }));
    expect(await screen.findByText('Checkout success')).toBeVisible();
    expect(fetcher).toHaveBeenCalledWith('/api/v1/slis?limit=20&cursor=next-cursor', expect.anything());
    expect(screen.queryByText('Request success')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Next ›' })).toBeDisabled();
    expect(new URL(window.location.href).searchParams.get('cursor')).toBe('next-cursor');
    await user.click(screen.getByRole('button', { name: '‹ Previous' }));
    expect(await screen.findByText('Request success')).toBeVisible();
    expect(screen.queryByText('Checkout success')).not.toBeInTheDocument();
    expect(new URL(window.location.href).searchParams.has('cursor')).toBe(false);
  });

  it('resets the bounded list cursor when page size or search changes', async () => {
    history.replaceState({}, '', '/slis?cursor=old-cursor');
    const fetcher = vi.fn(async (input: RequestInfo | URL) => String(input).startsWith('/api/v1/datasources')
      ? response({ items: [source()], next_cursor: '' }) : response({ items: [sli()], next_cursor: 'next-cursor' }));
    vi.stubGlobal('fetch', fetcher);
    const user = userEvent.setup();
    render(<SlisPage context={viewer} />);
    await user.selectOptions(await screen.findByLabelText('Per page'), '10');
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith('/api/v1/slis?limit=10', expect.anything()));
    expect(new URL(window.location.href).searchParams.get('page_size')).toBe('10');
    expect(new URL(window.location.href).searchParams.has('cursor')).toBe(false);
    await user.click(screen.getByRole('button', { name: 'Next ›' }));
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith('/api/v1/slis?limit=10&cursor=next-cursor', expect.anything()));
    await user.type(screen.getByLabelText('Search SLIs'), 'target');
    await waitFor(() => expect(fetcher).toHaveBeenCalledWith('/api/v1/slis?limit=10&q=target', expect.anything()));
    expect(new URL(window.location.href).searchParams.has('cursor')).toBe(false);
  });

  it('loads later datasource pages for creation', async () => {
    history.replaceState({}, '', '/slis/new');
    const second = { ...source(), id: 'c2e0739a-3c86-4a7f-b7e5-c15e9626e71c', name: 'second-source', display_name: 'Second source' };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => String(input).includes('cursor=next')
      ? response({ items: [second], next_cursor: '' }) : response({ items: [source()], next_cursor: 'next' })));
    render(<SlisPage context={editor} />);
    await userEvent.click(await screen.findByLabelText('Datasource'));
    expect(await screen.findByRole('option', { name: /Second source/ })).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Change' })).not.toBeInTheDocument();
  });

  it('reports every missing field under its control and continues to the SLO form after creating', async () => {
    const user = userEvent.setup();
    history.replaceState({}, '', '/slis/new');
    const second = { ...source(), id: 'c2e0739a-3c86-4a7f-b7e5-c15e9626e71c', name: 'second-source', display_name: 'Second source' };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source(), second], next_cursor: '' });
      if (url.startsWith('/api/v1/agents')) return response({ items: [{ id: sli().agent_id, name: 'local', display_name: 'Local agent', enabled: true, version: '3', created_at: '2026-10-20T12:00:00Z', updated_at: '2026-10-21T12:00:00Z', runtime: { reachability: 'unreachable', protocol: 'almanac-agent/2', software_version: '0.9.3', capabilities: [], last_heartbeat_at: null, desired_configuration_revision: '1', applied_configuration_revision: '1', scheduler_health: 'stopped', collection_progress: 'blocked', configuration_failure_code: null, configuration_failure_revision: null, last_attempt_at: null, last_successful_query_at: null, next_expected_execution_at: null, collection_explanation: 'none' } }], next_cursor: '' });
      if (url === '/api/v1/slis' && init?.method === 'POST') return response(sli(), 201, { ETag: `"sli:${sliID}:2"` });
      return response({ items: [], next_cursor: '' });
    }));
    render(<SlisPage context={admin} />);
    await user.click(await screen.findByRole('button', { name: 'Create SLI' }));
    expect(screen.getByRole('alert')).toHaveTextContent('Enter a display name.');
    expect(screen.getByLabelText('Display name')).toHaveAccessibleDescription(/Enter a display name/);
    expect(screen.getByLabelText('Datasource')).toHaveAccessibleDescription(/Choose an existing Prometheus datasource/);
    expect(screen.getByText('Enter a good events query.')).toBeVisible();
    expect(screen.getByLabelText('Good events')).toHaveAccessibleDescription(/Enter a good events query/);
    await user.type(screen.getByLabelText('Display name'), 'Request success');
    expect(screen.getByLabelText('Display name')).not.toHaveAccessibleDescription(/Enter a display name/);
    await user.click(screen.getByLabelText('Datasource'));
    expect(await screen.findByRole('option', { name: /Production metrics/ })).toHaveTextContent('Agent unreachable');
    await user.click(screen.getByRole('option', { name: /Production metrics/ }));
    expect(screen.getByText('You can still create the SLI. Collection starts when the agent reconnects.')).toBeVisible();
    fireEvent.change(screen.getByLabelText('Good events'), { target: { value: 'sum(increase(good[1m]))' } });
    fireEvent.change(screen.getByLabelText('Total events'), { target: { value: 'sum(increase(total[1m]))' } });
    await user.click(screen.getByRole('button', { name: 'Create SLI and add an SLO' }));
    await waitFor(() => expect(location.pathname + location.search).toBe(`/slos/new?sli=${sliID}`));
  });

  it('keeps every digit of exact counts in the latest result', async () => {
    history.replaceState({}, '', `/slis/${sliID}`);
    const precise = { ...sli(), latest_observation: { ...sli().latest_observation!, good_count: '1234567.0001', total_count: '1234567.5' } };
    vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes('/observations')) return response(historyPage([precise.latest_observation!]));
      if (url.startsWith('/api/v1/datasources')) return response({ items: [source()], next_cursor: '' });
      if (url.startsWith('/api/v1/slis?')) return response({ items: [precise], next_cursor: '' });
      return response(precise, 200, { ETag: `"sli:${sliID}:2"` });
    }));
    render(<SlisPage context={viewer} />);
    await screen.findByRole('heading', { name: 'Request success' });
    expect(screen.getAllByText(/1,234,567\.0001/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/1,234,567\.5/).length).toBeGreaterThan(0);
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
    await screen.findByRole('heading', { name: 'Request success' });
    await user.click(screen.getByRole('button', { name: 'Edit SLI' }));
    const field = screen.getByLabelText('Display name');
    await user.clear(field);
    await user.type(field, 'Draft name');
    expect(await screen.findByText('SLI management is temporarily unavailable.')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Refresh SLI' }));
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
    await user.click(screen.getByRole('button', { name: 'Refresh SLI' }));
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
    await userEvent.click(screen.getByText('Settings and technical details'));
    expect(screen.getByText('Good count query')).toBeVisible();
  });
});
