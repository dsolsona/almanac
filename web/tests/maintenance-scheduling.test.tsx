import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { AuthContext } from '../src/auth/client.ts';
import { MaintenancePage } from '../src/maintenance/MaintenancePage.tsx';
import { ScheduleMaintenance } from '../src/maintenance/ScheduleMaintenance.tsx';
import { maintenanceClient, type MaintenanceWindow } from '../src/maintenance/client.ts';
import { SloFailure, sloClient, type OperationalOverview } from '../src/slos/client.ts';
import { serviceClient } from '../src/services/client.ts';

const id = '60000000-0000-4000-8000-000000000001';
const sloId = '70000000-0000-4000-8000-000000000001';
const context: AuthContext = {
  csrf_token: 'test',
  session: { login: 'editor', user_id: id, role: 'editor', restricted: false },
};
const now = '2026-11-18T02:00:00Z';
const record: MaintenanceWindow = {
  id, title: 'Upgrade Checkout', reason: 'Planned database upgrade', scope: { kind: 'slos', slo_ids: [sloId] },
  revision: '1', operation: 'create', tombstone: false, start: '2026-11-18T02:00:01Z', end: '2026-11-18T02:00:02Z',
  created_at: now, updated_at: now, creator_id: id, actor_id: id, audit_id: id,
};
const dialogMethod = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'showModal');
const closeMethod = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'close');

beforeEach(() => {
  if (!dialogMethod) Object.defineProperty(HTMLDialogElement.prototype, 'showModal', {
    configurable: true, value(this: HTMLDialogElement) { this.setAttribute('open', ''); },
  });
  if (!closeMethod) Object.defineProperty(HTMLDialogElement.prototype, 'close', {
    configurable: true, value(this: HTMLDialogElement) { this.removeAttribute('open'); },
  });
  const empty = { inventory_at: now, ready: true, total: '0', items: [] };
  vi.spyOn(maintenanceClient, 'impact').mockResolvedValue(empty);
  vi.spyOn(maintenanceClient, 'preview').mockResolvedValue(empty);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  if (!dialogMethod) Reflect.deleteProperty(HTMLDialogElement.prototype, 'showModal');
  if (!closeMethod) Reflect.deleteProperty(HTMLDialogElement.prototype, 'close');
  vi.useRealTimers();
  history.replaceState({}, '', '/');
});

async function showDetail(window: MaintenanceWindow) {
  vi.useFakeTimers({ toFake: ['Date', 'performance', 'setTimeout', 'clearTimeout'] });
  vi.setSystemTime(new Date(now));
  const serverClock = { time: Date.parse(now), receivedAt: performance.now() };
  const get = vi.spyOn(maintenanceClient, 'get').mockResolvedValue({
    value: window, etag: `"maintenance:${id}:1"`, serverClock,
  });
  vi.spyOn(sloClient, 'get').mockRejectedValue(new Error('Name not loaded'));
  history.replaceState({}, '', `/maintenance/${id}`);
  render(<MaintenancePage context={context} />);
  await act(async () => {});
  return get;
}

it('preserves an early-end draft when its window ends naturally', async () => {
  await showDetail({ ...record, start: '2026-11-18T01:00:00Z' });
  fireEvent.click(screen.getByRole('button', { name: 'End maintenance early' }));
  const reason = screen.getByLabelText('Reason for this change');
  fireEvent.change(reason, { target: { value: 'Traffic switch finished.' } });
  act(() => vi.advanceTimersByTime(2000));
  expect(screen.getByRole('dialog', { name: 'End this maintenance early?' })).toBeVisible();
  expect(reason).toHaveValue('Traffic switch finished.');
  expect(screen.getByRole('button', { name: 'End maintenance' })).toBeDisabled();
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation(callback => { callback(performance.now()); return 1; });
  fireEvent.click(screen.getByRole('button', { name: 'Close' }));
  expect(screen.getByRole('heading', { name: record.title, level: 1 })).toHaveFocus();
});

