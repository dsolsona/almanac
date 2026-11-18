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

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.useRealTimers();
  history.replaceState({}, '', '/');
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
