import type { components } from '../api/generated.ts';
import type { AuthContext } from '../auth/client.ts';
export type { AuthContext } from '../auth/client.ts';

export type Agent = components['schemas']['Agent'];
export type AgentCredential = components['schemas']['AgentCredential'];
export type AgentPage = components['schemas']['AgentPage'];
export type CredentialPage = components['schemas']['CredentialPage'];
export type LifecycleResult = components['schemas']['LifecycleResult'];
export type AgentIssuance = components['schemas']['AgentIssuanceFresh'] | components['schemas']['AgentIssuanceReplay'];
export type RotationIssuance = components['schemas']['RotationIssuanceResponse'];
export type CreateAgentInput = components['schemas']['CreateAgentRequest'];
export type RotateAgentInput = components['schemas']['RotateAgentRequest'];
export type PasswordProofInput = components['schemas']['PasswordProofRequest'];
export type AgentReadOptions = { signal?: AbortSignal; limit?: number; cursor?: string };
export type AgentMutationOptions = { signal?: AbortSignal };
export type AgentDetail = { agent: Agent; etag: string };
export type AgentFailureKind = 'rejected' | 'unavailable' | 'uncertain';

export class AgentFailure extends Error {
  readonly kind: AgentFailureKind;
  readonly code: string;
  readonly retryAfterSeconds: number | null;
  readonly requestID: string | null;

  constructor(kind: AgentFailureKind, code: string, message: string, retryAfterSeconds: number | null = null, requestID: string | null = null) {
    super(message);
    this.name = 'AgentFailure';
    this.kind = kind;
    this.code = code;
    this.retryAfterSeconds = retryAfterSeconds;
    this.requestID = requestID;
  }
}

const BASE = '/api/v1/agents';
const MAX_BODY_BYTES = 512 * 1024;
const DEADLINE_MS = 30_000;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const DECIMAL = /^(0|[1-9][0-9]*)$/;
const POSITIVE_DECIMAL = /^[1-9][0-9]*$/;
const MAX_INT64 = '9223372036854775807';
const safeMessages: Record<string, string> = {
  invalid_request: 'The request could not be accepted.', unauthenticated: 'Your session could not be confirmed.',
  forbidden: 'You are not allowed to perform this operation.', not_found: 'The requested agent was not found.',
  origin_rejected: 'This request was rejected.', secure_transport_required: 'A secure connection is required.',
  csrf_invalid: 'Refresh the page and try again.',
  conflict: 'The agent changed. Refresh it and try again.', invalid_agent_input: 'The agent details were rejected.',
  rate_limited: 'Too many requests. Wait before trying again.', request_timeout: 'The request timed out.',
  request_too_large: 'The request is too large.', unsupported_media_type: 'The request format is unsupported.',
  management_unavailable: 'Agent management is temporarily unavailable.',
};
const statusCodes: Record<number, string[]> = {
  400: ['invalid_request'], 401: ['unauthenticated'],
  403: ['forbidden', 'origin_rejected', 'secure_transport_required', 'csrf_invalid'], 404: ['not_found'],
  408: ['request_timeout'], 409: ['conflict'], 413: ['request_too_large'], 415: ['unsupported_media_type'],
  422: ['invalid_agent_input'], 429: ['rate_limited'], 503: ['management_unavailable'],
};

type Budget = { signal: AbortSignal; expired: () => boolean; timedOut: () => boolean; cleanup: () => void };
function failure(kind: AgentFailureKind, code: string): AgentFailure {
  const message = code === 'outcome_unknown' ? 'The operation may have completed. Refresh agent details before deciding what to do next.'
    : code === 'cancelled' ? 'The request was cancelled.'
      : code === 'invalid_request' ? safeMessages.invalid_request
        : code === 'invalid_response' ? 'The server returned an invalid response.'
          : code === 'request_failed' ? 'Agent management could not be reached.'
            : safeMessages[code] ?? 'Agent management is temporarily unavailable.';
  return new AgentFailure(kind, code, message);
}

