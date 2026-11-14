import { describe, expect, it } from 'vitest';
import {
  assertEligibleBackfill,
  backfillMode,
  backfillProfile,
  calculationMode,
  experimentSQL,
  plannedCalculationCalls,
  runBackfill,
  verifyQueryEquivalence,
} from '../perf/alert-calculation.experiment.ts';

describe('explicit write-attribution diagnostic', () => {
  it('keeps full backfill as the default and rejects ambiguous settings', () => {
    expect(backfillMode(undefined)).toBe('enabled');
    expect(backfillMode('enabled')).toBe('enabled');
    expect(backfillMode('omitted-diagnostic')).toBe('omitted-diagnostic');
    for (const value of ['', 'disabled', 'omit', 'enabled ', 'ENABLED']) {
      expect(() => backfillMode(value)).toThrow(/ALERT_BACKFILL_MODE/);
    }
  });

  it('never starts SQL for an omitted diagnostic or labels it capacity eligible', async () => {
    let writes = 0;
    const execute = async () => { writes++; return 'completed'; };
    expect(await runBackfill('omitted-diagnostic', execute)).toBe('');
    expect(writes).toBe(0);
    expect(backfillProfile('omitted-diagnostic', 1400)).toEqual({
      mode: 'omitted-diagnostic', availableBatches: 1400, plannedBatches: 0, fullWorkload: false,
    });
  });

  it('executes full backfill once and preserves its output and failure', async () => {
    let writes = 0;
    const execute = async () => { writes++; return 'completed'; };
    expect(await runBackfill(backfillMode(undefined), execute)).toBe('completed');
    expect(writes).toBe(1);
    expect(backfillProfile('enabled', 1400)).toEqual({
      mode: 'enabled', availableBatches: 1400, plannedBatches: 1400, fullWorkload: true,
    });
    await expect(runBackfill('enabled', async () => { throw new Error('write failed'); })).rejects.toThrow('write failed');
  });
});

describe('alert-calculation experiment controls', () => {
  it('defaults only an absent mode and rejects mistyped or empty explicit modes', () => {
    expect(calculationMode(undefined)).toBe('paired');
    for (const mode of ['paired', 'baseline-only', 'candidate-only'] as const) {
      expect(calculationMode(mode)).toBe(mode);
    }
    for (const mode of ['', 'baseline', 'candidate', 'PAIRED', 'paired ']) {
      expect(() => calculationMode(mode)).toThrow(/ALERT_CALCULATION_MODE/);
    }
  });

  it('retains the independent oracle and alternating calls in paired mode', () => {
    const sql = experimentSQL('paired', 'SELECT baseline_probe', 'SELECT candidate_probe');
    expect(sql).toContain('SELECT baseline_probe');
    expect(sql).toContain('SELECT candidate_probe');
    expect(sql).toContain('public.almanac_sli_observations');
    expect(sql).toContain('IF candidate_first THEN');
    expect(sql).toContain('IF NOT candidate_first THEN');
    expect(plannedCalculationCalls('paired', 800)).toEqual({ baseline: 800, candidate: 800, reference: 800 });
  });

  it('omits the candidate and raw-population oracle from baseline-only SQL', () => {
    const sql = experimentSQL('baseline-only', 'SELECT baseline_probe', 'SELECT forbidden_candidate');
    expect(sql).toContain('SELECT baseline_probe');
    expect(sql).not.toContain('forbidden_candidate');
    expect(sql).not.toContain('alert_experiment_candidate');
    expect(sql).not.toContain('public.almanac_sli_observations');
    expect(plannedCalculationCalls('baseline-only', 800)).toEqual({ baseline: 800, candidate: 0, reference: 0 });
  });

  it('omits the baseline and raw-population oracle from candidate-only SQL', () => {
    const sql = experimentSQL('candidate-only', 'SELECT forbidden_baseline', 'SELECT candidate_probe');
    expect(sql).toContain('SELECT candidate_probe');
    expect(sql).not.toContain('forbidden_baseline');
    expect(sql).not.toContain('alert_experiment_baseline');
    expect(sql).not.toContain('public.almanac_sli_observations');
    expect(plannedCalculationCalls('candidate-only', 800)).toEqual({ baseline: 0, candidate: 800, reference: 0 });
  });
});

