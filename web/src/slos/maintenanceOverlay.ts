import type { components } from '../api/generated.ts';
import type { MaintenanceBand } from '../components/charts/SloHistoryChart.tsx';
import { validScopedWindow } from '../maintenance/client.ts';
import { maintenanceTimeKey, requestValidated, SloFailure, sloClient } from './client.ts';

type Overflow = components['schemas']['MaintenanceOverflow'];
type ScopedOverlay = components['schemas']['ScopedMaintenanceTimelinePage'];

export type OverlayPage = {
  items: MaintenanceBand[];
  pending: boolean;
  source: 'legacy' | 'scoped';
  next_cursor?: string;
  overflow?: Overflow;
};

const pageLimit = 200;
const record = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

function validPage(value: unknown, id: string, start: string, end: string): value is ScopedOverlay {
  if (!record(value) || typeof value.pending !== 'boolean' || !Array.isArray(value.items) || value.items.length > pageLimit ||
    !(value.next_cursor === undefined || typeof value.next_cursor === 'string' && value.next_cursor.length <= 512)) return false;
  const rangeStart = maintenanceTimeKey(start);
  const rangeEnd = maintenanceTimeKey(end);
  if (!rangeStart || !rangeEnd || rangeStart >= rangeEnd) return false;
  const overflow = value.overflow;
  if (overflow !== undefined) {
    if (!record(overflow) || !value.next_cursor || typeof overflow.count !== 'string' || !/^[1-9][0-9]*$/.test(overflow.count)) return false;
    const first = maintenanceTimeKey(overflow.start);
    const last = maintenanceTimeKey(overflow.end);
    if (!first || !last || first < rangeStart || last > rangeEnd || first > last) return false;
  }
  return value.items.every(item => {
    if (!record(item) || !validScopedWindow(item.window) || item.window.tombstone) return false;
    if (item.window.scope.kind === 'slos' && !item.window.scope.slo_ids.includes(id)) return false;
    const first = maintenanceTimeKey(item.start);
    const last = maintenanceTimeKey(item.end);
    const scheduledStart = maintenanceTimeKey(item.window.start)!;
    const scheduledEnd = maintenanceTimeKey(item.window.end)!;
    return first !== undefined && last !== undefined && first < last && first < rangeEnd && rangeStart < last &&
      scheduledStart <= first && last <= scheduledEnd;
  });
}

export async function readSloMaintenance(id: string, start: string, end: string, cursor: string, signal: AbortSignal, source?: OverlayPage['source']): Promise<OverlayPage> {
  if (source !== 'legacy') {
    const params = new URLSearchParams({ start, end, limit: String(pageLimit), ...(cursor ? { cursor } : { include_overflow: 'true' }) });
    try {
      const { value } = await requestValidated(`/api/v1/slos/${id}/scoped-maintenance?${params}`, { signal }, {
        status: 200, validate: (data): data is ScopedOverlay => validPage(data, id, start, end),
      });
      return {
        ...value, source: 'scoped',
        items: value.items.map(({ window, start, end }) => ({ id: window.id, title: window.title, reason: window.reason, start, end })),
      };
    } catch (error) {
      // An older deployment has no scoped route. A scoped page cursor must
      // stay on that endpoint, and an API-level rejection stays visible.
      if (source || cursor || signal.aborted || !(error instanceof SloFailure) || error.status !== 404 || error.code) throw error;
    }
  }
  const value = await sloClient.maintenance(id, start, end, cursor, signal);
  return { ...value, pending: false, source: 'legacy' };
}