it('checks the early-end time at submission and clears its validation error on edit', async () => {
  await showDetail({ ...record, start: '2026-11-18T01:00:00Z', end: '2026-11-18T03:00:00Z' });
  const submit = vi.fn(async () => new Response('{}', { status: 503 }));
  vi.stubGlobal('fetch', submit);
  fireEvent.click(screen.getByRole('button', { name: 'End maintenance early' }));
  const time = screen.getByLabelText('New end time (UTC)');
  expect(time).toHaveAttribute('step', '1');
  fireEvent.change(time, { target: { value: '02:01' } });
  fireEvent.change(screen.getByLabelText('Reason for this change'), { target: { value: 'Traffic switch finished.' } });
  act(() => vi.advanceTimersByTime(120_000));
  fireEvent.click(screen.getByRole('button', { name: 'End maintenance' }));
  expect(submit).not.toHaveBeenCalled();
  expect(screen.getByRole('alert')).toHaveTextContent('Choose a future time before the scheduled end.');
  fireEvent.change(time, { target: { value: '02:03' } });
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});

it('shows the latest scope and disables cancellation at its updated start boundary', async () => {
  const get = await showDetail({ ...record, start: '2026-11-18T03:00:00Z', end: '2026-11-18T04:00:00Z' });
  const replacement = '70000000-0000-4000-8000-000000000002';
  const newer = { ...record, revision: '2', operation: 'edit' as const, start: '2026-11-18T02:00:01Z', end: '2026-11-18T04:00:00Z', scope: { kind: 'slos' as const, slo_ids: [replacement] } };
  vi.mocked(sloClient.get).mockResolvedValue({ value: { display_name: 'Orders availability' }, etag: '' } as Awaited<ReturnType<typeof sloClient.get>>);
  get.mockResolvedValueOnce({
    value: newer, etag: `"maintenance:${id}:2"`, serverClock: { time: Date.parse(now), receivedAt: performance.now() },
  });
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ code: 'maintenance_conflict' }, { status: 409 })));
  fireEvent.click(screen.getByRole('button', { name: 'Cancel scheduled maintenance' }));
  const reason = screen.getByLabelText('Reason for this change');
  fireEvent.change(reason, { target: { value: 'Postpone the work.' } });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Cancel maintenance' })); });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Read latest revision' })); });
  expect(reason).toHaveValue('Postpone the work.');
  const confirmation = screen.getByRole('dialog', { name: 'Cancel this maintenance window?' });
  expect(within(confirmation).getByText('Orders availability')).toBeVisible();
  expect(screen.getByRole('button', { name: 'Cancel maintenance' })).toBeEnabled();
  act(() => vi.advanceTimersByTime(1000));
  expect(screen.getByRole('button', { name: 'Cancel maintenance' })).toBeDisabled();
  expect(screen.getByText('In progress', { exact: true })).toBeVisible();
});

it('can check an uncertain cancellation without giving up the draft', async () => {
  const scheduled = { ...record, start: '2026-11-18T03:00:00Z', end: '2026-11-18T04:00:00Z' };
  const get = await showDetail(scheduled);
  const cancelled = { ...scheduled, revision: '2', operation: 'cancel' as const, tombstone: true };
  get.mockRestore();
  const fetch = vi.fn(async (_input: string, request?: RequestInit) => {
    if (request?.method === 'POST') throw new TypeError('Response lost');
    return Response.json(cancelled, { headers: { ETag: `"maintenance:${id}:2"` } });
  });
  vi.stubGlobal('fetch', fetch);
  fireEvent.click(screen.getByRole('button', { name: 'Cancel scheduled maintenance' }));
  const reason = screen.getByLabelText('Reason for this change');
  fireEvent.change(reason, { target: { value: 'Postpone the work.' } });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Cancel maintenance' })); });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Read latest revision' })); });
  expect(reason).toHaveValue('Postpone the work.');
  expect(screen.getByText('Cancelled', { exact: true })).toBeVisible();
  expect(screen.getByRole('button', { name: 'Cancel maintenance' })).toBeDisabled();
  expect(fetch.mock.calls.map(([input, request]) => [input, request?.method ?? 'GET'])).toEqual([
    [`/api/v1/scoped-maintenance/${id}/cancel`, 'POST'],
    [`/api/v1/scoped-maintenance/${id}`, 'GET'],
  ]);
});

it('allows dismissal while checking a revision and aborts the pending read', async () => {
  const get = await showDetail({ ...record, start: '2026-11-18T03:00:00Z', end: '2026-11-18T04:00:00Z' });
  let readSignal: AbortSignal | undefined;
  get.mockImplementationOnce(async (_id, _revision, signal) => {
    readSignal = signal;
    return new Promise(() => {});
  });
  vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Response lost'); }));
  fireEvent.click(screen.getByRole('button', { name: 'Cancel scheduled maintenance' }));
  fireEvent.change(screen.getByLabelText('Reason for this change'), { target: { value: 'Postpone the work.' } });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Cancel maintenance' })); });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Read latest revision' })); });
  expect(readSignal?.aborted).toBe(false);
  fireEvent.click(screen.getByRole('button', { name: 'Close' }));
  expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  expect(readSignal?.aborted).toBe(true);
});

