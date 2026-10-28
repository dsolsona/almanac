import type { components } from '../api/generated.ts';
import type { AuthContext } from '../auth/client.ts';

export type Sli = components['schemas']['Sli'];
export type SliObservation = components['schemas']['SliObservation'];
export type SliObservationPage = components['schemas']['SliObservationPage'];
export type SliCreateRequest = components['schemas']['SliCreateRequest'];

export class SliFailure extends Error {
  constructor(readonly code: string, message: string, readonly status = 0, readonly uncertain = false) {
    super(message);
    this.name = 'SliFailure';
  }
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const MAX_INT64 = '9223372036854775807';
const messages: Record<string, string> = {
  unauthorized: 'Your session has expired. Check your session and try again.',
  forbidden: 'Your role does not allow this operation.',
  not_found: 'The requested SLI or datasource was not found.',
  conflict: 'The SLI changed. Refresh it and review the current version.',
  reassignment_pending: 'This agent is completing a datasource move. Wait for it to finish or cancel the move before changing collection settings.',
  reassignment_settings_conflict: 'Save connection changes before changing the assigned agent.',
  invalid_request: 'The SLI request input is invalid. Review the fields and try again.',
  unsupported_measurement: 'This measurement kind or component mode is not supported by the current collector.',
  measurement_conflict: 'The datasource cadence or agent assignment cannot change while this SLI uses it.',
  agent_unavailable: 'The assigned agent cannot currently accept this SLI configuration.',
  source_secret_unavailable: 'The datasource credential is unavailable. Update its credentials before collecting.',
  sli_unavailable: 'SLI management is temporarily unavailable.',
};

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function exactKeys(value: Record<string, unknown>, keys: readonly string[]): boolean {
  return Object.keys(value).length === keys.length && keys.every((key) => Object.hasOwn(value, key));
}

function revision(value: unknown, allowZero = false): value is string {
  return typeof value === 'string' && (allowZero ? /^(0|[1-9][0-9]*)$/ : /^[1-9][0-9]*$/).test(value)
    && (value.length < MAX_INT64.length || value.length === MAX_INT64.length && value <= MAX_INT64);
}

function timestamp(value: unknown): value is string {
  return typeof value === 'string' && value.length <= 40 && Number.isFinite(Date.parse(value)) && /Z$/.test(value);
}

function validCount(value: unknown): value is string {
  if (typeof value !== 'string' || value.length > 40) return false;
  const match = /^(0|[1-9][0-9]*)(?:\.([0-9]{1,12}))?$/.exec(value);
  return Boolean(match && match[1]!.length <= 26);
}

function scaledCount(value: string): bigint {
  const [whole, fraction = ''] = value.split('.');
  return BigInt(whole!) * 1_000_000_000_000n + BigInt((fraction + '0'.repeat(12)).slice(0, 12));
}

function validObservation(value: unknown): value is SliObservation {
  if (!object(value) || !exactKeys(value, ['observation_id', 'measurement_revision', 'interval_start', 'interval_end', 'outcome', 'good_count', 'total_count', 'error_code', 'missing_components', 'recorded_at'])
    || typeof value.observation_id !== 'string' || !UUID.test(value.observation_id) || !revision(value.measurement_revision)
    || !timestamp(value.interval_start) || !timestamp(value.interval_end) || !timestamp(value.recorded_at)
    || Date.parse(value.interval_end) <= Date.parse(value.interval_start)
    || !['observed', 'complete_empty', 'incomplete', 'failed'].includes(String(value.outcome))
    || !Array.isArray(value.missing_components) || value.missing_components.length > 2
    || !value.missing_components.every((component) => component === 'good' || component === 'total')
    || new Set(value.missing_components).size !== value.missing_components.length) return false;
  if (value.error_code !== null && !['authentication', 'incomplete', 'network', 'query', 'rate_limited', 'tls', 'timeout', 'unsupported', 'assignment_revoked', 'deadline'].includes(String(value.error_code))) return false;
  if (value.outcome === 'observed') {
    return validCount(value.good_count) && validCount(value.total_count) && value.error_code === null && value.missing_components.length === 0
      && scaledCount(value.good_count) <= scaledCount(value.total_count);
  }
  if (value.good_count !== null || value.total_count !== null) return false;
  if (value.outcome === 'complete_empty') return value.error_code === null && value.missing_components.length === 0;
  if (value.outcome === 'incomplete') return value.error_code === null && value.missing_components.length > 0;
  return value.outcome === 'failed' && value.error_code !== null && value.missing_components.length === 0;
}

function validSli(value: unknown): value is Sli {
  if (!object(value) || !exactKeys(value, ['id', 'name', 'display_name', 'version', 'measurement_revision', 'datasource_id', 'datasource_version', 'agent_id', 'agent_reachability', 'collection_progress', 'measurement_kind', 'component_mode', 'good_query', 'total_query', 'polling_interval_seconds', 'settling_delay_seconds', 'effective_from', 'activated_at', 'activation_state', 'freshness', 'latest_observation', 'last_attempt_at', 'last_observation_at', 'next_expected_execution_at', 'created_at', 'updated_at'])) return false;
  return typeof value.id === 'string' && UUID.test(value.id) && typeof value.name === 'string' && value.name.length > 0
    && typeof value.display_name === 'string' && value.display_name.length > 0 && revision(value.version) && revision(value.measurement_revision)
    && typeof value.datasource_id === 'string' && UUID.test(value.datasource_id) && revision(value.datasource_version)
    && typeof value.agent_id === 'string' && UUID.test(value.agent_id)
    && ['connected', 'unreachable', 'incompatible', 'disabled', 'unreported'].includes(String(value.agent_reachability))
    && ['not_started', 'idle', 'running', 'blocked'].includes(String(value.collection_progress))
    && value.measurement_kind === 'event_counts' && value.component_mode === 'interval_count'
    && typeof value.good_query === 'string' && typeof value.total_query === 'string'
    && typeof value.polling_interval_seconds === 'number' && Number.isInteger(value.polling_interval_seconds) && value.polling_interval_seconds >= 15
    && value.settling_delay_seconds === 60 && timestamp(value.effective_from) && (value.activated_at === null || timestamp(value.activated_at))
    && ['preparing', 'scheduled', 'active', 'blocked'].includes(String(value.activation_state))
    && ['unknown', 'fresh', 'stale'].includes(String(value.freshness))
    && (value.latest_observation === null || validObservation(value.latest_observation))
    && (value.last_attempt_at === null || timestamp(value.last_attempt_at)) && (value.last_observation_at === null || timestamp(value.last_observation_at))
    && (value.next_expected_execution_at === null || timestamp(value.next_expected_execution_at)) && timestamp(value.created_at) && timestamp(value.updated_at);
}

function validSliPage(value: unknown): value is { items: Sli[]; next_cursor: string } {
  return object(value) && exactKeys(value, ['items', 'next_cursor']) && Array.isArray(value.items) && value.items.length <= 200
    && value.items.every(validSli) && typeof value.next_cursor === 'string' && value.next_cursor.length <= 512;
}

function validObservationPage(value: unknown): value is SliObservationPage {
  return object(value) && exactKeys(value, ['items', 'next_cursor', 'earliest_queryable_interval_start', 'inspected_start', 'inspected_end', 'missing_intervals']) && Array.isArray(value.items) && value.items.length <= 200
    && value.items.every(validObservation) && typeof value.next_cursor === 'string' && value.next_cursor.length <= 512
    && timestamp(value.earliest_queryable_interval_start) && ((value.inspected_start === null && value.inspected_end === null) || (timestamp(value.inspected_start) && timestamp(value.inspected_end) && Date.parse(value.inspected_start) < Date.parse(value.inspected_end)))
    && Array.isArray(value.missing_intervals) && value.missing_intervals.length <= 200 && value.missing_intervals.every((range: unknown) => object(range) && exactKeys(range, ['interval_start', 'interval_end']) && timestamp(range.interval_start) && timestamp(range.interval_end) && Date.parse(range.interval_start) < Date.parse(range.interval_end));
}

async function readJSON(response: Response, maxBytes = 512 * 1024): Promise<unknown> {
  const declared = Number(response.headers.get('Content-Length'));
  if (Number.isFinite(declared) && declared > maxBytes || !response.body) throw new SliFailure('invalid_response', 'The server returned an invalid SLI response.');
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > maxBytes) { await reader.cancel(); throw new SliFailure('invalid_response', 'The server returned an invalid SLI response.'); }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
  try { return JSON.parse(new TextDecoder().decode(bytes)) as unknown; } catch { throw new SliFailure('invalid_response', 'The server returned an invalid SLI response.'); }
}

