import type { Page } from '@playwright/test';

export type ApiResult<T = any> = { status: number; body: T; etag: string | null };

// Calls the real API from inside the page, so requests carry the browser's session cookie,
// same-origin headers and a CSRF token exactly as the application sends them. Used to set up
// data a test is not about; the journey under test always goes through the UI.
export async function api<T = any>(page: Page, method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<ApiResult<T>> {
  if (!page.url().startsWith('http')) await page.goto('/');
  return page.evaluate(async ({ method, path, body, headers }) => {
    const context = await (await fetch('/api/v1/auth/context', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })).json();
    const sent: Record<string, string> = { 'Content-Type': 'application/json', 'X-CSRF-Token': context.csrf_token, ...headers };
    if (method !== 'GET' && !(path.startsWith('/api/v1/services/') && ['PATCH','DELETE'].includes(method)) && !sent['Idempotency-Key']) sent['Idempotency-Key'] = crypto.randomUUID();
    const response = await fetch(path, { method, headers: sent, body: body === undefined ? undefined : JSON.stringify(body) });
    const text = await response.text();
    return { status: response.status, body: text ? JSON.parse(text) : null, etag: response.headers.get('etag') };
  }, { method, path, body, headers });
}

export async function expectOk<T = any>(result: Promise<ApiResult<T>>, status = [200, 201]): Promise<ApiResult<T>> {
  const value = await result;
  if (!status.includes(value.status)) throw new Error(`API returned ${value.status}: ${JSON.stringify(value.body)}`);
  return value;
}

export const unique = (prefix: string) => `${prefix}-${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