it('keeps a saved cancellation when a stale detail read resolves before the effect refresh', async () => {
  const scheduled = { ...record, start: '2026-11-18T03:00:00Z', end: '2026-11-18T04:00:00Z' };
  const get = await showDetail(scheduled);
  let finishStaleRead: (() => void) | undefined;
  get.mockImplementationOnce(() => new Promise(resolve => {
    finishStaleRead = () => resolve({ value: scheduled, etag: `"maintenance:${id}:1"` });
  })).mockRejectedValueOnce(new Error('Refresh unavailable'));
  fireEvent.click(screen.getByRole('button', { name: 'Refresh maintenance window' }));
  const cancelled = { ...scheduled, revision: '2', operation: 'cancel', tombstone: true };
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(cancelled), {
    headers: { ETag: `"maintenance:${id}:2"` },
  })));
  vi.spyOn(window, 'requestAnimationFrame').mockImplementation(() => { finishStaleRead?.(); return 1; });
  fireEvent.click(screen.getByRole('button', { name: 'Cancel scheduled maintenance' }));
  fireEvent.change(screen.getByLabelText('Reason for this change'), { target: { value: 'Postpone the work.' } });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Cancel maintenance' })); });
  expect(screen.getByText('Cancelled', { exact: true })).toBeVisible();
  expect(screen.getByRole('alert')).toHaveTextContent('Refresh unavailable');
});

it('updates the list and detail at the server-time window boundaries', async () => {
  vi.useFakeTimers({ toFake: ['Date', 'performance', 'setTimeout', 'clearTimeout'] });
  // Deliberately keep the client wall clock ahead of the server.
  vi.setSystemTime(new Date('2026-11-19T00:00:00Z'));
  const serverClock = { time: Date.parse(now), receivedAt: performance.now() };
  vi.spyOn(maintenanceClient, 'list').mockResolvedValue({ value: { items: [record] }, etag: '', serverClock });
  vi.spyOn(maintenanceClient, 'get').mockResolvedValue({ value: record, etag: '', serverClock });
  vi.spyOn(sloClient, 'get').mockRejectedValue(new Error('Name not loaded'));

  history.replaceState({}, '', '/maintenance');
  const list = render(<MaintenancePage context={context} />);
  history.replaceState({}, '', `/maintenance/${id}`);
  const detail = render(<MaintenancePage context={context} />);
  await act(async () => {});
  expect(screen.getAllByText('Scheduled', { exact: true })).toHaveLength(2);
  act(() => vi.advanceTimersByTime(1000));
  expect(screen.getAllByText('In progress', { exact: true })).toHaveLength(2);
  act(() => vi.advanceTimersByTime(1000));
  expect(screen.getAllByText('Ended', { exact: true })).toHaveLength(2);
  expect(list.container).toBeInTheDocument();
  expect(detail.container).toBeInTheDocument();
});

