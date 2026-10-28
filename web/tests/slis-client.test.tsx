import { afterEach, describe, expect, it, vi } from 'vitest';
import { createSliClient } from '../src/slis/client.ts';
import type { AuthContext } from '../src/auth/client.ts';

const context: AuthContext = { csrf_token: 'csrf-token', session: { user_id: 'user-1', login: 'editor', role: 'editor', restricted: false } };
const id = '8ed93d7e-51e3-4a3e-9222-3e8a612de20a';
const sourceID = 'b13e0739-3c86-4a7f-b7e5-c15e9626e71c';
function sli() { return { id, name: 'request-success', display_name: 'Request success', version: '2', measurement_revision: '1', datasource_id: sourceID, datasource_version: '4', agent_id: '4b13e073-3c86-4a7f-b7e5-c15e9626e71c', agent_reachability: 'connected', collection_progress: 'idle', measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: 'sum(rate(requests_total{status=~"2.."}[1m]))', total_query: 'sum(rate(requests_total[1m]))', polling_interval_seconds: 15, settling_delay_seconds: 60, effective_from: '2026-10-27T12:00:15Z', activation_state: 'scheduled', freshness: 'fresh', latest_observation: { observation_id: id, measurement_revision: '1', interval_start: '2026-10-27T11:59:45Z', interval_end: '2026-10-27T12:00:00Z', outcome: 'observed', good_count: '99', total_count: '100', error_code: null, missing_components: [], recorded_at: '2026-10-27T12:00:01Z' }, last_attempt_at: '2026-10-27T12:00:01Z', last_observation_at: '2026-10-27T12:00:01Z', next_expected_execution_at: '2026-10-27T12:00:15Z', created_at: '2026-10-27T11:59:00Z', updated_at: '2026-10-27T12:00:00Z' }; }
function response(body: unknown, status = 200, headers: Record<string, string> = {}) { return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } }); }
function historyPage(items: unknown[]) { return { items, next_cursor: '', earliest_queryable_interval_start: '2026-10-27T11:00:00Z', inspected_start: '2026-10-27T11:59:45Z', inspected_end: '2026-10-27T12:00:00Z', missing_intervals: [] }; }

afterEach(() => vi.unstubAllGlobals());

describe('SLI client', () => {
  it.each([['reassignment_pending', 409, 'This agent is completing a datasource move. Wait for it to finish or cancel the move before changing collection settings.'], ['reassignment_settings_conflict', 422, 'Save connection changes before changing the assigned agent.']])('explains %s', async (code, status, message) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response({ code }, status)));
    await expect(createSliClient().get(id)).rejects.toMatchObject({ code, status, message });
  });
  it('creates with CSRF and idempotency, then verifies the resource ETag', async () => {
    const fetcher = vi.fn().mockResolvedValue(response(sli(), 201, { ETag: `"sli:${id}:2"` }));
    vi.stubGlobal('fetch', fetcher);
    const input = { name: 'request-success', display_name: 'Request success', datasource_id: sourceID, measurement_kind: 'event_counts', component_mode: 'interval_count', good_query: 'sum(rate(requests_total{status=~"2.."}[1m]))', total_query: 'sum(rate(requests_total[1m]))' } as const;
    await expect(createSliClient().create(input, context, 'create-key')).resolves.toMatchObject({ version: '2', activation_state: 'scheduled' });
    expect(fetcher.mock.calls[0]?.[0]).toBe('/api/v1/slis');
    expect(new Headers(fetcher.mock.calls[0]?.[1]?.headers).get('X-CSRF-Token')).toBe('csrf-token');
    expect(new Headers(fetcher.mock.calls[0]?.[1]?.headers).get('Idempotency-Key')).toBe('create-key');
  });

  it('preserves exact count strings and rejects missing-component values', async () => {
    const observation = sli().latest_observation;
    const fetcher = vi.fn().mockResolvedValue(response(historyPage([observation])));
    vi.stubGlobal('fetch', fetcher);
    await expect(createSliClient().observations(id)).resolves.toMatchObject({ items: [{ good_count: '99', total_count: '100' }] });
    fetcher.mockResolvedValue(response(historyPage([{ ...observation, outcome: 'incomplete', good_count: '0', total_count: '0', missing_components: ['total'] }])));
    await expect(createSliClient().observations(id)).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('accepts int64 versions beyond binary64 precision and reports uncertain updates', async () => {
    const version = '9223372036854775807';
    const fetcher = vi.fn().mockResolvedValue(response({ ...sli(), version }, 200, { ETag: `"sli:${id}:${version}"` }));
    vi.stubGlobal('fetch', fetcher);
    await expect(createSliClient().updateMetadata(id, `"sli:${id}:${version}"`, 'New name', context)).resolves.toMatchObject({ version });
    fetcher.mockRejectedValue(new TypeError('connection lost'));
    await expect(createSliClient().updateMetadata(id, `"sli:${id}:${version}"`, 'New name', context)).rejects.toMatchObject({ code: 'outcome_unknown', uncertain: true });
  });
});