describe('exact query evidence before workload failures are asserted', () => {
  const rows = () => Array.from({ length: 4 }, (_, index) => ({ ordinal: index + 1,
    prepared: true, good: '9007199254740992', total: '9999999999999999.000',
    observed: 5, known_empty: 0, zero_traffic: 0,
    first_observation: '2026-11-13T20:00:00+00:00', last_observation: '2026-11-13T20:05:00+00:00',
    latest_receipt: '2026-11-13T20:06:00+00:00' }));
  const batch = () => ({ mode: 'paired', sli: 'test-sli', cutoff: '2026-11-13T20:05:00Z', candidate: rows(), reference: rows() });

  it('reports complete matches independently of any later latency assertion', () => {
    const result = verifyQueryEquivalence('paired', [batch()], 1);
    expect(result).toMatchObject({ state: 'matched', windows: 4, expectedWindows: 4, mismatches: [] });
  });

  it('detects exact decimals that collapse to the same JavaScript number', () => {
    const input = batch();
    input.candidate[0]!.good = '9007199254740993';
    const result = verifyQueryEquivalence('paired', [input], 1);
    expect(result.state).toBe('mismatched');
    expect(result.mismatches).toEqual([expect.objectContaining({ ordinal: 1, field: 'good', actual: '9007199254740993', expected: '9007199254740992' })]);
  });

  it('ignores only equivalent decimal scale, keeping fractional differences exact', () => {
    const input = batch();
    input.candidate[0]!.good = '9007199254740992.0000';
    input.candidate[0]!.total = '9999999999999999';
    expect(verifyQueryEquivalence('paired', [input], 1).state).toBe('matched');
    input.candidate[0]!.total = '9999999999999999.000000000001';
    expect(verifyQueryEquivalence('paired', [input], 1).state).toBe('mismatched');
  });

  it('keeps missing batches, pending windows and absent reference rows incomplete', () => {
    expect(verifyQueryEquivalence('paired', [batch()], 2)).toMatchObject({ state: 'incomplete', windows: 4, expectedWindows: 8 });
    const pending = batch(); pending.candidate[0]!.prepared = false;
    expect(verifyQueryEquivalence('paired', [pending], 1)).toMatchObject({ state: 'incomplete', windows: 3 });
    const missing = batch(); missing.reference.pop();
    expect(verifyQueryEquivalence('paired', [missing], 1).state).toBe('incomplete');
  });

  it('never claims equivalence for a single-query control', () => {
    expect(verifyQueryEquivalence('baseline-only', [], 800)).toMatchObject({ state: 'not_checked', windows: 0 });
    expect(verifyQueryEquivalence('candidate-only', [], 800)).toMatchObject({ state: 'not_checked', windows: 0 });
  });
});

describe('backfill eligibility before workload setup', () => {
  const eligibleFrom = '2026-11-09T20:13:00Z';
  const initialHistory = '2026-11-12T20:13:00Z';
  const range = { from: eligibleFrom, to: initialHistory };
  const bounds = { measurementEffectiveFrom: eligibleFrom, sloEvaluationValidFrom: eligibleFrom, cadenceSeconds: 60 };

  it('allows the earliest planned interval exactly at both eligibility bounds', () => {
    expect(() => assertEligibleBackfill([range], bounds)).not.toThrow();
  });

  it('rejects the reproduced three-day backfill before the measurement validity', () => {
    expect(() => assertEligibleBackfill([range], { ...bounds, measurementEffectiveFrom: initialHistory })).toThrow(/measurement/);
  });

  it('rejects backfill before SLO validity even when measurement history is eligible', () => {
    expect(() => assertEligibleBackfill([range], { ...bounds, sloEvaluationValidFrom: initialHistory })).toThrow(/SLO/);
  });

  it('checks every planned range, including a later invalid one', () => {
    expect(() => assertEligibleBackfill([
      range,
      { from: '2026-11-09T20:12:00Z', to: eligibleFrom },
    ], bounds)).toThrow(/measurement/);
  });

  it('rejects invalid, reversed and off-cadence bounds before executing inserts', () => {
    for (const invalid of [
      { from: 'bad-date', to: initialHistory },
      { from: initialHistory, to: eligibleFrom },
      { from: eligibleFrom, to: eligibleFrom },
      { from: '2026-11-09T20:13:01Z', to: initialHistory },
      { from: eligibleFrom, to: '2026-11-12T20:13:01Z' },
    ]) expect(() => assertEligibleBackfill([invalid], bounds)).toThrow();
  });
});