it('keeps the current route when a save completes after leaving the form', async () => {
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(new Date(now));
  history.replaceState({}, '', '/maintenance/new');
  vi.spyOn(sloClient, 'overview').mockResolvedValue({
    items: [{ slo: { id: sloId, display_name: 'Checkout availability', service_display_name: 'Checkout' } }],
  } as OperationalOverview);
  let finish: ((result: { value: MaintenanceWindow; etag: string }) => void) | undefined;
  const create = vi.spyOn(maintenanceClient, 'create').mockReturnValue(new Promise(resolve => { finish = resolve; }));
  const form = render(<ScheduleMaintenance context={context} />);
  fireEvent.change(screen.getByLabelText('Name'), { target: { value: record.title } });
  fireEvent.change(screen.getByLabelText('Reason'), { target: { value: record.reason } });
  fireEvent.focus(screen.getByRole('combobox', { name: 'Search SLOs' }));
  fireEvent.mouseDown(await screen.findByRole('option', { name: /Checkout availability/ }));
  const start = screen.getByRole('button', { name: /^Maintenance window/ });
  fireEvent.click(start);
  const picker = within(screen.getByRole('dialog', { name: 'Choose maintenance window' }));
  fireEvent.click(picker.getByRole('button', { name: 'Friday, November 20, 2026' }));
  fireEvent.click(picker.getByRole('button', { name: 'Saturday, November 21, 2026' }));
  const clock = picker.getByRole('textbox', { name: 'Start time (UTC)' });
  fireEvent.change(clock, { target: { value: '25:30' } });
  expect(picker.getByRole('button', { name: 'Set window' })).toBeDisabled();
  fireEvent.change(clock, { target: { value: '23:30' } });
  fireEvent.change(picker.getByRole('textbox', { name: 'End time (UTC)' }), { target: { value: '00:30' } });
  fireEvent.click(picker.getByRole('button', { name: 'Set window' }));
  expect(start).toHaveFocus();
  expect(start).toHaveTextContent('Sat, Nov 21, 2026 · 00:30 UTC');
  const save = screen.getByRole('button', { name: 'Schedule maintenance' });
  await waitFor(() => expect(save).toBeEnabled());
  fireEvent.click(save);
  expect(create).toHaveBeenCalledExactlyOnceWith(expect.objectContaining({
    start: '2026-11-20T23:30:00.000Z', end: '2026-11-21T00:30:00.000Z',
  }), context, expect.any(String));

  form.unmount();
  history.replaceState({}, '', '/alerts');
  await act(async () => { finish?.({ value: record, etag: '' }); });
  expect(location.pathname).toBe('/alerts');
});

it('preserves the exact bounds of an API-created window when editing its reason', async () => {
  const route = `/maintenance/${id}?edit=1`;
  history.replaceState({}, '', route);
  const etag = `"maintenance:${id}:1"`;
  vi.spyOn(sloClient, 'get').mockRejectedValue(new Error('Name not loaded'));
  vi.spyOn(sloClient, 'overview').mockResolvedValue({ items: [] } as unknown as OperationalOverview);
  const update = vi.spyOn(maintenanceClient, 'update').mockResolvedValue({ value: { ...record, revision: '2' }, etag: `"maintenance:${id}:2"` });

  render(<ScheduleMaintenance context={context} existing={{ value: record, etag }} />);
  expect(screen.getByRole('heading', { name: 'Edit maintenance schedule' })).toHaveFocus();
  expect(screen.getByRole('button', { name: /^Maintenance window/ })).toHaveTextContent('02:00:01 → 02:00:02 UTC');
  fireEvent.change(screen.getByLabelText('Reason'), { target: { value: 'Updated reason' } });
  const save = screen.getByRole('button', { name: 'Save schedule' });
  await waitFor(() => expect(save).toBeEnabled());
  fireEvent.click(save);
  await act(async () => {});
  expect(update).toHaveBeenCalledWith(id, {
    title: record.title, reason: 'Updated reason', scope: record.scope,
    start: '2026-11-18T02:00:01.000Z', end: '2026-11-18T02:00:02.000Z',
  }, context, expect.any(String), etag);
  expect(location.pathname + location.search).toBe(`/maintenance/${id}`);
});

it('retries an edit after its inventory changes using the same draft and request key', async () => {
  history.replaceState({}, '', `/maintenance/${id}?edit=1`);
  const etag = `"maintenance:${id}:1"`;
  vi.spyOn(sloClient, 'get').mockRejectedValue(new Error('Name not loaded'));
  vi.spyOn(sloClient, 'overview').mockResolvedValue({ items: [] } as unknown as OperationalOverview);
  const update = vi.spyOn(maintenanceClient, 'update');
  const fetch = vi.fn()
    .mockResolvedValueOnce(Response.json({ code: 'maintenance_write_retry' }, { status: 409 }))
    .mockResolvedValueOnce(Response.json({ ...record, reason: 'Updated reason', revision: '2', operation: 'edit' }, {
      headers: { ETag: `"maintenance:${id}:2"` },
    }));
  vi.stubGlobal('fetch', fetch);

  render(<ScheduleMaintenance context={context} existing={{ value: record, etag }} />);
  const reason = screen.getByLabelText('Reason');
  fireEvent.change(reason, { target: { value: 'Updated reason' } });
  const save = screen.getByRole('button', { name: 'Save schedule' });
  await waitFor(() => expect(save).toBeEnabled());
  fireEvent.click(save);
  expect(await screen.findByRole('alert')).toHaveTextContent('The affected SLO inventory changed or the scheduled start passed while saving. Try saving again.');
  expect(screen.queryByRole('button', { name: 'Read latest revision' })).toBeNull();
  expect(reason).toHaveValue('Updated reason');
  expect(save).toBeEnabled();
  await act(async () => { fireEvent.click(save); });
  expect(update).toHaveBeenCalledTimes(2);
  expect(update.mock.calls[1]).toEqual(update.mock.calls[0]);
  expect(location.pathname + location.search).toBe(`/maintenance/${id}`);
});