function withBudget<T>(signal: AbortSignal | undefined, action: (budget: Budget) => Promise<T>): Promise<T> {
  if (signal?.aborted) return Promise.reject(failure('unavailable', 'cancelled'));
  const controller = new AbortController();
  const abort = () => controller.abort();
  signal?.addEventListener('abort', abort, { once: true });
  let timedOut = false;
  const deadline = setTimeout(() => { timedOut = true; abort(); }, DEADLINE_MS);
  const budget: Budget = { signal: controller.signal, expired: () => controller.signal.aborted, timedOut: () => timedOut,
    cleanup: () => { clearTimeout(deadline); signal?.removeEventListener('abort', abort); } };
  return action(budget).finally(budget.cleanup);
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
function validUUID(value: unknown): value is string { return typeof value === 'string' && UUID.test(value); }
function validDecimal(value: unknown, positive = false): value is string {
  if (typeof value !== 'string' || !(positive ? POSITIVE_DECIMAL : DECIMAL).test(value)) return false;
  if (value.length < MAX_INT64.length) return true;
  return value.length === MAX_INT64.length && value <= MAX_INT64;
}
function validDate(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  const match = /^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.\d{1,9})?(Z|([+-])(\d\d):(\d\d))$/.exec(value);
  if (!match) return false;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const second = Number(match[6]);
  const offsetHour = Number(match[9] ?? 0);
  const offsetMinute = Number(match[10] ?? 0);
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const daysInMonth = month === 2 ? (leapYear ? 29 : 28)
    : [4, 6, 9, 11].includes(month) ? 30
      : [1, 3, 5, 7, 8, 10, 12].includes(month) ? 31 : 0;
  return day >= 1 && day <= daysInMonth && hour <= 23 && minute <= 59 && second <= 59
    && offsetHour <= 23 && offsetMinute <= 59 && Number.isFinite(Date.parse(value));
}
function validAgent(value: unknown): value is Agent {
  return isObject(value) && validUUID(value.id) && typeof value.name === 'string' && typeof value.display_name === 'string'
    && typeof value.enabled === 'boolean' && validDecimal(value.version, true)
    && validDate(value.created_at) && validDate(value.updated_at);
}
function validCredential(value: unknown, agentID?: string): value is AgentCredential {
  return isObject(value) && validUUID(value.id) && validUUID(value.agent_id) && (agentID === undefined || value.agent_id === agentID)
    && validDate(value.issued_at) && validDate(value.expires_at)
    && (value.revoked_at === null || validDate(value.revoked_at)) && Object.hasOwn(value, 'revoked_at')
    && (value.retire_at === null || validDate(value.retire_at)) && Object.hasOwn(value, 'retire_at');
}
function validCursor(value: unknown): value is string {
  return typeof value === 'string' && value.length <= 512 && /^[\x21-\x7e]*$/.test(value);
}
function validAgentPage(value: unknown): value is AgentPage {
  return isObject(value) && Array.isArray(value.items) && value.items.length <= 200 && value.items.every(validAgent) && validCursor(value.next_cursor);
}
function validCredentialPage(value: unknown, agentID: string): value is CredentialPage {
  return isObject(value) && Array.isArray(value.items) && value.items.length <= 200
    && value.items.every((item) => validCredential(item, agentID)) && validCursor(value.next_cursor);
}
function validLifecycle(value: unknown, agentID: string): value is LifecycleResult {
  return isObject(value) && validAgent(value.agent) && value.agent.id === agentID
    && (value.credential === null || validCredential(value.credential, agentID)) && Object.hasOwn(value, 'credential')
    && typeof value.changed === 'boolean' && validDecimal(value.revoked_credential_count)
    && typeof value.replayed === 'boolean';
}
function validRevokeResult(value: unknown, agentID: string, credentialID: string): value is LifecycleResult {
  if (!validLifecycle(value, agentID) || value.credential === null || value.credential.id !== credentialID || value.credential.revoked_at === null) return false;
  return value.changed ? value.revoked_credential_count === '1' : value.revoked_credential_count === '0';
}
function validDisableResult(value: unknown, agentID: string): value is LifecycleResult {
  if (!validLifecycle(value, agentID) || value.credential !== null || value.agent.enabled) return false;
  return value.changed || value.revoked_credential_count === '0';
}
function validIssuanceBase(value: unknown, agentID?: string): value is Record<string, unknown> {
  return isObject(value) && validAgent(value.agent) && (agentID === undefined || value.agent.id === agentID)
    && validCredential(value.credential, (value.agent as Agent).id) && typeof value.replayed === 'boolean'
    && (value.secret_state === 'issued' || value.secret_state === 'secret_unavailable');
}
function validAgentIssuance(value: unknown, status: number): value is AgentIssuance {
  if (!validIssuanceBase(value)) return false;
  if (value.replayed === false) return status === 201 && value.secret_state === 'issued' && typeof value.token === 'string' && value.token.length > 0;
  return status === 200 && value.secret_state === 'secret_unavailable' && !Object.hasOwn(value, 'token');
}
function validRotation(value: unknown, agentID: string): value is RotationIssuance {
  if (!validIssuanceBase(value, agentID) || !(value.previous_credential === null || validCredential(value.previous_credential, agentID))
    || !Object.hasOwn(value, 'previous_credential')) return false;
  if (value.previous_credential !== null && value.previous_credential.id === (value.credential as AgentCredential).id) return false;
  if (value.replayed === false) return value.secret_state === 'issued' && typeof value.token === 'string' && value.token.length > 0;
  return value.secret_state === 'secret_unavailable' && !Object.hasOwn(value, 'token');
}

