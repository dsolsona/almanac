import type { components } from '../api/generated.ts';
import type { AuthContext } from '../auth/client.ts';

export type Provider = components['schemas']['ProviderDescriptor'];
export type Datasource = components['schemas']['Datasource'];
export type DatasourceWrite = components['schemas']['DatasourceWrite'];
export type Diagnostic = components['schemas']['Diagnostic'];

export class DatasourceFailure extends Error {
  constructor(readonly code: string, message: string, readonly status = 0, readonly uncertain = false) {
    super(message);
    this.name = 'DatasourceFailure';
  }
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const MAX_INT64 = '9223372036854775807';
const messages: Record<string, string> = {
  unauthenticated: 'Your session has expired. Check your session and try again.',
  forbidden: 'Your role does not allow this operation.',
  not_found: 'The requested datasource or diagnostic was not found.',
  conflict: 'The datasource changed. Refresh it and review the current settings.',
  invalid_datasource_input: 'The datasource settings were rejected. Review the fields and try again.',
  configuration_pending: 'The agent is applying the latest datasource settings. No diagnostic was started; wait for readiness and retry.',
  agent_unavailable: 'The assigned agent is unavailable. No diagnostic was started; retry when it is connected.',
  diagnostic_unavailable: 'The assigned agent cannot run this diagnostic right now.',
  request_timeout: 'The request timed out. You can retry when the agent is available.',
  management_unavailable: 'Datasource management is temporarily unavailable.',
};

function object(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function hasCredentialKey(value: unknown): boolean {
  if (Array.isArray(value)) return value.some(hasCredentialKey);
  if (!object(value)) return false;
  return Object.entries(value).some(([key, child]) => ['password', 'token', 'password_secret', 'token_secret'].includes(key) || hasCredentialKey(child));
}

function validDatasource(value: unknown): value is Datasource {
  if (!object(value)) return false;
  let endpoint: URL;
  try { endpoint = new URL(String(value.endpoint)); } catch { return false; }
  const version = typeof value.version === 'string' && /^[1-9][0-9]*$/.test(value.version)
    && (value.version.length < MAX_INT64.length || value.version.length === MAX_INT64.length && value.version <= MAX_INT64);
  return typeof value.id === 'string' && UUID.test(value.id) && version && typeof value.name === 'string' && typeof value.display_name === 'string'
    && value.provider === 'prometheus' && value.configuration_format === 'prometheus-http-v1'
    && typeof value.endpoint === 'string' && ['http:', 'https:'].includes(endpoint.protocol) && !endpoint.username && !endpoint.password && !endpoint.hash
    && object(value.tls) && typeof value.tls.insecure_skip_verify === 'boolean'
    && (value.tls.ca_pem === null || typeof value.tls.ca_pem === 'string' && value.tls.ca_pem.length <= 64 * 1024) && object(value.authentication)
    && ['none', 'basic', 'bearer'].includes(String(value.authentication.mode))
    && (value.authentication.username === null || typeof value.authentication.username === 'string')
    && typeof value.authentication.password_configured === 'boolean' && typeof value.authentication.token_configured === 'boolean'
    && typeof value.polling_interval_seconds === 'number' && Number.isInteger(value.polling_interval_seconds) && value.polling_interval_seconds >= 15 && value.settling_delay_seconds === 60 && typeof value.agent_id === 'string' && UUID.test(value.agent_id)
    && typeof value.created_at === 'string' && Number.isFinite(Date.parse(value.created_at)) && typeof value.updated_at === 'string' && Number.isFinite(Date.parse(value.updated_at));
}

function validRevision(value: unknown, allowZero: boolean): value is string {
  return typeof value === 'string' && (allowZero ? /^(0|[1-9][0-9]*)$/ : /^[1-9][0-9]*$/).test(value);
}

function validProvider(value: unknown): value is Provider {
  const availabilities = ['available', 'applying', 'offline', 'disabled', 'unreported', 'configuration_failed', 'incompatible', 'planned', 'unsupported'];
  const failureCodes = ['invalid_snapshot', 'incompatible', 'apply_failed'];
  return object(value) && value.provider === 'prometheus' && value.configuration_format === 'prometheus-http-v1'
    && availabilities.includes(String(value.availability)) && Array.isArray(value.operations) && value.operations.length <= 16
    && value.operations.every((operation) => operation === 'connection_test' || operation === 'query_preview')
    && (value.desired_configuration_revision === null || validRevision(value.desired_configuration_revision, false))
    && (value.applied_configuration_revision === null || validRevision(value.applied_configuration_revision, true))
    && (value.configuration_failure_code === null || failureCodes.includes(String(value.configuration_failure_code)))
    && (value.configuration_failure_revision === null || validRevision(value.configuration_failure_revision, false));
}

function validDiagnostic(value: unknown): value is Diagnostic {
  return object(value) && typeof value.id === 'string' && UUID.test(value.id)
    && ['pending', 'running', 'succeeded', 'failed', 'canceled', 'timed_out', 'unknown'].includes(String(value.state))
    && ['connection_test', 'query_preview'].includes(String(value.operation))
    && typeof value.created_at === 'string' && Number.isFinite(Date.parse(value.created_at)) && typeof value.deadline_at === 'string' && Number.isFinite(Date.parse(value.deadline_at))
    && (value.result_class === null || ['complete', 'empty', 'incomplete'].includes(String(value.result_class)))
    && (value.result === null || object(value.result))
    && (value.error_code === null || typeof value.error_code === 'string' && value.error_code.length <= 64)
    && (value.message === null || typeof value.message === 'string' && value.message.length <= 1024);
}

function safeDiagnostic(value: Diagnostic): Diagnostic {
  const descriptions: Record<string, string> = {
    authentication_failed: 'The datasource rejected authentication.',
    network_failed: 'The assigned agent could not reach the datasource.',
    tls_failed: 'The datasource TLS connection could not be verified.',
    query_failed: 'The datasource rejected the query.',
    request_timeout: 'The datasource diagnostic timed out.',
    assignment_revoked: 'The datasource assignment changed before the diagnostic could complete.',
    rate_limited: 'The datasource rate limited the diagnostic.',
    unsupported: 'The assigned agent does not support this operation.',
  };
  const message = value.error_code ? descriptions[value.error_code] ?? 'The diagnostic failed. Check agent connectivity and datasource settings.'
    : value.state === 'succeeded' ? 'The diagnostic completed.' : null;
  return { ...value, error_code: value.error_code && Object.hasOwn(descriptions, value.error_code) ? value.error_code : (value.error_code ? 'diagnostic_failed' : null), message };
}

async function json(response: Response): Promise<unknown> {
  const declared = Number(response.headers.get('Content-Length'));
  if (Number.isFinite(declared) && declared > 512 * 1024) throw new DatasourceFailure('invalid_response', 'The server returned an invalid response.');
  if (!response.body) throw new DatasourceFailure('invalid_response', 'The server returned an invalid response.');
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > 512 * 1024) { await reader.cancel(); throw new DatasourceFailure('invalid_response', 'The server returned an invalid response.'); }
      chunks.push(value);
    }
  } finally { reader.releaseLock(); }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
  try { return JSON.parse(new TextDecoder().decode(bytes)) as unknown; } catch { throw new DatasourceFailure('invalid_response', 'The server returned an invalid response.'); }
}