it('reviews a concurrent SLO swap and reason before enabling the retained draft', async () => {
  const replacement = '70000000-0000-4000-8000-000000000002';
  const etag = `"maintenance:${id}:1"`;
  const latestEtag = `"maintenance:${id}:2"`;
  const latest = { ...record, revision: '2', scope: { kind: 'slos' as const, slo_ids: [replacement] }, reason: 'Another editor changed the scope.' };
  vi.spyOn(sloClient, 'get').mockImplementation(async objectiveId => ({
    value: { id: objectiveId, display_name: objectiveId === sloId ? 'Checkout availability' : 'Orders availability' },
    etag: '',
  } as Awaited<ReturnType<typeof sloClient.get>>));
  vi.spyOn(sloClient, 'overview').mockResolvedValue({ items: [] } as unknown as OperationalOverview);
  const update = vi.spyOn(maintenanceClient, 'update')
    .mockRejectedValueOnce(new SloFailure('Schedule changed.', false, 409))
    .mockResolvedValueOnce({ value: { ...record, revision: '3' }, etag: '' });
  vi.spyOn(maintenanceClient, 'get').mockResolvedValue({
    value: latest, etag: latestEtag, serverClock: { time: Date.parse(now), receivedAt: performance.now() },
  });

  render(<ScheduleMaintenance context={context} existing={{ value: record, etag }} />);
  fireEvent.change(screen.getByLabelText('Reason'), { target: { value: 'My scheduled work.' } });
  const save = screen.getByRole('button', { name: 'Save schedule' });
  await waitFor(() => expect(save).toBeEnabled());
  fireEvent.click(save);
  fireEvent.click(await screen.findByRole('button', { name: 'Read latest revision' }));
  await screen.findByText(latest.reason);
  expect(await screen.findByText('Orders availability')).toBeVisible();
  expect(screen.getByText('Only in the current revision')).toBeVisible();
  expect(screen.getByText('Only in your draft')).toBeVisible();
  expect(screen.getByLabelText('Reason')).toHaveValue('My scheduled work.');
  expect(save).toBeDisabled();
  fireEvent.click(save);
  expect(update).toHaveBeenCalledTimes(1);

  fireEvent.click(screen.getByRole('button', { name: 'Keep my draft' }));
  expect(save).toBeEnabled();
  fireEvent.click(save);
  await act(async () => {});
  expect(update).toHaveBeenLastCalledWith(id, expect.objectContaining({ reason: 'My scheduled work.', scope: { kind: 'slos', slo_ids: [sloId] } }),
    context, expect.any(String), latestEtag);
});

it('searches the whole service catalogue and keeps scope choices exclusive', async () => {
  vi.spyOn(sloClient, 'overview').mockResolvedValue({ items: [] } as unknown as OperationalOverview);
  const serviceSearch = vi.spyOn(serviceClient, 'list').mockImplementation(async (query = {}) => ({
    next_cursor: '',
    items: query.q === 'Fleet owner 49' ? [{
      id, name: 'fleet-49', display_name: 'Fleet 49', owner: 'Fleet owner 49', version: '1', created_at: now, updated_at: now,
    }] : [],
  }));
  const scope = render(<ScheduleMaintenance context={context} />);
  const services = screen.getByRole('radio', { name: 'Selected services' });
  const objectives = screen.getByRole('radio', { name: 'Selected SLOs' });
  const all = screen.getByRole('radio', { name: 'All services' });

  fireEvent.click(services);
  expect(services).toBeChecked();
  expect(objectives).not.toBeChecked();
  const picker = screen.getByRole('combobox', { name: 'Search services' });
  picker.focus();
  fireEvent.change(picker, { target: { value: 'Fleet owner 49' } });
  await screen.findByRole('option', { name: /Fleet 49/ });
  expect(serviceSearch).toHaveBeenCalledWith({ q: 'Fleet owner 49', limit: 10 }, expect.any(AbortSignal));
  fireEvent.keyDown(picker, { key: 'ArrowDown' });
  fireEvent.keyDown(picker, { key: 'Enter' });
  expect(within(screen.getByRole('list', { name: 'Selected services' })).getByText('Fleet 49')).toBeVisible();

  fireEvent.click(all);
  expect(all).toBeChecked();
  expect(screen.queryByRole('combobox', { name: 'Search services' })).not.toBeInTheDocument();
  expect(screen.queryByRole('combobox', { name: 'Search SLOs' })).not.toBeInTheDocument();
  expect(screen.queryByRole('list', { name: 'Selected services' })).not.toBeInTheDocument();
  scope.unmount();
});

