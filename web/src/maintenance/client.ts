import type { components } from '../api/generated.ts';
import type { AuthContext } from '../auth/client.ts';
import { request, write } from '../slos/client.ts';

type Schemas = components['schemas'];
export type MaintenanceWindow = Schemas['MaintenanceWindow'];
export type MaintenanceWrite = Schemas['MaintenanceWrite'];
export type MaintenancePage = Schemas['MaintenancePage'];

const path = '/api/v1/maintenance';

export const maintenanceClient = {
  list: (limit: number, cursor: string, signal: AbortSignal) =>
    request<MaintenancePage>(`${path}?${new URLSearchParams({ limit: String(limit), ...(cursor ? { cursor } : {}) })}`, { signal }),
  get: (id: string, revision: string, signal: AbortSignal) =>
    request<MaintenanceWindow>(`${path}/${id}${revision ? `?${new URLSearchParams({ revision })}` : ''}`, { signal }),
  update: (id: string, body: MaintenanceWrite, context: AuthContext, key: string, etag: string) =>
    request<MaintenanceWindow>(`${path}/${id}`, { ...write(context, body, key, etag), method: 'PUT' }, true),
  create: (body: MaintenanceWrite, context: AuthContext, key: string) =>
    request<MaintenanceWindow>(path, write(context, body, key), true),
};