async function request<T>(path: string, init: RequestInit, expected: number, validate: (value: unknown) => value is T): Promise<{ value: T; response: Response }> {
  let response: Response;
  try { response = await fetch(path, { ...init, credentials: 'same-origin', mode: 'same-origin', cache: 'no-store', redirect: 'error' }); }
  catch {
    if (init.signal?.aborted) throw new SliFailure('request_canceled', 'The SLI request was canceled.');
    throw new SliFailure('request_failed', 'SLI management could not be reached.');
  }
  const body = await readJSON(response);
  if (response.status !== expected) {
    const code = object(body) && typeof body.code === 'string' ? body.code : 'sli_unavailable';
    throw new SliFailure(code, messages[code] ?? messages.sli_unavailable!, response.status);
  }
  if (!validate(body)) throw new SliFailure('invalid_response', 'The server returned an invalid SLI response.');
  return { value: body, response };
}

function safeID(value: string): string {
  if (!UUID.test(value)) throw new SliFailure('invalid_request', 'The SLI identifier is invalid.');
  return value;
}

function validName(value: string): boolean { return /^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$/.test(value); }
function validKey(value: string): boolean { return value.length > 0 && value.length <= 128 && /^[\x21-\x7e]+$/.test(value); }
function validETag(value: string, id: string): boolean {
  const match = /^"sli:([0-9a-f-]{36}):([1-9][0-9]*)"$/.exec(value);
  return Boolean(match && match[1] === id && revision(match[2]));
}