it('pages the all-services preview and distinguishes an unavailable inventory from zero impact', async () => {
  vi.spyOn(sloClient, 'overview').mockResolvedValue({ items: [] } as unknown as OperationalOverview);
  vi.mocked(maintenanceClient.preview).mockRestore();
  const preview = vi.fn(async (input: string, _request?: RequestInit) => {
    const url = new URL(input, location.origin);
    if (url.searchParams.get('cursor') === 'next') return new Response('{}', { status: 503 });
    return Response.json({
      inventory_at: now, ready: true, total: '51', next_cursor: 'next',
      items: [{ slo_id: sloId, slo_name: 'Checkout availability', service_id: id, service_name: 'Checkout' }],
    });
  });
  vi.stubGlobal('fetch', preview);

  render(<ScheduleMaintenance context={context} />);
  fireEvent.click(screen.getByRole('radio', { name: 'All services' }));
  expect(await screen.findByText('Checkout availability')).toBeVisible();
  expect(screen.getByText(/51 affected SLOs/)).toBeVisible();
  expect(screen.getByText(/Inventory as of/)).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: /Next/ }));
  expect(await screen.findByRole('alert')).toHaveTextContent('Affected SLOs could not be loaded');
  expect(screen.queryByText('0 affected SLOs')).not.toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Retry affected SLOs' })).toBeEnabled();
  expect(preview).toHaveBeenCalledTimes(2);
  for (const [index, [input, request]] of preview.mock.calls.entries()) {
    const url = new URL(input, location.origin);
    expect(url.searchParams.get('cursor')).toBe(index === 0 ? null : 'next');
    expect(url.pathname).toBe('/api/v1/scoped-maintenance/preview');
    expect(url.searchParams.get('limit')).toBe('10');
    expect(request?.method).toBe('POST');
    expect(new Headers(request?.headers).get('X-CSRF-Token')).toBe(context.csrf_token);
    expect(JSON.parse(String(request?.body))).toEqual({ scope: { kind: 'all_services' } });
  }
});

it('shows scheduled scope before displaying recorded participation at activation', async () => {
  const serviceID = '80000000-0000-4000-8000-000000000001';
  const impact = vi.mocked(maintenanceClient.impact).mockResolvedValue({
    inventory_at: now, ready: true, total: '1',
    items: [{ slo_id: sloId, slo_name: 'Checkout availability', service_id: serviceID, service_name: 'Checkout', start: record.start, end: record.end }],
  });
  vi.spyOn(serviceClient, 'get').mockResolvedValue({
    value: { id: serviceID, name: 'checkout', display_name: 'Checkout', version: '1', created_at: now, updated_at: now },
    etag: '',
  });
  await showDetail({ ...record, scope: { kind: 'services', service_ids: [serviceID] } });

  const schedule = within(screen.getByRole('region', { name: 'Schedule' }));
  expect(schedule.getByText('02:00:01').closest('time')).toHaveAttribute('datetime', record.start);
  expect(schedule.getByText('02:00:02').closest('time')).toHaveAttribute('datetime', record.end);
  expect(schedule.getByText(record.reason)).toBeVisible();
  const scope = within(screen.getByRole('region', { name: 'Scope' }));
  expect(scope.getByRole('link', { name: 'Checkout' })).toHaveAttribute('href', `/services/${serviceID}`);
  expect(screen.getByText('Recorded SLO participation will appear when the window starts.')).toBeVisible();
  expect(impact).not.toHaveBeenCalled();

  await act(async () => { vi.advanceTimersByTime(1000); });
  expect(screen.getByRole('link', { name: 'Checkout availability' })).toHaveAttribute('href', `/slos/${sloId}`);
  expect(within(screen.getByRole('region', { name: 'Affected SLOs' })).getByText('2026-11-18 02:00:01')).toHaveAttribute('datetime', record.start);
});