async function readBytes(response: Response, signal: AbortSignal): Promise<Uint8Array> {
  if (!response.body) throw new Error('missing body');
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  let pending: Promise<ReadableStreamReadResult<Uint8Array>> | null = null;
  let complete = false;
  let cancelPromise: Promise<void> | null = null;
  const cancel = () => { if (!cancelPromise) { try { cancelPromise = reader.cancel(); void cancelPromise.catch(() => undefined); } catch { /* The safe local failure is preserved. */ } } };
  let rejectAbort!: (error: Error) => void;
  const aborted = new Promise<never>((_, reject) => { rejectAbort = reject; });
  const onAbort = () => { cancel(); rejectAbort(new Error('aborted')); };
  signal.addEventListener('abort', onAbort, { once: true });
  try {
    while (true) {
      if (signal.aborted) throw new Error('aborted');
      pending = reader.read();
      const result = await Promise.race([pending, aborted]);
      if (signal.aborted) throw new Error('aborted');
      pending = null;
      if (result.done) { complete = true; break; }
      size += result.value.byteLength;
      if (size > MAX_BODY_BYTES) { cancel(); throw new Error('oversized'); }
      chunks.push(result.value);
    }
  } finally {
    signal.removeEventListener('abort', onAbort);
    if (!complete && !cancelPromise) cancel();
    const release = () => { try { reader.releaseLock(); } catch { /* A pending cancelled read can still settle. */ } };
    if (pending) void pending.then(release, release); else release();
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
  return bytes;
}
async function readJSON(response: Response, signal: AbortSignal): Promise<unknown> {
  return JSON.parse(new TextDecoder().decode(await readBytes(response, signal))) as unknown;
}
function discard(response: Response): void {
  try { const result = response.body?.cancel(); if (result) void result.catch(() => undefined); } catch { /* Closed bodies need no public detail. */ }
}
function parseProblem(value: unknown, status: number, response: Response): AgentFailure | null {
  if (!isObject(value) || typeof value.code !== 'string' || typeof value.message !== 'string'
    || typeof value.request_id !== 'string' || !Array.isArray(value.field_errors)
    || !value.field_errors.every((item) => isObject(item) && typeof item.field === 'string' && typeof item.code === 'string' && typeof item.message === 'string')) return null;
  if (!statusCodes[status]?.includes(value.code)) return null;
  const kind: AgentFailureKind = status === 503 ? 'unavailable' : 'rejected';
  const retryHeader = response.headers.get('Retry-After');
  const retryValue = retryHeader && /^\d+$/.test(retryHeader) ? Number(retryHeader) : NaN;
  const retry = Number.isSafeInteger(retryValue) && retryValue > 0 ? retryValue : null;
  const requestID = /^[0-9a-f]{32}$/.test(value.request_id) ? value.request_id : null;
  return new AgentFailure(kind, value.code, safeMessages[value.code], retry, requestID);
}
async function rejected(response: Response, signal: AbortSignal): Promise<AgentFailure | null> {
  try { return parseProblem(await readJSON(response, signal), response.status, response); }
  catch { discard(response); return null; }
}
function readFailure(signal: AbortSignal): AgentFailure {
  return failure('unavailable', signal.aborted ? 'cancelled' : 'request_failed');
}

async function request(fetcher: typeof fetch, path: string, method: 'GET' | 'POST', budget: Budget, body?: unknown, headers?: Record<string, string>): Promise<Response> {
  let result: Promise<Response>;
  try {
    result = fetcher(path, { method, ...(method === 'POST' ? { body: JSON.stringify(body) } : {}),
      credentials: 'same-origin', mode: 'same-origin', cache: 'no-store', redirect: 'error',
      headers: { ...(method === 'POST' ? { 'Content-Type': 'application/json' } : {}), ...headers }, signal: budget.signal });
  } catch { throw new Error('request failed'); }
  return new Promise((resolve, reject) => {
    const aborted = () => reject(new Error('request aborted'));
    if (budget.signal.aborted) { aborted(); return; }
    budget.signal.addEventListener('abort', aborted, { once: true });
    result.then((response) => {
      budget.signal.removeEventListener('abort', aborted);
      if (budget.signal.aborted) { discard(response); return; }
      resolve(response);
    }, (error: unknown) => { budget.signal.removeEventListener('abort', aborted); reject(error); });
  });
}
function canonicalID(value: string): boolean { return UUID.test(value); }
function validatePageOptions(options: AgentReadOptions): { limit: number; cursor?: string } {
  const limit = options.limit ?? 50;
  if (!Number.isInteger(limit) || limit < 1 || limit > 200 || (options.cursor !== undefined && !validCursor(options.cursor))) {
    throw failure('rejected', 'invalid_request');
  }
  return { limit, ...(options.cursor !== undefined ? { cursor: options.cursor } : {}) };
}
function query(options: AgentReadOptions): string {
  const params = new URLSearchParams();
  const bounded = validatePageOptions(options);
  params.set('limit', String(bounded.limit));
  if (bounded.cursor !== undefined) params.set('cursor', bounded.cursor);
  return `?${params.toString()}`;
}
function validateMutation(key: string, etag?: string, id?: string): void {
  if (typeof key !== 'string' || key.length < 1 || key.length > 128 || !/^[\x21-\x7e]+$/.test(key)) throw failure('rejected', 'invalid_request');
  if (id !== undefined) {
    const match = /^"agent:([0-9a-f-]{36}):([1-9][0-9]*)"$/.exec(etag ?? '');
    if (!match || match[1] !== id || !validDecimal(match[2], true)) throw failure('rejected', 'invalid_request');
  }
}
function read<T>(fetcher: typeof fetch, path: string, signal: AbortSignal | undefined, validate: (body: unknown, response: Response) => body is T): Promise<T> {
  return withBudget(signal, async (budget) => {
    let response: Response;
    try { response = await request(fetcher, path, 'GET', budget); }
    catch { throw readFailure(budget.signal); }
    if (response.status !== 200) {
      const problem = await rejected(response, budget.signal);
      if (problem) throw problem;
      throw failure('unavailable', 'request_failed');
    }
    try {
      const body = await readJSON(response, budget.signal);
      if (!validate(body, response)) throw failure('unavailable', 'invalid_response');
      return body;
    } catch (error) {
      if (error instanceof AgentFailure) throw error;
      throw failure('unavailable', budget.expired() ? 'cancelled' : 'invalid_response');
    }
  });
}
function mutate<T>(fetcher: typeof fetch, path: string, body: unknown, context: AuthContext, key: string, etag: string | undefined,
  signal: AbortSignal | undefined, validate: (value: unknown, status: number) => value is T, diagnoseUncertain = false): Promise<T> {
  return withBudget(signal, async (budget) => {
    let response: Response;
    try {
      const headers: Record<string, string> = { 'X-CSRF-Token': context.csrf_token, 'Idempotency-Key': key };
      if (etag !== undefined) headers['If-Match'] = etag;
      response = await request(fetcher, path, 'POST', budget, body, headers);
    } catch (error) {
      if (error instanceof AgentFailure && error.kind === 'rejected') throw error;
      if (!diagnoseUncertain) throw failure('uncertain', 'outcome_unknown');
      if (budget.timedOut()) throw failure('uncertain', 'request_timeout');
      if (budget.signal.aborted) throw failure('uncertain', 'request_cancelled');
      throw failure('uncertain', 'connection_failed');
    }
    if (response.status < 200 || response.status >= 300) {
      const problem = await rejected(response, budget.signal);
      if (problem?.kind === 'rejected') throw problem;
      if (problem && diagnoseUncertain) throw new AgentFailure('uncertain', problem.code, problem.message, problem.retryAfterSeconds, problem.requestID);
      if (problem) throw failure('uncertain', 'outcome_unknown');
      if (!diagnoseUncertain) throw failure('uncertain', 'outcome_unknown');
      if (budget.timedOut()) throw failure('uncertain', 'request_timeout');
      if (budget.signal.aborted) throw failure('uncertain', 'request_cancelled');
      throw failure('uncertain', 'unexpected_response');
    }
    try {
      const value = await readJSON(response, budget.signal);
      if (!validate(value, response.status)) throw failure('uncertain', 'invalid_response');
      return value;
    } catch (error) {
      if (!diagnoseUncertain) throw failure('uncertain', 'outcome_unknown');
      if (error instanceof AgentFailure) throw error;
      if (budget.timedOut()) throw failure('uncertain', 'request_timeout');
      if (budget.signal.aborted) throw failure('uncertain', 'request_cancelled');
      throw failure('uncertain', 'invalid_response');
    }
  });
}

export function createAgentClient(fetcher: typeof fetch = fetch) {
  function promiseCall<T>(action: () => Promise<T>): Promise<T> {
    try { return action(); } catch (error) { return Promise.reject(error); }
  }
  return {
    list(options: AgentReadOptions = {}): Promise<AgentPage> {
      return promiseCall(() => {
        const path = `${BASE}${query(options)}`;
        return read(fetcher, path, options.signal, (value): value is AgentPage => validAgentPage(value));
      });
    },
    get(agentID: string, options: AgentMutationOptions = {}): Promise<AgentDetail> {
      if (!canonicalID(agentID)) return Promise.reject(failure('rejected', 'invalid_request'));
      return withBudget(options.signal, async (budget) => {
        let response: Response;
        try { response = await request(fetcher, `${BASE}/${encodeURIComponent(agentID)}`, 'GET', budget); }
        catch { throw readFailure(budget.signal); }
        if (response.status !== 200) {
          const problem = await rejected(response, budget.signal);
          if (problem) throw problem;
          throw failure('unavailable', 'request_failed');
        }
        try {
          const value = await readJSON(response, budget.signal);
          const etag = response.headers.get('ETag');
          if (!validAgent(value) || value.id !== agentID || etag !== `"agent:${agentID}:${value.version}"`) {
            throw failure('unavailable', 'invalid_response');
          }
          return { agent: value, etag };
        } catch (error) {
          if (error instanceof AgentFailure) throw error;
          throw failure('unavailable', budget.expired() ? 'cancelled' : 'invalid_response');
        }
      });
    },
    credentials(agentID: string, options: AgentReadOptions = {}): Promise<CredentialPage> {
      if (!canonicalID(agentID)) return Promise.reject(failure('rejected', 'invalid_request'));
      return promiseCall(() => {
        const path = `${BASE}/${encodeURIComponent(agentID)}/credentials${query(options)}`;
        return read(fetcher, path, options.signal, (value): value is CredentialPage => validCredentialPage(value, agentID));
      });
    },
    create(input: CreateAgentInput, context: AuthContext, idempotencyKey: string, options: AgentMutationOptions = {}): Promise<AgentIssuance> {
      return promiseCall(() => {
        validateMutation(idempotencyKey);
        return mutate(fetcher, BASE, input, context, idempotencyKey, undefined, options.signal, validAgentIssuance, true);
      });
    },
    rotate(agentID: string, input: RotateAgentInput, context: AuthContext, etag: string, idempotencyKey: string, options: AgentMutationOptions = {}): Promise<RotationIssuance> {
      if (!canonicalID(agentID)) return Promise.reject(failure('rejected', 'invalid_request'));
      return promiseCall(() => {
        validateMutation(idempotencyKey, etag, agentID);
        return mutate(fetcher, `${BASE}/${encodeURIComponent(agentID)}/credentials/rotate`, input, context, idempotencyKey, etag, options.signal,
          (value, status): value is RotationIssuance => status === 200 && validRotation(value, agentID));
      });
    },
    revoke(agentID: string, credentialID: string, input: PasswordProofInput, context: AuthContext, etag: string, idempotencyKey: string, options: AgentMutationOptions = {}): Promise<LifecycleResult> {
      if (!canonicalID(agentID) || !canonicalID(credentialID)) return Promise.reject(failure('rejected', 'invalid_request'));
      return promiseCall(() => {
        validateMutation(idempotencyKey, etag, agentID);
        return mutate(fetcher, `${BASE}/${encodeURIComponent(agentID)}/credentials/${encodeURIComponent(credentialID)}/revoke`, input,
          context, idempotencyKey, etag, options.signal, (value, status): value is LifecycleResult => status === 200 && validRevokeResult(value, agentID, credentialID));
      });
    },
    disable(agentID: string, input: PasswordProofInput, context: AuthContext, etag: string, idempotencyKey: string, options: AgentMutationOptions = {}): Promise<LifecycleResult> {
      if (!canonicalID(agentID)) return Promise.reject(failure('rejected', 'invalid_request'));
      return promiseCall(() => {
        validateMutation(idempotencyKey, etag, agentID);
        return mutate(fetcher, `${BASE}/${encodeURIComponent(agentID)}/disable`, input, context, idempotencyKey, etag, options.signal,
          (value, status): value is LifecycleResult => status === 200 && validDisableResult(value, agentID));
      });
    },
  };
}
