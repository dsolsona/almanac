import type { components } from '../api/generated.ts';
import type { AuthContext } from '../auth/client.ts';
import { requestValidated, write } from '../slos/client.ts';

type Schemas = components['schemas'];
export type MaintenanceSelector = Schemas['MaintenanceScope'];
export type MaintenanceWindow = Schemas['ScopedMaintenanceWindow'];
export type MaintenanceWrite = Schemas['ScopedMaintenanceWrite'];
export type MaintenancePage = Schemas['ScopedMaintenancePage'];
export type MaintenanceAction = 'cancel' | 'end' | 'withdraw';
export type ImpactPage = Schemas['MaintenanceImpactPage'];

const path = '/api/v1/scoped-maintenance';

function paging(limit: number, cursor: string, revision = '') {
  return new URLSearchParams({ limit: String(limit), ...(cursor ? { cursor } : {}), ...(revision ? { revision } : {}) });
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);
const isUUID = (value: unknown): value is string =>
  typeof value === 'string' && /^[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}$/.test(value);
const isRevision = (value: unknown): value is string =>
  typeof value === 'string' && /^[1-9][0-9]{0,18}$/.test(value);
const timestampKey = (value: unknown) => {
  if (typeof value !== 'string' || !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,6})?Z$/.test(value) || !Number.isFinite(Date.parse(value))) return undefined;
  return value.slice(0, 19) + value.slice(20, -1).padEnd(6, '0');
};
const validBounds = (start: unknown, end: unknown) => {
  const first = timestampKey(start);
  const last = timestampKey(end);
  return first !== undefined && last !== undefined && first < last;
};
const cursorValid = (cursor: unknown) => cursor === undefined || typeof cursor === 'string' && cursor.length <= 512;
const validIDs = (ids: unknown) =>
  Array.isArray(ids) && ids.length > 0 && ids.length <= 100 && ids.every(isUUID) && new Set(ids).size === ids.length;
const validSelector = (scope: unknown): scope is MaintenanceSelector => {
  if (!isRecord(scope)) return false;
  switch (scope.kind) {
    case 'services': return validIDs(scope.service_ids) && !('slo_ids' in scope);
    case 'slos': return validIDs(scope.slo_ids) && !('service_ids' in scope);
    case 'all_services': return !('slo_ids' in scope) && !('service_ids' in scope);
    default: return false;
  }
};

export function validScopedWindow(value: unknown): value is MaintenanceWindow {
  return isRecord(value) && isUUID(value.id) && isRevision(value.revision) && validSelector(value.scope) &&
    typeof value.title === 'string' && value.title.length > 0 && typeof value.reason === 'string' &&
    typeof value.operation === 'string' && ['create', 'edit', 'end', 'cancel', 'withdraw'].includes(value.operation) && typeof value.tombstone === 'boolean' &&
    validBounds(value.start, value.end) && timestampKey(value.created_at) !== undefined && timestampKey(value.updated_at) !== undefined &&
    isUUID(value.creator_id) && isUUID(value.actor_id) && isUUID(value.audit_id);
}

function windowContract(status: number, id?: string, revision?: string) {
  return { status, validate: (value: unknown, headers: Headers): value is MaintenanceWindow =>
    validScopedWindow(value) && (!id || value.id === id) && (!revision || value.revision === revision) &&
    headers.get('ETag') === `"maintenance:${value.id}:${value.revision}"`,
  };
}

function impactContract(limit: number, kind: 'preview' | 'impact') {
  return { status: 200, validate: (value: unknown): value is ImpactPage => {
    if (!isRecord(value) || timestampKey(value.inventory_at) === undefined || typeof value.ready !== 'boolean' ||
      typeof value.total !== 'string' || !/^(0|[1-9][0-9]{0,18})$/.test(value.total) ||
      !Array.isArray(value.items) || value.items.length > limit || !cursorValid(value.next_cursor) ||
      !value.items.every(item =>
        isRecord(item) && isUUID(item.slo_id) && isUUID(item.service_id) &&
        typeof item.slo_name === 'string' && typeof item.service_name === 'string' &&
        (kind === 'preview' ? item.start === undefined && item.end === undefined : validBounds(item.start, item.end)))) return false;
    const count = kind === 'preview' ? value.items.length : new Set(value.items.map(item => item.slo_id)).size;
    return BigInt(count) <= BigInt(value.total);
  } };
}

export const maintenanceClient = {
  list: (limit: number, cursor: string, signal: AbortSignal) =>
    requestValidated(`${path}?${paging(limit, cursor)}`, { signal }, {
      status: 200, validate: (value: unknown): value is MaintenancePage =>
        isRecord(value) && Array.isArray(value.items) && value.items.length <= limit && value.items.every(validScopedWindow) && cursorValid(value.next_cursor),
    }),
  get: (id: string, revision: string, signal: AbortSignal) =>
    requestValidated(`${path}/${id}${revision ? `?${new URLSearchParams({ revision })}` : ''}`, { signal }, windowContract(200, id, revision)),
  update: (id: string, body: MaintenanceWrite, context: AuthContext, key: string, etag: string) =>
    requestValidated(`${path}/${id}`, { ...write(context, body, key, etag), method: 'PUT' }, windowContract(200, id), true),
  create: (body: MaintenanceWrite, context: AuthContext, key: string) =>
    requestValidated(path, write(context, body, key), windowContract(201), true),
  transition: (id: string, action: MaintenanceAction, body: { reason: string; end?: string }, context: AuthContext, key: string, etag: string) =>
    requestValidated(`${path}/${id}/${action}`, { ...write(context, body, key, etag), method: 'POST' }, windowContract(200, id), true),
  preview: (scope: MaintenanceSelector, context: AuthContext, limit: number, cursor: string, signal: AbortSignal) =>
    requestValidated(`${path}/preview?${paging(limit, cursor)}`, { ...write(context, { scope }), signal }, impactContract(limit, 'preview')).then(result => result.value),
  impact: (id: string, revision: string, limit: number, cursor: string, signal: AbortSignal) =>
    requestValidated(`${path}/${id}/impact?${paging(limit, cursor, revision)}`, { signal }, impactContract(limit, 'impact')).then(result => result.value),
};