it('shows one affected SLO with two recorded participation periods', async () => {
  vi.mocked(maintenanceClient.impact).mockRestore();
  const member = { slo_id: sloId, slo_name: 'Checkout availability', service_id: id, service_name: 'Checkout' };
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    expect(new URL(input, location.origin).pathname).toBe(`/api/v1/scoped-maintenance/${id}/impact`);
    return Response.json({
      inventory_at: now, total: '1', ready: true,
      items: [
        { ...member, start: '2026-11-18T01:05:00Z', end: '2026-11-18T01:15:00Z' },
        { ...member, start: '2026-11-18T01:40:00Z', end: '2026-11-18T02:30:00Z' },
      ],
    });
  }));

  await showDetail({ ...record, start: '2026-11-18T01:00:00Z', end: '2026-11-18T03:00:00Z', scope: { kind: 'all_services' } });
  const impact = within(screen.getByRole('region', { name: 'Affected SLOs' }));
  expect(impact.getByText(/^1 affected SLO ·/)).toBeVisible();
  const periods = within(impact.getByRole('table'));
  expect(periods.getAllByRole('link', { name: 'Checkout availability' })).toHaveLength(2);
  expect(periods.getByText('2026-11-18 01:05:00')).toHaveAttribute('datetime', '2026-11-18T01:05:00Z');
  expect(periods.getByText('2026-11-18 01:40:00')).toHaveAttribute('datetime', '2026-11-18T01:40:00Z');
});

it('rejects an inventory preview with more rows than the reported total', async () => {
  vi.mocked(maintenanceClient.preview).mockRestore();
  const member = { slo_id: sloId, slo_name: 'Checkout availability', service_id: id, service_name: 'Checkout' };
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({
    inventory_at: now, total: '1', ready: true, items: [member, member],
  })));

  await expect(maintenanceClient.preview({ kind: 'all_services' }, context, 10, '', new AbortController().signal))
    .rejects.toThrow('invalid response');
});

it('revokes preview approval when a continuation reports a changed snapshot', async () => {
  const preview = vi.mocked(maintenanceClient.preview);
  preview.mockResolvedValueOnce({ inventory_at: now, ready: true, total: '21', items: [], next_cursor: 'next' });
  preview.mockRejectedValueOnce(new SloFailure('Inventory changed.', false, 409, 'maintenance_snapshot_changed'));
  render(<ScheduleMaintenance context={context} />);
  fireEvent.click(screen.getByRole('radio', { name: 'All services' }));
  const save = screen.getByRole('button', { name: 'Schedule maintenance' });
  await waitFor(() => expect(save).toBeEnabled());

  fireEvent.click(screen.getByRole('button', { name: /Next/ }));
  const reload = await screen.findByRole('button', { name: 'Reload affected SLOs' });
  expect(save).toBeDisabled();
  fireEvent.click(reload);
  expect(save).toBeDisabled();
  await waitFor(() => expect(save).toBeEnabled());
  expect(preview.mock.calls.map(args => args[3])).toEqual(['', 'next', '']);
});

it('retains focus on Per page during a replacement read', async () => {
  const preview = vi.mocked(maintenanceClient.preview);
  preview.mockResolvedValueOnce({ inventory_at: now, ready: true, total: '15', items: [], next_cursor: 'next' });
  preview.mockImplementationOnce(() => new Promise(() => {}));
  render(<ScheduleMaintenance context={context} />);
  fireEvent.click(screen.getByRole('radio', { name: 'All services' }));
  const size = await screen.findByRole('combobox', { name: 'Per page' });
  size.focus();
  fireEvent.change(size, { target: { value: '20' } });
  expect(size).toHaveFocus();
  expect(size).toHaveValue('20');
  expect(screen.getByRole('button', { name: /Next/ })).toBeDisabled();
});

it('offers a retry when the first preview stalls', async () => {
  vi.useFakeTimers();
  const preview = vi.mocked(maintenanceClient.preview);
  preview.mockImplementationOnce(() => new Promise(() => {}));
  render(<ScheduleMaintenance context={context} />);
  fireEvent.click(screen.getByRole('radio', { name: 'All services' }));
  const save = screen.getByRole('button', { name: 'Schedule maintenance' });
  expect(save).toBeDisabled();
  await act(async () => { vi.advanceTimersByTime(30_000); });
  expect(preview.mock.calls[0]?.[4].aborted).toBe(true);
  expect(save).toBeDisabled();
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Retry affected SLOs' })); });
  expect(save).toBeEnabled();
});

