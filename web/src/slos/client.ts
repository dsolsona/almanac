import type { components } from '../api/generated.ts';
import type { AuthContext } from '../auth/client.ts';

export type Service = components['schemas']['Service'];
export type Slo = components['schemas']['Slo'];
export type Evaluation = components['schemas']['SloEvaluation'];
export type CreateSlo = components['schemas']['SloCreateRequest'];
export class SloFailure extends Error {
  constructor(message: string, readonly uncertain = false, readonly status = 0) { super(message); }
}
const record = (x: unknown): x is Record<string, unknown> => typeof x === 'object' && x !== null && !Array.isArray(x);
const decimal = (x: unknown) => x === null || typeof x === 'string' && /^-?\d+(?:\.\d+)?$/.test(x) && x.length <= 128;
function validEvaluation(x: Record<string, unknown>): boolean {
  if (!record(x.window) || !record(x.quality) || !record(x.performance) || !record(x.objective) || !record(x.timeline)) return false;
  const w=x.window, q=x.quality, p=x.performance, t=x.timeline;
  const timestamp=(v:unknown) => typeof v === 'string' && /Z$/.test(v) && Number.isFinite(Date.parse(v));
  const integer=(v:unknown) => typeof v === 'number' && Number.isSafeInteger(v) && v >= 0;
  return x.snapshot_kind === 'current_evaluation' && timestamp(x.requested_at) && timestamp(x.effective_end) &&
    timestamp(w.effective_start) && timestamp(w.end) && timestamp(t.start) && timestamp(t.end) && integer(t.cadence_seconds) && typeof t.cadence_seconds === 'number' && t.cadence_seconds > 0 && t.max_intervals === 1500 &&
    ['complete','partial','no_data','zero_traffic','expired_history','not_applicable'].includes(String(q.state)) && ['unknown','fresh','stale'].includes(String(q.freshness)) &&
    ['expected_intervals','observed_intervals','known_empty_intervals','missing_intervals','zero_traffic_intervals','expired_intervals'].every(k=>integer(q[k])) && decimal(q.coverage) &&
    ['met','violated','unknown','not_applicable'].includes(String(p.state)) && (p.observed_state===null || p.observed_state==='met' || p.observed_state==='violated') && typeof p.provisional==='boolean' &&
    ['good','total','bad','attainment','allowed_budget','remaining_budget','consumed_fraction','remaining_fraction','burn'].every(k=>decimal(p[k])) && p.budget_unit==='events' &&
    Array.isArray(t.items) && t.items.length<=1500 && t.items.every(item=>record(item) && timestamp(item.start) && timestamp(item.end) &&
      ['observed','zero_traffic','known_empty','missing','expired','not_applicable'].includes(String(item.state)) && ['good','total','attainment','burn'].every(k=>decimal(item[k])) &&
      (item.state==='observed' ? typeof item.attainment==='string' : item.attainment===null));
}
const messages: Record<string, string> = {
  conflict: 'This name or version conflicts with an existing record. Review it and try again.',
  invalid_request: 'Review the fields, target and window alignment, then try again.',
  not_found: 'The requested record was not found.', forbidden: 'Your role does not allow this operation.',
  unauthorized: 'Your session has expired. Sign in again.', unauthenticated: 'Your session has expired. Sign in again.',
};
const fieldMessages: Record<string, string> = {
  'target:target_invalid': 'Target must be from 0% up to but not including 100%.',
  'rolling_window_seconds:window_invalid': 'Enter a positive window within the supported limit and aligned to the SLI collection interval.',
  'evaluation_valid_from:historical_start_invalid': 'The eligible retained range changed. Refresh the eligible range and review the historical start before retrying.',
  'timeline_start:timeline_range_invalid': 'Choose both aligned timeline bounds within the objective window, with at most 1,500 intervals.',
  'at:evaluation_time_invalid': 'Choose a valid evaluation time that is not in the future.',
};
async function request<T>(path: string, init: RequestInit = {}, mutation = false): Promise<{ value: T; etag: string }> {
  let response: Response;
  try { response = await fetch(path, { ...init, credentials: 'same-origin', mode: 'same-origin', cache: 'no-store', redirect: 'error' }); }
  catch { throw new SloFailure(mutation ? 'The request may have succeeded. Check the list before deliberately retrying the same request.' : 'Almanac could not be reached.', mutation); }
  let data: unknown;
  try {
    const limit = 4 * 1024 * 1024;
    if (!response.body || Number(response.headers.get('Content-Length')) > limit) throw new Error('large response');
    const reader = response.body.getReader(); const chunks: Uint8Array[] = []; let size = 0;
    try { while (true) { const item = await reader.read(); if (item.done) break; size += item.value.length; if (size > limit) { await reader.cancel(); throw new Error('large response'); } chunks.push(item.value); } }
    finally { reader.releaseLock(); }
    const bytes = new Uint8Array(size); let offset = 0; for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
    data = JSON.parse(new TextDecoder().decode(bytes));
  } catch { throw new SloFailure(mutation ? 'The request may have succeeded, but its response could not be confirmed. Check the list before retrying.' : 'The server returned an invalid response.', mutation, response.status); }
  if (!response.ok) {
    const code = typeof data === 'object' && data !== null && 'code' in data && typeof data.code === 'string' ? data.code : '';
    const errors = record(data) && Array.isArray(data.field_errors) && data.field_errors.length <= 16 ? data.field_errors : [];
    const guidance = errors.filter(record).map(item => fieldMessages[`${item.field}:${item.code}`]).filter(Boolean).join(' ');
    throw new SloFailure(guidance || messages[code] || 'The request could not be completed. Try again.', mutation && response.status >= 500, response.status);
  }
  const expected = init.method === 'POST' ? 201 : 200;
  if (response.status !== expected) throw new SloFailure('The server returned an unexpected response.', mutation, response.status);
  if (!data || typeof data !== 'object') throw new SloFailure('The server returned an invalid response.', mutation, response.status);
  const value = data as Record<string, unknown>;
  const isService = path.startsWith('/api/v1/services');
  const isEvaluation = path.includes('/evaluation');
  const pathname = path.split('?')[0]!;
  const pathID = /^\/api\/v1\/(?:services|slos)\/([0-9a-f-]{36})(?:\/evaluation)?$/.exec(pathname)?.[1];
  const isList = !pathID && init.method !== 'POST';
  const uuid = (v: unknown) => typeof v === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(v);
  const version = (v: unknown) => typeof v === 'string' && /^[1-9][0-9]{0,18}$/.test(v) && (v.length < 19 || v <= '9223372036854775807');
  const resource = (item: unknown): item is Record<string, unknown> => typeof item === 'object' && item !== null &&
    uuid((item as Record<string, unknown>).id) && typeof (item as Record<string, unknown>).name === 'string' && (item as Record<string, string>).name.length > 0 &&
    typeof (item as Record<string, unknown>).display_name === 'string' && (item as Record<string, string>).display_name.length > 0 && version((item as Record<string, unknown>).version);
  const slo = (item: unknown) => resource(item) && typeof item.service_display_name === 'string' && typeof item.sli_display_name === 'string' &&
    typeof item.target === 'string' && /^(?:0(?:\.\d{1,12})?)$/.test(item.target) && version(item.definition_revision) &&
    typeof item.rolling_window_seconds === 'number' && Number.isSafeInteger(item.rolling_window_seconds) && item.rolling_window_seconds > 0 &&
    typeof item.cadence_seconds === 'number' && Number.isSafeInteger(item.cadence_seconds) && item.cadence_seconds > 0 &&
    item.rolling_window_seconds % item.cadence_seconds === 0 && uuid(item.service_id) && uuid(item.sli_id);
  const valid = isEvaluation ? validEvaluation(value) && record(value.objective) && value.objective.slo_id === pathID
    : isList ? Array.isArray(value.items) && value.items.length <= 200 && typeof value.next_cursor === 'string' && value.next_cursor.length <= 512 && value.items.every(isService ? resource : slo)
      : (isService ? resource(value) : slo(value)) && (!pathID || value.id === pathID);
  if (!valid) throw new SloFailure('The server returned an invalid response.', mutation, response.status);
  if (!isEvaluation && !isList) {
    const tag = response.headers.get('ETag');
    if (tag !== `"${isService ? 'service' : 'slo'}:${value.id}:${value.version}"`)
      throw new SloFailure(mutation ? 'The request may have succeeded, but its version could not be confirmed. Check the record before retrying.' : 'The record version could not be confirmed.', mutation, response.status);
  }
  return { value: data as T, etag: response.headers.get('ETag') ?? '' };
}
function write(context: AuthContext, body: unknown, key?: string, etag?: string): RequestInit {
  return { method: etag ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token, ...(key ? { 'Idempotency-Key': key } : {}), ...(etag ? { 'If-Match': etag } : {}) }, body: JSON.stringify(body) };
}
export const sloClient = {
  services: (cursor = '', signal?: AbortSignal) => request<{items: Service[]; next_cursor: string}>(`/api/v1/services?${new URLSearchParams({limit:'50', ...(cursor ? {cursor} : {})})}`, {signal}).then(x => x.value),
  service: (id: string, signal?: AbortSignal) => request<Service>(`/api/v1/services/${id}`, {signal}),
  createService: (body: {name:string; display_name:string}, context: AuthContext, key: string) => request<Service>('/api/v1/services', write(context, body, key), true).then(x => x.value),
  updateService: (id: string, name: string, etag: string, context: AuthContext) => request<Service>(`/api/v1/services/${id}`, write(context, {display_name:name}, undefined, etag), true),
  list: (cursor = '', signal?: AbortSignal) => request<{items:Slo[]; next_cursor:string}>(`/api/v1/slos?${new URLSearchParams({limit:'50', ...(cursor ? {cursor} : {})})}`, {signal}).then(x => x.value),
  get: (id: string, signal?: AbortSignal) => request<Slo>(`/api/v1/slos/${id}`, {signal}),
  create: (body: CreateSlo, context: AuthContext, key: string) => request<Slo>('/api/v1/slos', write(context, body, key), true).then(x => x.value),
  update: (id: string, name: string, etag: string, context: AuthContext) => request<Slo>(`/api/v1/slos/${id}`, write(context, {display_name:name}, undefined, etag), true),
  evaluation: (id: string, signal?: AbortSignal) => request<Evaluation>(`/api/v1/slos/${id}/evaluation`, {signal}).then(x => x.value),
};
