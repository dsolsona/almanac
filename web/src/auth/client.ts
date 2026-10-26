import type { components } from '../api/generated.ts';

export type AuthContext = components['schemas']['AuthResponse'];
export type LoginInput = components['schemas']['LoginRequest'];
export type PasswordInput = components['schemas']['PasswordRequest'];
export type AuthOptions = { signal?: AbortSignal };
type FailureKind = 'rejected' | 'unavailable' | 'uncertain';

export class AuthFailure extends Error {
  readonly kind: FailureKind;
  readonly code: string;
  readonly recovery: AuthContext | null;
  readonly retryAfterSeconds: number | null;

  constructor(kind: FailureKind, code: string, message: string, recovery: AuthContext | null = null, retryAfterSeconds: number | null = null) {
    super(message);
    this.name = 'AuthFailure';
    this.kind = kind;
    this.code = code;
    this.recovery = recovery;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

const MAX_BODY_BYTES = 64 * 1024;
const DEADLINE_MS = 30_000;
const BASE = '/api/v1/auth/';

type Budget = { signal: AbortSignal; expired: () => boolean; cleanup: () => void };

function withBudget<T>(signal: AbortSignal | undefined, action: (budget: Budget) => Promise<T>): Promise<T> {
  if (signal?.aborted) return Promise.reject(unavailable('cancelled'));
  const controller = new AbortController();
  const abort = () => controller.abort();
  signal?.addEventListener('abort', abort, { once: true });
  const deadline = setTimeout(abort, DEADLINE_MS);
  const budget: Budget = {
    signal: controller.signal,
    expired: () => controller.signal.aborted,
    cleanup: () => { clearTimeout(deadline); signal?.removeEventListener('abort', abort); },
  };
  return action(budget).finally(budget.cleanup);
}

function unavailable(code: string): AuthFailure {
  return new AuthFailure('unavailable', code, 'Authentication is temporarily unavailable. Try again.');
}

function uncertainty(recovery: AuthContext | null = null): AuthFailure {
  return new AuthFailure('uncertain', 'outcome_unknown', 'The operation may have completed. Check your sign-in state or deliberately sign in again.', recovery);
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function validContext(value: unknown): value is AuthContext {
  if (!isObject(value) || typeof value.csrf_token !== 'string' || value.csrf_token.length === 0 || !Object.hasOwn(value, 'session')) return false;
  if (value.session === null) return true;
  if (!isObject(value.session)) return false;
  return typeof value.session.user_id === 'string' && value.session.user_id.length > 0
    && typeof value.session.login === 'string' && value.session.login.length > 0
    && typeof value.session.role === 'string' && value.session.role.length > 0
    && typeof value.session.restricted === 'boolean';
}

async function readBody(response: Response, signal: AbortSignal): Promise<unknown> {
  if (!response.body) throw new Error('invalid body');
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    while (true) {
      if (signal.aborted) throw new Error('aborted');
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > MAX_BODY_BYTES) throw new Error('oversized body');
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.length; }
  return JSON.parse(new TextDecoder().decode(bytes)) as unknown;
}

function abortable<T>(promise: Promise<T>, signal: AbortSignal, cancel?: () => void): Promise<T> {
  return new Promise((resolve, reject) => {
    const aborted = () => { try { void cancel?.(); } catch { /* The read race still ends on the abort. */ } reject(new Error('operation aborted')); };
    if (signal.aborted) { aborted(); return; }
    signal.addEventListener('abort', aborted, { once: true });
    promise.then(
      (value) => { signal.removeEventListener('abort', aborted); resolve(value); },
      (error: unknown) => { signal.removeEventListener('abort', aborted); reject(error); },
    );
  });
}

function safeProblem(value: unknown): value is components['schemas']['Problem'] {
  if (!isObject(value) || typeof value.code !== 'string' || typeof value.message !== 'string'
    || typeof value.request_id !== 'string' || !Array.isArray(value.field_errors)) return false;
  return value.field_errors.every((item) => isObject(item) && typeof item.field === 'string'
    && typeof item.code === 'string' && typeof item.message === 'string');
}

const allowedCodes: Record<number, string[]> = {
  400: ['invalid_request'], 401: ['unauthenticated'],
  403: ['origin_rejected', 'secure_transport_required', 'csrf_invalid'],
  409: ['session_present'], 422: ['password_unchanged', 'password_policy'],
  429: ['rate_limited'], 405: ['method_not_allowed'], 408: ['request_timeout'],
  413: ['request_too_large'], 415: ['unsupported_media_type'],
  501: ['capability_unavailable'],
};

const rejectionMessages: Record<string, string> = {
  invalid_request: 'The request could not be accepted.', unauthenticated: 'Sign-in was not accepted.',
  origin_rejected: 'This request was rejected.', secure_transport_required: 'A secure connection is required.',
  csrf_invalid: 'Refresh the page and try again.', session_present: 'A session is already active.',
  password_unchanged: 'Choose a different password.', password_policy: 'The password does not meet the requirements.',
  rate_limited: 'Too many attempts. Wait before trying again.', method_not_allowed: 'This operation is unavailable.',
  request_timeout: 'The request was rejected before it could be completed.', request_too_large: 'The request is too large.',
  unsupported_media_type: 'The request format is unsupported.', capability_unavailable: 'This operation is unavailable.',
};

async function post(fetcher: typeof fetch, path: string, body: unknown, csrf: string | null, budget: Budget): Promise<Response> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (csrf !== null) headers['X-CSRF-Token'] = csrf;
  const request = fetcher(`${BASE}${path}`, {
    method: 'POST', body: JSON.stringify(body), credentials: 'same-origin', mode: 'same-origin',
    cache: 'no-store', redirect: 'error', headers, signal: budget.signal,
  });
  return new Promise((resolve, reject) => {
    const aborted = () => reject(new Error('request aborted'));
    if (budget.signal.aborted) { aborted(); return; }
    budget.signal.addEventListener('abort', aborted, { once: true });
    request.then(
      (response) => { budget.signal.removeEventListener('abort', aborted); resolve(response); },
      (error: unknown) => { budget.signal.removeEventListener('abort', aborted); reject(error); },
    );
  });
}

async function getValidContext(fetcher: typeof fetch, budget: Budget): Promise<AuthContext> {
  const response = await post(fetcher, 'context', {}, null, budget);
  if (response.status !== 200) throw unavailable('context_unavailable');
  const body = await abortable(readBody(response, budget.signal), budget.signal, () => response.body?.cancel());
  if (!validContext(body)) throw unavailable('invalid_context');
  return body;
}

async function parseRejection(response: Response, signal: AbortSignal): Promise<AuthFailure | null> {
  if (response.status === 501 || response.status >= 500) return null;
  let body: unknown;
  try { body = await abortable(readBody(response, signal), signal, () => response.body?.cancel()); } catch { return null; }
  if (!safeProblem(body) || !allowedCodes[response.status]?.includes(body.code)) return null;
  const header = response.headers.get('Retry-After');
  const parsed = header !== null && /^\d+$/.test(header) ? Number(header) : NaN;
  const retry = Number.isSafeInteger(parsed) && parsed > 0 ? parsed : null;
  return new AuthFailure('rejected', body.code, rejectionMessages[body.code] ?? 'The request was rejected.', null, retry);
}

async function mutation(fetcher: typeof fetch, path: 'login' | 'password', body: unknown, csrf: string, validate: (value: unknown) => value is AuthContext, budget: Budget): Promise<AuthContext> {
  try {
    const response = await post(fetcher, path, body, csrf, budget);
    if (response.status !== 200) {
      const failure = await parseRejection(response, budget.signal);
      if (failure) throw failure;
      throw uncertainty();
    }
    const value = await abortable(readBody(response, budget.signal), budget.signal, () => response.body?.cancel());
    if (!validate(value)) throw uncertainty();
    return value;
  } catch (error) {
    if (error instanceof AuthFailure && error.kind === 'rejected') throw error;
    if (budget.expired()) throw uncertainty();
    let recovery: AuthContext | null = null;
    try { recovery = await getValidContext(fetcher, budget); } catch { /* Unknown outcome is preserved. */ }
    throw uncertainty(recovery);
  }
}

export function createAuthClient(fetcher: typeof fetch = fetch) {
  return {
    context(options: AuthOptions = {}): Promise<AuthContext> {
      return withBudget(options.signal, (budget) => getValidContext(fetcher, budget).catch(() => { throw unavailable('context_unavailable'); }));
    },
    login(input: LoginInput, _context: AuthContext, options: AuthOptions = {}): Promise<AuthContext> {
      return withBudget(options.signal, (budget) => mutation(fetcher, 'login', input, _context.csrf_token,
        (value): value is AuthContext => validContext(value) && value.session !== null, budget));
    },
    changePassword(input: PasswordInput, context: AuthContext, options: AuthOptions = {}): Promise<AuthContext> {
      if (context.session === null) return Promise.reject(new AuthFailure('rejected', 'unauthenticated', 'Sign in before changing the password.'));
      return withBudget(options.signal, (budget) => mutation(fetcher, 'password', input, context.csrf_token,
        (value): value is AuthContext => validContext(value) && value.session !== null
          && !value.session.restricted && value.session.user_id === context.session?.user_id, budget));
    },
    logout(context: AuthContext, options: AuthOptions = {}): Promise<void> {
      return withBudget(options.signal, async (budget) => {
        try {
          const response = await post(fetcher, 'logout', {}, context.csrf_token, budget);
          if (response.status === 204) return;
          throw unavailable('logout_unconfirmed');
        } catch {
          throw new AuthFailure('unavailable', 'logout_unconfirmed', 'Sign-out could not be confirmed.');
        }
      });
    },
  };
}