it('refreshes pending participation from page one even when the revision is unchanged', async () => {
  const impact = vi.mocked(maintenanceClient.impact);
  impact.mockResolvedValueOnce({ inventory_at: now, ready: false, total: '0', items: [] });
  await showDetail({ ...record, start: '2026-11-18T01:00:00Z', end: '2026-11-18T03:00:00Z' });
  expect(screen.getByText('Affected SLOs are being prepared.')).toBeVisible();

  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Refresh maintenance window' })); });
  expect(impact.mock.calls.map(([windowID, revision, , cursor]) => [windowID, revision, cursor])).toEqual([
    [id, '1', ''], [id, '1', ''],
  ]);
  expect(screen.getByText(/0 affected SLOs/)).toBeVisible();
});

it('rejects malformed operation values through uncertain-write recovery', async () => {
  for (const operation of [['cancel'], { toString: null }]) {
    vi.stubGlobal('fetch', vi.fn(async () => Response.json({ ...record, operation }, { headers: { ETag: `"maintenance:${id}:1"` } })));
    await expect(maintenanceClient.transition(id, 'cancel', { reason: 'Change of plan' }, context, 'key', `"maintenance:${id}:1"`))
      .rejects.toMatchObject({ uncertain: true, status: 200, message: 'The server returned an invalid response.' });
  }
});

it('requires recorded bounds for impact and rejects them in the preview', async () => {
  vi.mocked(maintenanceClient.impact).mockRestore();
  vi.mocked(maintenanceClient.preview).mockRestore();
  const member = { slo_id: sloId, service_id: id, slo_name: 'Checkout availability', service_name: 'Checkout' };
  vi.stubGlobal('fetch', vi.fn(async () => Response.json({ inventory_at: now, ready: true, total: '1', items: [member] })));
  await expect(maintenanceClient.impact(id, '1', 10, '', new AbortController().signal)).rejects.toThrow('invalid response');

  vi.stubGlobal('fetch', vi.fn(async () => Response.json({
    inventory_at: now, ready: true, total: '1', items: [{ ...member, start: record.start, end: record.end }],
  })));
  await expect(maintenanceClient.preview({ kind: 'all_services' }, context, 10, '', new AbortController().signal)).rejects.toThrow('invalid response');
});

it.each(['services', 'slos'] as const)('finds more matching %s without clearing the query or selection', async kind => {
  const first = { id, name: 'fleet-a', display_name: 'Fleet A', version: '1', created_at: now, updated_at: now };
  const second = { ...first, id: sloId, name: 'fleet-b', display_name: 'Fleet B' };
  const services = vi.spyOn(serviceClient, 'list').mockImplementation(async query => ({
    items: query?.cursor ? [second] : [first], next_cursor: query?.cursor ? '' : 'next',
  }));
  const slos = vi.spyOn(sloClient, 'overview').mockImplementation(async query => ({
    items: [{ slo: query?.cursor ? second : first }], next_cursor: query?.cursor ? '' : 'next',
  } as OperationalOverview));
  render(<ScheduleMaintenance context={context} />);
  const serviceMode = kind === 'services';
  const label = serviceMode ? 'Selected services' : 'Selected SLOs';
  const search = serviceMode ? 'Search services' : 'Search SLOs';
  fireEvent.click(screen.getByRole('radio', { name: label }));
  const picker = screen.getByRole('combobox', { name: search });
  picker.focus();
  fireEvent.change(picker, { target: { value: 'Fleet owner' } });
  fireEvent.mouseDown(await screen.findByRole('option', { name: 'Fleet A' }));
  expect(picker).toHaveValue('Fleet owner');
  expect(picker).toHaveFocus();

  fireEvent.keyDown(picker, { key: 'ArrowDown' });
  expect(screen.getByText('More matches are available. Choose Next search results.')).toBeVisible();
  fireEvent.click(screen.getByRole('button', { name: 'Next search results' }));
  fireEvent.mouseDown(await screen.findByRole('option', { name: 'Fleet B' }));
  expect(serviceMode ? services : slos).toHaveBeenLastCalledWith({ q: 'Fleet owner', limit: 10, cursor: 'next' }, expect.any(AbortSignal));
  expect(within(screen.getByRole('list', { name: label })).getAllByRole('listitem')).toHaveLength(2);
});
