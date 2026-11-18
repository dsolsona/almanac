import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import type { AuthContext } from '../src/auth/client.ts';
import { MaintenancePage } from '../src/maintenance/MaintenancePage.tsx';
import { ScheduleMaintenance } from '../src/maintenance/ScheduleMaintenance.tsx';
import { maintenanceClient, type MaintenanceWindow } from '../src/maintenance/client.ts';
import { sloClient, type OperationalOverview } from '../src/slos/client.ts';

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
  id, title: 'Upgrade Checkout', reason: 'Planned database upgrade', slo_ids: [sloId],
  revision: '1', operation: 'create', tombstone: false, start: '2026-11-18T02:00:01Z', end: '2026-11-18T02:00:02Z',
  created_at: now, updated_at: now, creator_id: id, actor_id: id, audit_id: id,
};
const dialogMethod = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'showModal');

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
});

it('checks the early-end time at submission and clears its validation error on edit', async () => {
  await showDetail({ ...record, start: '2026-11-18T01:00:00Z', end: '2026-11-18T03:00:00Z' });
  const submit = vi.fn(async () => new Response('{}', { status: 503 }));
  vi.stubGlobal('fetch', submit);
  fireEvent.click(screen.getByRole('button', { name: 'End maintenance early' }));
  const time = screen.getByLabelText('New end time (UTC)');
  fireEvent.change(time, { target: { value: '02:01' } });
  fireEvent.change(screen.getByLabelText('Reason for this change'), { target: { value: 'Traffic switch finished.' } });
  act(() => vi.advanceTimersByTime(120_000));
  fireEvent.click(screen.getByRole('button', { name: 'End maintenance' }));
  expect(submit).not.toHaveBeenCalled();
  expect(screen.getByRole('alert')).toHaveTextContent('Choose a future time before the scheduled end.');
  fireEvent.change(time, { target: { value: '02:03' } });
  expect(screen.queryByRole('alert')).not.toBeInTheDocument();
});

it('keeps a cancellation draft and blocks confirmation if the latest window has started', async () => {
  await showDetail({ ...record, start: '2026-11-18T03:00:00Z', end: '2026-11-18T04:00:00Z' });
  const newer = { ...record, revision: '2', operation: 'edit', start: '2026-11-18T01:59:00Z', end: '2026-11-18T04:00:00Z' };
  vi.stubGlobal('fetch', vi.fn(async (_url: string, request: RequestInit) => request.method === 'POST'
    ? new Response(JSON.stringify({ code: 'maintenance_conflict' }), { status: 409 })
    : new Response(JSON.stringify(newer), { headers: { ETag: `"maintenance:${id}:2"` } })));
  fireEvent.click(screen.getByRole('button', { name: 'Cancel scheduled maintenance' }));
  const reason = screen.getByLabelText('Reason for this change');
  fireEvent.change(reason, { target: { value: 'Postpone the work.' } });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Cancel maintenance' })); });
  await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Read latest revision' })); });
  expect(reason).toHaveValue('Postpone the work.');
  expect(screen.getByRole('button', { name: 'Cancel maintenance' })).toBeDisabled();
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
  fireEvent.focus(screen.getByRole('combobox', { name: 'Affected SLOs' }));
  fireEvent.mouseDown(await screen.findByRole('option', { name: /Checkout availability/ }));
  fireEvent.change(screen.getByLabelText('Start date'), { target: { value: '2026-11-20' } });
  fireEvent.change(screen.getByLabelText('Start time (UTC)'), { target: { value: '02:00' } });
  fireEvent.click(screen.getByRole('button', { name: 'Schedule maintenance' }));
  expect(create).toHaveBeenCalledOnce();

  form.unmount();
  history.replaceState({}, '', '/alerts');
  await act(async () => { finish?.({ value: record, etag: '' }); });
  expect(location.pathname).toBe('/alerts');
});