export function createSliClient() {
  return {
    async list(limit = 50, cursor = '', signal?: AbortSignal): Promise<{ items: Sli[]; next_cursor: string }> {
      if (!Number.isInteger(limit) || limit < 1 || limit > 200 || cursor.length > 512) throw new SliFailure('invalid_request', 'Pagination options are invalid.');
      const query = new URLSearchParams({ limit: String(limit) });
      if (cursor) query.set('cursor', cursor);
      return (await request(`/api/v1/slis?${query}`, { method: 'GET', signal }, 200, validSliPage)).value;
    },
    async get(id: string, signal?: AbortSignal): Promise<{ sli: Sli; etag: string }> {
      const { value, response } = await request(`/api/v1/slis/${safeID(id)}`, { method: 'GET', signal }, 200, validSli);
      const etag = response.headers.get('ETag') ?? '';
      if (etag !== `"sli:${id}:${value.version}"`) throw new SliFailure('invalid_response', 'The SLI version could not be confirmed.');
      return { sli: value, etag };
    },
    async create(input: SliCreateRequest, context: AuthContext, idempotencyKey: string, signal?: AbortSignal): Promise<Sli> {
      if (!validName(input.name) || !input.display_name.trim() || !UUID.test(input.datasource_id) || input.measurement_kind !== 'event_counts'
        || input.component_mode !== 'interval_count' || !input.good_query.trim() || !input.total_query.trim() || !validKey(idempotencyKey)) {
        throw new SliFailure('invalid_request', 'Enter a valid SLI name, datasource and both queries.');
      }
      try {
        const { value, response } = await request('/api/v1/slis', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token, 'Idempotency-Key': idempotencyKey }, body: JSON.stringify(input), signal }, 201, validSli);
        if (response.headers.get('ETag') !== `"sli:${value.id}:${value.version}"`) throw new SliFailure('outcome_unknown', 'The SLI may have been created, but its version could not be confirmed. Check the SLI list before retrying.', 0, true);
        return value;
      } catch (error) {
        if (error instanceof SliFailure && (error.status === 0 || error.status >= 500) && !error.uncertain) throw new SliFailure('outcome_unknown', 'The SLI may have been created. Retry the same input with the same idempotency key or reconcile the SLI list.', error.status, true);
        throw error;
      }
    },
    async updateMetadata(id: string, etag: string, displayName: string, context: AuthContext, signal?: AbortSignal): Promise<Sli> {
      safeID(id);
      if (!validETag(etag, id) || !displayName.trim()) throw new SliFailure('invalid_request', 'The SLI version or display name is invalid.');
      try {
        const { value, response } = await request(`/api/v1/slis/${id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token, 'If-Match': etag }, body: JSON.stringify({ display_name: displayName.trim() }), signal }, 200, validSli);
        if (response.headers.get('ETag') !== `"sli:${id}:${value.version}"`) throw new SliFailure('outcome_unknown', 'The update may have succeeded. Refresh the SLI to confirm its current name.', 0, true);
        return value;
      } catch (error) {
        if (error instanceof SliFailure && (error.status === 0 || error.status >= 500) && !error.uncertain) throw new SliFailure('outcome_unknown', 'The update may have succeeded. Refresh the SLI to confirm its current name.', error.status, true);
        throw error;
      }
    },
    async observations(id: string, limit = 50, cursor = '', signal?: AbortSignal): Promise<SliObservationPage> {
      safeID(id);
      if (!Number.isInteger(limit) || limit < 1 || limit > 200 || cursor.length > 512) throw new SliFailure('invalid_request', 'Pagination options are invalid.');
      const query = new URLSearchParams({ limit: String(limit) });
      if (cursor) query.set('cursor', cursor);
      return (await request(`/api/v1/slis/${id}/observations?${query}`, { method: 'GET', signal }, 200, validObservationPage)).value;
    },
  };
}
