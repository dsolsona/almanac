import { describe, expect, it } from 'vitest';
import type { Sli } from '../src/slis/client.ts';
import { sliVocabulary } from '../src/slis/vocabulary.ts';

// ADR-0032: SLI health names the state of the data, never a verdict on the service.
const observation = { observation_id: 'f313e073-3c86-4a7f-b7e5-c15e9626e71c', measurement_revision: '1', interval_start: '2026-11-05T11:59:45Z', interval_end: '2026-11-05T12:00:00Z', outcome: 'observed', good_count: '99', total_count: '100', error_code: null, missing_components: [], recorded_at: '2026-11-05T12:00:01Z' };
function sli(changes: Record<string, unknown>, latest: Record<string, unknown> | null = {}): Sli {
  return { agent_reachability: 'connected', activation_state: 'active', collection_progress: 'idle', freshness: 'fresh', last_observation_at: '2026-11-05T12:00:01Z', next_expected_execution_at: null, latest_observation: latest === null ? null : { ...observation, ...latest }, ...changes } as unknown as Sli;
}

describe('SLI health vocabulary', () => {
  it.each([
    ['Up to date', 'healthy', sli({})],
    ['Stale', 'stale', sli({ freshness: 'stale', agent_reachability: 'unreachable' })],
    ['Waiting for data', 'none', sli({ freshness: 'unknown' }, null)],
    ['Query error', 'warning', sli({}, { outcome: 'failed', good_count: null, total_count: null, error_code: 'timeout' })],
    ['Partial data', 'warning', sli({}, { outcome: 'incomplete', good_count: null, missing_components: ['total'] })],
    ['Zero traffic', 'none', sli({}, { good_count: '0', total_count: '0' })],
  ])('labels %s', (label, tone, item) => {
    expect(sliVocabulary(item)).toMatchObject({ label, tone });
  });

  it('never calls an SLI healthy or failing', () => {
    const labels = [sli({}), sli({}, { outcome: 'failed', error_code: 'timeout' }), sli({ freshness: 'stale' })].map((item) => sliVocabulary(item).label);
    expect(labels).not.toContain('Healthy');
    expect(labels).not.toContain('Failing');
    expect(labels).not.toContain('Not reporting');
  });

  it('keeps the reason that explains a stale or failing query', () => {
    expect(sliVocabulary(sli({ freshness: 'stale', agent_reachability: 'unreachable' })).reason).toBe('agent unreachable');
    expect(sliVocabulary(sli({}, { outcome: 'failed', error_code: 'timeout' })).reason).toBe('last query timeout');
  });
});
