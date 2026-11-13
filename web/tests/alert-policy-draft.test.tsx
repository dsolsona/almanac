import { describe, expect, it } from 'vitest';
import { conditionPreset, defaultAlertConditions, formatAlertDuration, parseAlertDuration, validateAlertThreshold } from '../src/alerts/policyDraft.ts';

describe('alert policy draft values', () => {
  it.each([
    ['5 min', 300, 60, 300], ['1 HOUR', 300, 60, 3600], ['2 days', 300, 60, 172800],
    ['1.5 h', 300, 60, 5400], [' 10 minutes ', 300, 60, 600], ['2', 3600, 60, 7200],
    ['2', 300, 60, 120], ['7 min', 420, 70, 420], ['30 d', 300, 60, 2592000],
  ])('parses %s without a unit dropdown', (text, previous, cadence, expected) => {
    expect(parseAlertDuration(text, previous, cadence)).toBe(expected);
  });
  it.each(['', '0', '-1 h', 'Infinity', 'NaN', '1e3 min', '0.5 min', '31 d', '5 seconds', '1.00000000000000001 min'])('rejects invalid or inexact duration %s', text => {
    expect(parseAlertDuration(text, 300, 60)).toBeNull();
  });
  it('does not silently round a duration to a source cadence', () => {
    expect(parseAlertDuration('5 min', 420, 70)).toBeNull();
    expect(parseAlertDuration('7 min', 420, 70)).toBe(420);
  });
  it('formats canonical editable units', () => {
    expect([300, 3600, 21600, 172800].map(formatAlertDuration)).toEqual(['5 min', '1 h', '6 h', '2 d']);
  });
  it('checks reachable thresholds without floating point rounding', () => {
    expect(validateAlertThreshold('1000', '0.999')).toBeUndefined();
    expect(validateAlertThreshold('1000.000000000000000001', '0.999')).toBeTruthy();
    expect(validateAlertThreshold('14.4', '0.9')).toBeTruthy();
    for (const value of ['0', '-1', 'Infinity', 'NaN', '1e2', '']) expect(validateAlertThreshold(value, '0.999')).toBeTruthy();
  });
  it('allows the supported zero target while rejecting thresholds beyond its reachable burn', () => {
    expect(validateAlertThreshold('1', '0')).toBeUndefined();
    expect(validateAlertThreshold('0.5', '0.000')).toBeUndefined();
    expect(validateAlertThreshold('1.000000000000000001', '0')).toBeTruthy();
    expect(validateAlertThreshold('1', '1')).toBeTruthy();
  });
  it('starts with independent fast and sustained conditions and labels only exact presets', () => {
    const conditions = defaultAlertConditions();
    expect(conditions.map(conditionPreset)).toEqual(['fast', 'sustained']);
    expect(conditions.map(({ shortSeconds, longSeconds, threshold }) => [shortSeconds, longSeconds, threshold]))
      .toEqual([[300, 3600, '14.4'], [1800, 21600, '6']]);
    expect(conditionPreset({ ...conditions[0]!, threshold: '14.4000' })).toBe('fast');
    expect(conditionPreset({ ...conditions[0]!, threshold: '14.400000000000000001' })).toBe('custom');
    expect(new Set(conditions.map(c => c.id)).size).toBe(2);
    conditions[0]!.threshold = '20';
    expect(defaultAlertConditions()[0]!.threshold).toBe('14.4');
  });
});
