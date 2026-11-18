import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { AuthContext } from '../src/auth/client.ts';
import { MaintenancePage } from '../src/maintenance/MaintenancePage.tsx';
import { ScheduleMaintenance } from '../src/maintenance/ScheduleMaintenance.tsx';
import { maintenanceClient, type MaintenanceWindow } from '../src/maintenance/client.ts';
import { SloFailure, sloClient, type OperationalOverview } from '../src/slos/client.ts';
import { serviceClient } from '../src/services/client.ts';

vi.mock('../src/components/ui/index.ts', async importActual => {
  const controls = await importActual<typeof import('../src/components/ui/index.ts')>();
  return {
    ...controls,
    DatePicker: ({ label, value, onValueChange }: { label: string; value: string; onValueChange: (value: string) => void }) =>
      <input aria-label={label} type="date" value={value} onChange={event => onValueChange(event.target.value)} />,
  };
});

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

beforeEach(() => {
  const empty = { inventory_at: now, ready: true, total: '0', items: [] };
  vi.spyOn(maintenanceClient, 'impact').mockResolvedValue(empty);
  vi.spyOn(maintenanceClient, 'preview').mockResolvedValue(empty);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  if (!dialogMethod) Reflect.deleteProperty(HTMLDialogElement.prototype, 'showModal');
  vi.useRealTimers();
  history.replaceState({}, '', '/');
});

async function showDetail(window: MaintenanceWindow) {
  vi.useFakeTimers({ toFake: ['Date', 'performance', 'setTimeout', 'clearTimeout'] });
  vi.setSystemTime(new Date(now));
  if (!dialogMethod) Object.defineProperty(HTMLDialogElement.prototype, 'showModal', {
    configurable: true, value(this: HTMLDialogElement) { this.setAttribute('open', ''); },
  });
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
  get.mockResolvedValueOnce({ value: cancelled, etag: `"maintenance:${id}:2"` });
  vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('Response lost'); }));
  fireEvent.click(screen.getByRole('button', { name: 'Cancel scheduled maintenance' }));
  const reason = screen.getByLabelText('Reason for this change');
  fireEvent.change(reason, { target: { value: 'Postpone the work.' } });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Cancel maintenance' })); });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Read latest revision' })); });
  expect(reason).toHaveValue('Postpone the work.');
  expect(screen.getByText('Cancelled', { exact: true })).toBeVisible();
  expect(screen.getByRole('button', { name: 'Cancel maintenance' })).toBeDisabled();
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
  fireEvent.change(screen.getByLabelText('Start date'), { target: { value: '2026-11-20' } });
  fireEvent.change(screen.getByLabelText('Start time (UTC)'), { target: { value: '02:00' } });
  const save = screen.getByRole('button', { name: 'Schedule maintenance' });
  await waitFor(() => expect(save).toBeEnabled());
  fireEvent.click(save);
  expect(create).toHaveBeenCalledOnce();

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
  const preview = vi.fn(async (input: string, request?: RequestInit) => {
    const url = new URL(input, location.origin);
    expect(url.pathname).toBe('/api/v1/scoped-maintenance/preview');
    expect(url.searchParams.get('limit')).toBe('10');
    expect(request?.method).toBe('POST');
    expect(new Headers(request?.headers).get('X-CSRF-Token')).toBe(context.csrf_token);
    expect(JSON.parse(String(request?.body))).toEqual({ scope: { kind: 'all_services' } });
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
});