async function request<T>(path: string, init: RequestInit, expected: number, validate: (value: unknown) => value is T): Promise<{ value: T; response: Response }> {
  let response: Response;
  try {
    response = await fetch(path, { ...init, credentials: 'same-origin', mode: 'same-origin', cache: 'no-store', redirect: 'error' });
  } catch {
    if (init.signal?.aborted) throw new DatasourceFailure('request_canceled', 'The datasource request was canceled.');
    throw new DatasourceFailure('request_failed', 'Datasource management could not be reached.');
  }
  const value = await json(response);
  if (response.status !== expected) {
    const code = object(value) && typeof value.code === 'string' ? value.code : 'management_unavailable';
    throw new DatasourceFailure(code, messages[code] ?? 'Datasource management is temporarily unavailable.', response.status);
  }
  if (hasCredentialKey(value)) throw new DatasourceFailure('invalid_response', 'The server returned an unsafe datasource response.');
  if (!validate(value)) throw new DatasourceFailure('invalid_response', 'The server returned an invalid datasource response.');
  return { value, response };
}

function safeUUID(id: string): string {
  if (!UUID.test(id)) throw new DatasourceFailure('invalid_request', 'The datasource identifier is invalid.');
  return id;
}

export function createDatasourceClient() {
  return {
    async providers(agentID?: string, signal?: AbortSignal): Promise<Provider[]> {
      const query = agentID ? `?agent_id=${encodeURIComponent(safeUUID(agentID))}` : '';
      const { value } = await request(`/api/v1/providers${query}`, { method: 'GET', signal }, 200,
        (body): body is { items: Provider[] } => object(body) && Array.isArray(body.items) && body.items.length <= 256 && body.items.every(validProvider));
      return value.items;
    },
    async list(limit = 50, cursor = '', signal?: AbortSignal): Promise<{ items: Datasource[]; next_cursor: string }> {
      if (!Number.isInteger(limit) || limit < 1 || limit > 200 || cursor.length > 512) throw new DatasourceFailure('invalid_request', 'Pagination options are invalid.');
      const params = new URLSearchParams({ limit: String(limit) });
      if (cursor) params.set('cursor', cursor);
      const { value } = await request(`/api/v1/datasources?${params}`, { method: 'GET', signal }, 200,
        (body): body is { items: Datasource[]; next_cursor: string } => object(body) && Array.isArray(body.items)
          && body.items.length <= 200 && body.items.every(validDatasource) && typeof body.next_cursor === 'string' && body.next_cursor.length <= 512);
      return value;
    },
    async get(id: string, signal?: AbortSignal): Promise<{ datasource: Datasource; etag: string }> {
      const { value, response } = await request(`/api/v1/datasources/${safeUUID(id)}`, { method: 'GET', signal }, 200, validDatasource);
      const etag = response.headers.get('ETag') ?? '';
      if (etag !== `"datasource:${id}:${value.version}"`) throw new DatasourceFailure('invalid_response', 'The datasource version could not be confirmed.');
      return { datasource: value, etag };
    },
    async save(input: DatasourceWrite, context: AuthContext, idempotencyKey: string, etag?: string, signal?: AbortSignal): Promise<Datasource> {
      const match = etag?.match(/^"datasource:([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}):([1-9][0-9]*)"$/);
      const id = match?.[1];
      if (etag && (!match || match[2]!.length > MAX_INT64.length || match[2]!.length === MAX_INT64.length && match[2]! > MAX_INT64)) throw new DatasourceFailure('invalid_request', 'The datasource version is invalid.');
      if (!etag && (idempotencyKey.length < 1 || idempotencyKey.length > 128 || !/^[\x21-\x7e]+$/.test(idempotencyKey))) throw new DatasourceFailure('invalid_request', 'The idempotency key is invalid.');
      const headers = new Headers({ 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token });
      const method = etag ? 'PUT' : 'POST';
      if (etag) headers.set('If-Match', etag); else headers.set('Idempotency-Key', idempotencyKey);
      const path = etag ? `/api/v1/datasources/${safeUUID(id!)}` : '/api/v1/datasources';
      try {
        const { value, response } = await request(path, { method, headers, body: JSON.stringify(input), signal }, etag ? 200 : 201, validDatasource);
        const resultID = etag ? id : value.id;
        if (response.headers.get('ETag') !== `"datasource:${resultID}:${value.version}"`) throw new DatasourceFailure('invalid_response', 'The datasource save may have completed, but its new version could not be confirmed.', 0, true);
        return value;
      } catch (error) {
        if (error instanceof DatasourceFailure && (error.status === 0 || error.status >= 500) && error.code !== 'invalid_request') throw new DatasourceFailure('outcome_unknown', 'The save may have completed. Reconcile its outcome before editing the datasource again.', error.status, true);
        throw error;
      }
    },
    async connectionTest(input: components['schemas']['ConnectionTestRequest'], context: AuthContext, signal?: AbortSignal): Promise<Diagnostic> {
      let value: Diagnostic;
      try { ({ value } = await request('/api/v1/datasources/connection-tests', {
          method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token }, body: JSON.stringify(input), signal,
        }, 202, validDiagnostic));
      } catch (error) { if (uncertainDiagnosticOutcome(error)) throw new DatasourceFailure('outcome_unknown', 'The diagnostic may still be running. Check its status before starting another test.', error.status, true); throw error; }
      return safeDiagnostic(value);
    },
    async preview(id: string, query: string, context: AuthContext, signal?: AbortSignal): Promise<Diagnostic> {
      let value: Diagnostic;
      try { ({ value } = await request(`/api/v1/datasources/${safeUUID(id)}/query-previews`, {
          method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token }, body: JSON.stringify({ query }), signal,
        }, 202, validDiagnostic));
      } catch (error) { if (uncertainDiagnosticOutcome(error)) throw new DatasourceFailure('outcome_unknown', 'The preview may still be running. Check its status before starting another preview.', error.status, true); throw error; }
      return safeDiagnostic(value);
    },
    async diagnostic(id: string, signal?: AbortSignal): Promise<Diagnostic> {
      const { value } = await request(`/api/v1/diagnostics/${safeUUID(id)}`, { method: 'GET', signal }, 200, validDiagnostic);
      return safeDiagnostic(value);
    },
    async cancelDiagnostic(id: string, context: AuthContext, signal?: AbortSignal): Promise<Diagnostic> {
      const { value } = await request(`/api/v1/diagnostics/${safeUUID(id)}/cancel`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token }, body: '{}', signal,
      }, 200, validDiagnostic);
      return safeDiagnostic(value);
    },
  };
}

function uncertainDiagnosticOutcome(error: unknown): error is DatasourceFailure {
  return error instanceof DatasourceFailure && (error.status === 0 || error.status >= 500)
    && !['configuration_pending', 'agent_unavailable', 'diagnostic_unavailable'].includes(error.code);
}
